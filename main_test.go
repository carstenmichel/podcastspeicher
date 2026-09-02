package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"podcastspeicher/internal/mirror"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
		got, err := parsePollInterval(c.in)
		if (err != nil) != c.err {
			t.Errorf("parsePollInterval(%q) error = %v, want error %v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("parsePollInterval(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLoadShows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.txt")
	content := "\uFEFFhttps://a.example/feed # primary\n" +
		"\n" +
		"\t\n" +
		"https://b.example/feed\n" +
		"https://a.example/feed\n" +
		"# full line comment\n" +
		"https://c.example/feed # note\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadShows(path, discardLogger())
	if err != nil {
		t.Fatalf("loadShows: %v", err)
	}
	want := []string{
		"https://a.example/feed",
		"https://b.example/feed",
		"https://c.example/feed",
	}
	if len(got) != len(want) {
		t.Fatalf("shows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shows[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadShowsCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.txt")
	got, err := loadShows(path, discardLogger())
	if err != nil {
		t.Fatalf("loadShows: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("shows = %v, want none", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("shows.txt was not created: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("created shows.txt is not empty: %d bytes", fi.Size())
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
	m := mirror.New(dir, discardLogger())

	writeShows := func(urls ...string) {
		t.Helper()
		if err := os.WriteFile(showsFile, []byte(strings.Join(urls, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A 404ing show must not prevent the healthy show from being mirrored.
	writeShows(srv.URL+"/bad/feed.xml", srv.URL+"/good/feed.xml")
	pollOnce(context.Background(), m, showsFile, discardLogger())
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
	pollOnce(context.Background(), m, showsFile, discardLogger())
	if _, err := os.Stat(filepath.Join(dir, "Other Show", "2026-08-29 - Other Show Ep.mp3")); err != nil {
		t.Fatalf("newly added show was not picked up without a restart: %v", err)
	}
}

func TestLoadShowsPreservesURLFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.txt")
	content := "https://x.example/feed#section-1\nhttps://y.example/feed # real comment\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadShows(path, discardLogger())
	if err != nil {
		t.Fatalf("loadShows: %v", err)
	}
	want := []string{
		"https://x.example/feed#section-1",
		"https://y.example/feed",
	}
	if len(got) != len(want) {
		t.Fatalf("shows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shows[%d] = %q, want %q", i, got[i], want[i])
		}
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

func TestRunStartupPollAndShutdown(t *testing.T) {
	t.Run("startup poll before first tick", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DATA_DIR", dir)
		t.Setenv("POLL_INTERVAL", "1s")
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
		go func() { errCh <- run(ctx) }()
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
		if err := os.WriteFile(filepath.Join(dir, "shows.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		pr, pw := stderrCapture(t)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- run(ctx) }()
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
		srv := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)
		if err := os.WriteFile(filepath.Join(dir, "shows.txt"), []byte(srv.URL+"/missing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pr, pw := stderrCapture(t)
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- run(ctx) }()
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
