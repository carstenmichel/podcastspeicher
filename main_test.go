package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"podcastspeicher/internal/mirror"
	"podcastspeicher/internal/settings"
	"podcastspeicher/internal/status"
	"podcastspeicher/internal/subs"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stderrLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func TestParsePollInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{`not-a-duration`, 0, true},
		{`0s`, 0, true},
		{`-1h`, 0, true},
		{`500ms`, 0, true},
		{` 5s `, 5 * time.Second, false},
		{`6h`, 6 * time.Hour, false},
	}
	for _, c := range cases {
		got, err := settings.ParseInterval(c.in)
		if (err != nil) != c.err {
			t.Errorf("ParseInterval(%q) error = %v, want error %v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("ParseInterval(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPollOnceFailureIsolationAndShowPickup(t *testing.T) {
	var (
		mu    sync.Mutex
		feeds = map[string]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bad/feed.xml":
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, ".mp3"):
			fmt.Fprintf(w, "bytes-%s", r.URL.Path)
		default:
			mu.Lock()
			body := feeds[r.URL.Path]
			mu.Unlock()
			if body == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)

	feedBody := func(title, guid, file string) string {
		return `<?xml version="1.0"?><rss version="2.0"><channel><title>` + title + `</title>` +
			`<item><title>` + title + ` Ep</title><guid>` + guid + `</guid>` +
			`<pubDate>Mon, 29 Aug 2026 12:00:00 -0700</pubDate>` +
			fmt.Sprintf(`<enclosure url="%s%s" type="audio/mpeg"/>`, srv.URL, file) +
			`</item></channel></rss>`
	}
	feeds["/good/feed.xml"] = feedBody("Good Show", "g1", "/good.mp3")
	feeds["/other/feed.xml"] = feedBody("Other Show", "g2", "/other.mp3")

	dir := t.TempDir()
	showsFile := filepath.Join(dir, "shows.txt")
	subStore := &subs.Store{Path: showsFile, Log: discardLogger()}
	m := mirror.New(dir, discardLogger())

	writeShows := func(urls ...string) {
		t.Helper()
		if err := os.WriteFile(showsFile, []byte(strings.Join(urls, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A 404ing show must not prevent the healthy show from being mirrored.
	writeShows(srv.URL+"/bad/feed.xml", srv.URL+"/good/feed.xml")
	pollOnce(context.Background(), m, subStore, status.NewStore(), discardLogger())
	if _, err := os.Stat(filepath.Join(dir, "Good Show", "2026-08-29 - Good Show Ep.mp3")); err != nil {
		t.Fatalf("healthy show was not mirrored: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if en.IsDir() && en.Name() != "Good Show" {
			t.Fatalf("unexpected show dir %q (a failed feed must not create one)", en.Name())
		}
	}

	// Editing shows.txt between pollOnce calls picks up the new show without
	// a restart.
	writeShows(srv.URL + "/other/feed.xml")
	pollOnce(context.Background(), m, subStore, status.NewStore(), discardLogger())
	if _, err := os.Stat(filepath.Join(dir, "Other Show", "2026-08-29 - Other Show Ep.mp3")); err != nil {
		t.Fatalf("newly added show was not picked up without a restart: %v", err)
	}
}

func TestEffectiveInterval(t *testing.T) {
	dir := t.TempDir()
	st := &settings.Store{Path: filepath.Join(dir, "settings.json"), Log: discardLogger()}

	// No settings.json: the env value applies.
	got, err := effectiveInterval(st, "1h30m", discardLogger())
	if err != nil || got != 90*time.Minute {
		t.Fatalf("effectiveInterval(no file, env=1h30m) = %v, %v; want 1h30m", got, err)
	}
	// No settings.json, no env: the default applies.
	got, err = effectiveInterval(st, "", discardLogger())
	if err != nil || got != 6*time.Hour {
		t.Fatalf("effectiveInterval(no file, no env) = %v, %v; want 6h", got, err)
	}
	// A valid settings.json override beats the env.
	if err := st.SetPollInterval("2h"); err != nil {
		t.Fatal(err)
	}
	got, err = effectiveInterval(st, "1h30m", discardLogger())
	if err != nil || got != 2*time.Hour {
		t.Fatalf("effectiveInterval(override=2h, env=1h30m) = %v, %v; want 2h", got, err)
	}
	// An invalid settings.json value falls back to the env.
	if err := os.WriteFile(st.Path, []byte(`{"poll_interval":"garbage"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = effectiveInterval(st, "1h30m", discardLogger())
	if err != nil || got != 90*time.Minute {
		t.Fatalf("effectiveInterval(invalid override, env=1h30m) = %v, %v; want 1h30m", got, err)
	}
	// An invalid env value with no valid override is fatal.
	if err := os.Remove(st.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := effectiveInterval(st, "garbage", discardLogger()); err == nil {
		t.Fatal("effectiveInterval(invalid env) should fail")
	}
}

// stderrCapture redirects os.Stderr into a pipe for the duration of a
// run() call. It returns the read end (for asserting on the output) and the
// write end, which the test closes once run() has returned so the read end
// reaches EOF.
func stderrCapture(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	old := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	t.Cleanup(func() {
		os.Stderr = old
	})
	return pr, pw
}

// freeAddr reserves a free localhost TCP port for the config server.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestRunStartupPollAndShutdown(t *testing.T) {
	t.Run("startup poll before first tick", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DATA_DIR", dir)
		t.Setenv("POLL_INTERVAL", "1s")
		t.Setenv("HTTP_ADDR", freeAddr(t))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/feed.xml":
				w.Header().Set("Content-Type", "application/rss+xml")
				_, _ = io.WriteString(w,
					`<?xml version="1.0"?><rss version="2.0"><channel><title>IT Show</title>`+
						`<item><title>It Ep</title><guid>g-it</guid>`+
						`<pubDate>Mon, 29 Aug 2026 12:00:00 -0700</pubDate>`+
						fmt.Sprintf(`<enclosure url="http://%s/ep.mp3" type="audio/mpeg"/>`, r.Host)+
						`</item></channel></rss>`)
			case strings.HasSuffix(r.URL.Path, ".mp3"):
				_, _ = io.WriteString(w, "it-bytes")
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		if err := os.WriteFile(filepath.Join(dir, "shows.txt"), []byte(srv.URL+"/feed.xml\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		pr, pw := stderrCapture(t)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		start := time.Now()
		go func() { errCh <- run(ctx, stderrLogger()) }()
		target := filepath.Join(dir, "IT Show", "2026-08-29 - It Ep.mp3")
		for {
			if _, err := os.Stat(target); err == nil {
				break
			}
			if time.Since(start) > 950*time.Millisecond {
				t.Fatal("episode was not archived before the first tick: the startup poll is missing")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if elapsed := time.Since(start); elapsed >= 950*time.Millisecond {
			t.Fatalf("episode appeared after %v, not in the startup poll", elapsed)
		}
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("run returned %v on clean shutdown, want nil", err)
		}
		pw.Close()
		logData, _ := io.ReadAll(pr)
		pr.Close()
		if !strings.Contains(string(logData), "show polled") {
			t.Errorf("stderr missing the poll success log: %q", logData)
		}
	})

	t.Run("no shows configured warning", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DATA_DIR", dir)
		t.Setenv("POLL_INTERVAL", "1s")
		t.Setenv("HTTP_ADDR", freeAddr(t))
		if err := os.WriteFile(filepath.Join(dir, "shows.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		pr, pw := stderrCapture(t)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- run(ctx, stderrLogger()) }()
		time.Sleep(300 * time.Millisecond)
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("run returned %v, want nil", err)
		}
		pw.Close()
		logData, _ := io.ReadAll(pr)
		pr.Close()
		if !strings.Contains(string(logData), "no shows configured") {
			t.Errorf("stderr missing the no-shows warning: %q", logData)
		}
	})

	t.Run("404 feed is skipped this cycle", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DATA_DIR", dir)
		t.Setenv("POLL_INTERVAL", "1s")
		t.Setenv("HTTP_ADDR", freeAddr(t))
		srv := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)
		if err := os.WriteFile(filepath.Join(dir, "shows.txt"), []byte(srv.URL+"/missing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pr, pw := stderrCapture(t)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- run(ctx, stderrLogger()) }()
		time.Sleep(300 * time.Millisecond)
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("run returned %v, want nil", err)
		}
		pw.Close()
		logData, _ := io.ReadAll(pr)
		pr.Close()
		if !strings.Contains(string(logData), "show skipped this cycle") {
			t.Errorf("stderr missing the skip log: %q", logData)
		}
	})
}

// TestRunConfigPage drives the full story-2 acceptance path over HTTP: the
// page is served, shows are added and removed through the API (mirroring
// starts on the next poll, removal stops it while files stay on disk), and
// the poll interval override is persisted.
func TestRunConfigPage(t *testing.T) {
	var (
		mu    sync.Mutex
		hitsA int
		hitsB int
	)
	feedBody := func(host, title, guid string) string {
		return `<?xml version="1.0"?><rss version="2.0"><channel><title>` + title + `</title>` +
			`<item><title>` + title + ` E1</title><guid>` + guid + `</guid>` +
			`<pubDate>Mon, 29 Aug 2026 12:00:00 -0700</pubDate>` +
			fmt.Sprintf(`<enclosure url="http://%s/%s.mp3" type="audio/mpeg"/>`, host, guid) +
			`</item></channel></rss>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch r.URL.Path {
		case "/a.xml":
			hitsA++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, feedBody(r.Host, "Show A", "ga1"))
			return
		case "/b.xml":
			hitsB++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, feedBody(r.Host, "Show B", "gb1"))
			return
		}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".mp3") {
			_, _ = io.WriteString(w, "bytes")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("POLL_INTERVAL", "1s")
	addr := freeAddr(t)
	t.Setenv("HTTP_ADDR", addr)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, discardLogger()) }()
	defer cancel()

	base := "http://" + addr
	// Wait for the config server to accept connections; fail fast if run()
	// died early (e.g. it could not bind the port).
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			t.Fatalf("run exited before the config page came up: %v", err)
		default:
		}
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("config server did not come up within 5s")
		}
		time.Sleep(25 * time.Millisecond)
	}
	get := func(path string) (*http.Response, error) {
		return http.Get(base + path)
	}
	postJSON := func(path, body string) *http.Response {
		resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}
	putJSON := func(path, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		return resp
	}

	waitFile := func(rel string, within time.Duration) {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("file %q did not appear within %v", rel, within)
	}

	t.Run("config page is served", func(t *testing.T) {
		resp, err := get("/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "podcastspeicher") {
			t.Error("index does not look like the config page")
		}
	})

	t.Run("health endpoint", func(t *testing.T) {
		resp, err := get("/healthz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("empty show list", func(t *testing.T) {
		resp, err := get("/api/shows")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"shows":[]}`+"\n" {
			t.Errorf("GET /api/shows = %s, want {\"shows\":[]}", body)
		}
	})

	t.Run("add show starts mirroring on the next poll", func(t *testing.T) {
		resp := postJSON("/api/shows", `{"url":"`+srv.URL+`/a.xml"}`)
		defer resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("POST /api/shows = %d, want 201", resp.StatusCode)
		}
		waitFile(filepath.Join("Show A", "2026-08-29 - Show A E1.mp3"), 5*time.Second)

		resp = postJSON("/api/shows", `{"url":"`+srv.URL+`/b.xml"}`)
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("POST /api/shows (B) = %d, want 201", resp.StatusCode)
		}
		waitFile(filepath.Join("Show B", "2026-08-29 - Show B E1.mp3"), 5*time.Second)
	})

	t.Run("add validates input", func(t *testing.T) {
		resp := postJSON("/api/shows", `{"url":"`+srv.URL+`/a.xml"}`)
		resp.Body.Close()
		if resp.StatusCode != 409 {
			t.Errorf("duplicate add = %d, want 409", resp.StatusCode)
		}
		resp = postJSON("/api/shows", `{"url":"not a url"}`)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("invalid url = %d, want 400", resp.StatusCode)
		}
		resp = postJSON("/api/shows", `{"url":"ftp://example.com/feed"}`)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("non-http url = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("remove show stops downloads but keeps files", func(t *testing.T) {
		resp, err := http.NewRequest(http.MethodDelete, base+"/api/shows?url="+srv.URL+"/a.xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		doResp, err := http.DefaultClient.Do(resp)
		if err != nil {
			t.Fatal(err)
		}
		doResp.Body.Close()
		if doResp.StatusCode != 200 {
			t.Fatalf("DELETE /api/shows = %d, want 200", doResp.StatusCode)
		}

		mu.Lock()
		before := hitsA
		mu.Unlock()
		time.Sleep(1400 * time.Millisecond)
		mu.Lock()
		after := hitsA
		mu.Unlock()
		if after != before {
			t.Errorf("removed show was polled again (hits %d -> %d)", before, after)
		}
		if _, err := os.Stat(filepath.Join(dir, "Show A", "2026-08-29 - Show A E1.mp3")); err != nil {
			t.Errorf("removed show's file must stay on disk: %v", err)
		}

		resp2, err := http.NewRequest(http.MethodDelete, base+"/api/shows?url="+srv.URL+"/nope.xml", nil)
		if err != nil {
			t.Fatal(err)
		}
		doResp2, err := http.DefaultClient.Do(resp2)
		if err != nil {
			t.Fatal(err)
		}
		doResp2.Body.Close()
		if doResp2.StatusCode != 404 {
			t.Errorf("DELETE unknown show = %d, want 404", doResp2.StatusCode)
		}
	})

	t.Run("poll interval override is persisted", func(t *testing.T) {
		resp, err := get("/api/settings")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), `"poll_interval":"1s"`) {
			t.Errorf("GET /api/settings = %s, want the effective env interval 1s", body)
		}

		resp = putJSON(`/api/settings`, `{"poll_interval":"2s"}`)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("PUT /api/settings = %d, want 200", resp.StatusCode)
		}
		if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
			t.Fatalf("settings.json was not written: %v", err)
		}
		resp, err = get("/api/settings")
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), `"poll_interval":"2s"`) {
			t.Errorf("GET /api/settings after PUT = %s, want 2s", body)
		}

		resp = putJSON(`/api/settings`, `{"poll_interval":"0s"}`)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("invalid interval = %d, want 400", resp.StatusCode)
		}
		resp = putJSON(`/api/settings`, `{"poll_interval":"garbage"}`)
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("unparseable interval = %d, want 400", resp.StatusCode)
		}
	})

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("run returned %v on clean shutdown, want nil", err)
	}
}
