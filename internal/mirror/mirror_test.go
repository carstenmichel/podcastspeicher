package mirror

import (
	"bytes"
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
	"sync/atomic"
	"testing"
	"time"

	"podcastspeicher/internal/feed"
	"podcastspeicher/internal/registry"
)

var testNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

type epSpec struct {
	title   string
	guid    string
	file    string
	pubDate string
}

type env struct {
	dir           string
	m             *Mirror
	Server        *httptest.Server
	setFeedBody   func(path, body string)
	setFeedStatus func(status int)
	setCLShort    func(short bool)
	releaseSlow   chan struct{}
	audioRequests *atomic.Int32
}

func newEnv(t *testing.T) *env {
	t.Helper()
	var (
		mu          sync.Mutex
		feeds       = map[string]string{}
		feedStatus  = http.StatusOK
		clShort     bool
		audioHits   atomic.Int32
		slowRelease = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail.mp3":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/empty.mp3":
			w.WriteHeader(http.StatusOK)
		case "/big.mp3":
			_, _ = io.WriteString(w, strings.Repeat("b", 64))
		case "/slow.mp3":
			// Header first, then block on the body until the test releases
			// it: gives the test a deterministic mid-download window.
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-slowRelease
			_, _ = io.WriteString(w, "slow-bytes")
		case "/cl.mp3":
			mu.Lock()
			short := clShort
			mu.Unlock()
			if short {
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "0123456789abcdef")
				return
			}
			fmt.Fprintf(w, "audio-bytes:%s", r.URL.Path)
		default:
			if strings.HasSuffix(r.URL.Path, ".mp3") {
				audioHits.Add(1)
				fmt.Fprintf(w, "audio-bytes:%s", r.URL.Path)
				return
			}
			mu.Lock()
			body, status := feeds[r.URL.Path], feedStatus
			mu.Unlock()
			if status != http.StatusOK {
				http.Error(w, "feed error", status)
				return
			}
			if body == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	m := New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.FeedClient = srv.Client()
	m.DownloadClient = srv.Client()
	m.Now = func() time.Time { return testNow }
	return &env{
		dir:           m.DataDir,
		m:             m,
		Server:        srv,
		releaseSlow:   slowRelease,
		audioRequests: &audioHits,
		setFeedBody: func(path, body string) {
			mu.Lock()
			feeds[path] = body
			mu.Unlock()
		},
		setFeedStatus: func(status int) {
			mu.Lock()
			feedStatus = status
			mu.Unlock()
		},
		setCLShort: func(short bool) {
			mu.Lock()
			clShort = short
			mu.Unlock()
		},
	}
}

func (e *env) feedURL(path string) string { return e.Server.URL + path }

func (e *env) pollPath(t *testing.T, path string) error {
	t.Helper()
	_, err := e.m.PollShow(context.Background(), e.feedURL(path))
	return err
}

func (e *env) poll(t *testing.T) error { return e.pollPath(t, "/feed.xml") }

func (e *env) showDir() string { return filepath.Join(e.dir, "Test Show") }

func (e *env) rowsIn(t *testing.T, dir string) []registry.Entry {
	t.Helper()
	reg, err := registry.Load(filepath.Join(dir, "podcast.md"))
	if err != nil {
		t.Fatalf("load registry in %s: %v", dir, err)
	}
	return reg.Entries()
}

func (e *env) rows(t *testing.T) []registry.Entry { return e.rowsIn(t, e.showDir()) }

func (e *env) partFiles() []string {
	entries, _ := os.ReadDir(e.showDir())
	var out []string
	for _, en := range entries {
		if !en.IsDir() && IsTempName(en.Name()) {
			out = append(out, en.Name())
		}
	}
	return out
}

func feedXMLWithTitle(base, title string, eps ...epSpec) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>` + title + `</title>`)
	for _, e := range eps {
		b.WriteString("<item><title>" + e.title + "</title>")
		if e.guid != "" {
			b.WriteString("<guid>" + e.guid + "</guid>")
		}
		if e.pubDate != "" {
			b.WriteString("<pubDate>" + e.pubDate + "</pubDate>")
		}
		if e.file != "" {
			fmt.Fprintf(&b, `<enclosure url="%s%s" type="audio/mpeg"/>`, base, e.file)
		}
		b.WriteString("</item>")
	}
	b.WriteString("</channel></rss>")
	return b.String()
}

func feedXML(base string, eps ...epSpec) string {
	return feedXMLWithTitle(base, "Test Show", eps...)
}

func baseEps() []epSpec {
	return []epSpec{
		{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
		{title: "Ep 2", guid: "g2", file: "/ep2.mp3", pubDate: "Mon, 29 Aug 2026 13:00:00 +0000"},
		{title: "Ep 3", guid: "g3", file: "/ep3.mp3", pubDate: "Tue, 30 Aug 2026 08:30:00 -0700"},
	}
}

func TestFirstPoll(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatalf("poll: %v", err)
	}
	wantFiles := map[string]string{
		"2026-08-29 - Ep 1.mp3": "/ep1.mp3",
		"2026-08-29 - Ep 2.mp3": "/ep2.mp3",
		"2026-08-30 - Ep 3.mp3": "/ep3.mp3",
	}
	for name, src := range wantFiles {
		data, err := os.ReadFile(filepath.Join(e.showDir(), name))
		if err != nil {
			t.Fatalf("missing %q: %v", name, err)
		}
		if !strings.Contains(string(data), src) {
			t.Errorf("%s content = %q, want bytes from %s", name, data, src)
		}
	}
	rows := e.rows(t)
	if len(rows) != 3 {
		t.Fatalf("registry rows = %d, want 3: %v", len(rows), rows)
	}
	for i, wantGUID := range []string{"g1", "g2", "g3"} {
		if rows[i].GUID != wantGUID {
			t.Errorf("row %d GUID = %q, want %q", i, rows[i].GUID, wantGUID)
		}
	}
}

func TestRepeatPollDownloadsNothing(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if got := e.audioRequests.Load(); got != 3 {
		t.Fatalf("audio requests after first poll = %d, want 3", got)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if got := e.audioRequests.Load(); got != 3 {
		t.Errorf("audio requests after repeat poll = %d, want 3 (zero new downloads)", got)
	}
	if rows := e.rows(t); len(rows) != 3 {
		t.Errorf("registry rows = %d, want 3 (unchanged)", len(rows))
	}
}

func TestFeedGrows(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()[:2]...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if got := e.audioRequests.Load(); got != 3 {
		t.Errorf("audio requests = %d, want 3 (only the new episode downloaded)", got)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-30 - Ep 3.mp3")); err != nil {
		t.Errorf("new episode missing: %v", err)
	}
	if rows := e.rows(t); len(rows) != 3 {
		t.Errorf("registry rows = %d, want 3", len(rows))
	}
}

func TestFileVanishedGapFill(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(e.showDir(), "2026-08-29 - Ep 2.mp3")
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if got := e.audioRequests.Load(); got != 4 {
		t.Errorf("audio requests = %d, want 4 (gap fill re-downloaded ep2)", got)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("gap fill did not restore the file: %v", err)
	}
	if rows := e.rows(t); len(rows) != 3 {
		t.Errorf("registry rows = %d, want 3 (no duplicate row)", len(rows))
	}
	if parts := e.partFiles(); len(parts) != 0 {
		t.Errorf("leftover temp files: %v", parts)
	}
}

func TestDownloadInterrupted(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
		epSpec{title: "Ep 2", guid: "g2", file: "/fail.mp3", pubDate: "Mon, 29 Aug 2026 13:00:00 +0000"},
		epSpec{title: "Ep 3", guid: "g3", file: "/ep3.mp3", pubDate: "Tue, 30 Aug 2026 08:30:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-29 - Ep 1.mp3")); err != nil {
		t.Errorf("ep1 should be present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-30 - Ep 3.mp3")); err != nil {
		t.Errorf("ep3 should be present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-29 - Ep 2.mp3")); !os.IsNotExist(err) {
		t.Errorf("failed download must not leave a target file")
	}
	if rows := e.rows(t); len(rows) != 2 {
		t.Errorf("registry rows = %d, want 2 (no row for failed download)", len(rows))
	}
	if parts := e.partFiles(); len(parts) != 0 {
		t.Errorf("temp file not cleaned up: %v", parts)
	}
	// Fix the feed and poll again: the failed episode is retried.
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
		epSpec{title: "Ep 2", guid: "g2", file: "/ep2.mp3", pubDate: "Mon, 29 Aug 2026 13:00:00 +0000"},
		epSpec{title: "Ep 3", guid: "g3", file: "/ep3.mp3", pubDate: "Tue, 30 Aug 2026 08:30:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-29 - Ep 2.mp3")); err != nil {
		t.Errorf("retried episode missing: %v", err)
	}
}

func TestNeverOverwritesExistingFile(t *testing.T) {
	e := newEnv(t)
	dir := e.showDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A verified registry (matching Feed line) so the poller reuses this
	// directory instead of treating it as unverifiable.
	header := "# Test Show\n\nFeed: " + e.feedURL("/feed.xml") + "\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n"
	if err := os.WriteFile(filepath.Join(dir, "podcast.md"), []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	preExisting := filepath.Join(dir, "2026-08-29 - Ep 1.mp3")
	if err := os.WriteFile(preExisting, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(preExisting)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sentinel" {
		t.Errorf("existing file was overwritten: %q", data)
	}
	// ep1's pre-existing file triggers the ledger self-heal row (g1), plus
	// the rows for ep2 and ep3.
	if rows := e.rows(t); len(rows) != 3 {
		t.Errorf("registry rows = %d, want 3 (self-healed ep1 row plus ep2 and ep3)", len(rows))
	}
}

func TestNonRSSFeedIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Atom</title></feed>`)
	if err := e.poll(t); err == nil {
		t.Fatal("poll succeeded on Atom feed, want error")
	}
	if _, err := os.Stat(e.showDir()); !os.IsNotExist(err) {
		t.Errorf("show dir must not be created for a non-RSS feed")
	}
}

func TestFetchFails(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	e.setFeedStatus(http.StatusNotFound)
	if err := e.poll(t); err == nil {
		t.Fatal("poll succeeded on 404 feed, want error")
	}
	if _, err := os.Stat(e.showDir()); !os.IsNotExist(err) {
		t.Errorf("show dir must not be created when the feed fetch fails")
	}
}

func TestEpisodeWithoutStableID(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "No ID", guid: "", file: "/no1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(e.showDir(), "2026-08-29 - No ID.mp3")
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("episode without stable id was not downloaded: %v", err)
	}
	if rows := e.rows(t); len(rows) != 1 || rows[0].GUID != "" {
		t.Fatalf("registry rows = %v, want one row with empty GUID", rows)
	}
	// No stable id: dedupe is by file existence, so a vanished file is
	// re-downloaded (duplicate tolerated).
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("re-download after file loss failed: %v", err)
	}
	if rows := e.rows(t); len(rows) != 1 {
		t.Errorf("registry rows = %d, want 1 (no duplicate row)", len(rows))
	}
}

func TestUnparseablePubDateFallsBackToDownloadDate(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Ep X", guid: "gx", file: "/epx.mp3", pubDate: "not a date"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-31 - Ep X.mp3")); err != nil {
		t.Errorf("expected file named with the download date: %v", err)
	}
}

func TestShowName(t *testing.T) {
	cases := []struct {
		title, feedURL, want string
	}{
		{"My Show", "https://example.com/feed", "My Show"},
		{"  A: B/C  ", "https://example.com/feed", "A_ B_C"},
		{"", "https://feeds.example.com/show/rss.xml", "feeds.example.com"},
		{"", "not a url", "untitled-show"},
	}
	for _, c := range cases {
		if got := ShowName(c.title, c.feedURL); got != c.want {
			t.Errorf("ShowName(%q, %q) = %q, want %q", c.title, c.feedURL, got, c.want)
		}
	}
}

// --- patch tests ---

func TestGUIDSkipAcrossFilenameChange(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	// Same GUID and title, but the feed now publishes a different date, so
	// the computed filename differs. The episode is already mirrored: no new
	// download, no duplicate row, no file under the new name.
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Wed, 02 Sep 2026 12:00:00 -0700"},
	))
	before := e.audioRequests.Load()
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if got := e.audioRequests.Load(); got != before {
		t.Errorf("audio requests went %d -> %d, want zero (GUID already mirrored)", before, got)
	}
	if rows := e.rows(t); len(rows) != 1 {
		t.Errorf("registry rows = %d, want 1 (no duplicate row)", len(rows))
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-09-02 - Ep 1.mp3")); !os.IsNotExist(err) {
		t.Error("recomputed filename must not be created for an already-mirrored GUID")
	}
}

func TestSameNameDifferentGUIDs(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Clash", guid: "g1", file: "/c1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
		epSpec{title: "Clash", guid: "g2", file: "/c2.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	wantSuffixed := "2026-08-29 - Clash-" + shortHash("g2") + ".mp3"
	for _, name := range []string{"2026-08-29 - Clash.mp3", wantSuffixed} {
		if _, err := os.Stat(filepath.Join(e.showDir(), name)); err != nil {
			t.Errorf("missing %q: %v (no episode may be lost to a name collision)", name, err)
		}
	}
	rows := e.rows(t)
	if len(rows) != 2 {
		t.Fatalf("registry rows = %d, want 2: %v", len(rows), rows)
	}
	if rows[0].GUID != "g1" || rows[0].File != "2026-08-29 - Clash.mp3" {
		t.Errorf("row 0 = %+v, want g1 under the plain name", rows[0])
	}
	if rows[1].GUID != "g2" || rows[1].File != wantSuffixed {
		t.Errorf("row 1 = %+v, want g2 under the deterministic suffixed name", rows[1])
	}
	// The suffix is deterministic: a repeat poll changes nothing.
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if rows := e.rows(t); len(rows) != 2 {
		t.Errorf("registry rows after repeat poll = %d, want 2", len(rows))
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), wantSuffixed)); err != nil {
		t.Errorf("suffixed file vanished after repeat poll: %v", err)
	}
}

func TestLongTitleIsTruncated(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x", 250)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: long, guid: "gl", file: "/long.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	// The title is budgeted so the TOTAL on-disk name stays within
	// maxFileNameBytes: date(10) + " - "(3) + title + ext(4). For ASCII the
	// rune cap may bind first; compute the expectation with truncateName.
	budget := maxFileNameBytes - len("2026-08-29") - len(" - ") - len(".mp3")
	wantTitle := truncateName(long, budget)
	wantName := "2026-08-29 - " + wantTitle + ".mp3"
	if _, err := os.Stat(filepath.Join(e.showDir(), wantName)); err != nil {
		t.Fatalf("expected truncated file %q: %v", wantName, err)
	}
	if len(wantName) > maxFileNameBytes {
		t.Errorf("truncated name still exceeds %d bytes: %d", maxFileNameBytes, len(wantName))
	}
	rows := e.rows(t)
	if len(rows) != 1 || rows[0].Title != long {
		t.Errorf("registry rows = %v, want one row keeping the full title", rows)
	}
}

func TestContentLengthMismatch(t *testing.T) {
	e := newEnv(t)
	e.setCLShort(true)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "CL", guid: "gcl", file: "/cl.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(e.showDir(), "2026-08-29 - CL.mp3")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("content-length mismatch must not archive a file")
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Errorf("registry rows = %d, want 0", len(rows))
	}
	// A following successful poll downloads the episode.
	e.setCLShort(false)
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("fixed download must land: %v", err)
	}
	if rows := e.rows(t); len(rows) != 1 {
		t.Errorf("registry rows = %d, want 1", len(rows))
	}
}

func TestZeroByteBody(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Empty", guid: "ge", file: "/empty.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-29 - Empty.mp3")); !os.IsNotExist(err) {
		t.Error("zero-byte body must not be archived")
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Errorf("registry rows = %d, want 0", len(rows))
	}
}

func TestDownloadSizeCap(t *testing.T) {
	e := newEnv(t)
	e.m.MaxDownloadBytes = 4
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Big", guid: "gbig", file: "/big.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.showDir(), "2026-08-29 - Big.mp3")); !os.IsNotExist(err) {
		t.Error("cap breach must not archive a file")
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Errorf("registry rows = %d, want 0", len(rows))
	}
	if parts := e.partFiles(); len(parts) != 0 {
		t.Errorf("leftover temp files after cap breach: %v", parts)
	}
}

func TestShowDirStableAcrossRename(t *testing.T) {
	e := newEnv(t)
	ep := epSpec{title: "Ep 1", guid: "g1", file: "/ep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"}
	e.setFeedBody("/f/feed.xml", feedXMLWithTitle(e.Server.URL, "Original Title", ep))
	if err := e.pollPath(t, "/f/feed.xml"); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(e.dir, "Original Title")
	if _, err := os.Stat(orig); err != nil {
		t.Fatalf("original show dir missing: %v", err)
	}
	// The channel is renamed: the feed URL is unchanged, so the existing
	// directory must be reused and nothing re-downloaded.
	e.setFeedBody("/f/feed.xml", feedXMLWithTitle(e.Server.URL, "Renamed Title", ep))
	before := e.audioRequests.Load()
	if err := e.pollPath(t, "/f/feed.xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "Renamed Title")); !os.IsNotExist(err) {
		t.Error("a new directory must not be spawned for a channel rename")
	}
	if got := e.audioRequests.Load(); got != before {
		t.Errorf("audio requests went %d -> %d, want zero re-downloads", before, got)
	}
	if rows := e.rowsIn(t, orig); len(rows) != 1 {
		t.Errorf("registry rows = %d, want 1", len(rows))
	}
}

func TestSameTitleDifferentFeedsGetSeparateDirs(t *testing.T) {
	e := newEnv(t)
	urlB := e.feedURL("/b/feed.xml")
	e.setFeedBody("/a/feed.xml", feedXMLWithTitle(e.Server.URL, "Twin Show",
		epSpec{title: "A Ep", guid: "ga", file: "/aep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	e.setFeedBody("/b/feed.xml", feedXMLWithTitle(e.Server.URL, "Twin Show",
		epSpec{title: "B Ep", guid: "gb", file: "/bep1.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.pollPath(t, "/a/feed.xml"); err != nil {
		t.Fatal(err)
	}
	if err := e.pollPath(t, "/b/feed.xml"); err != nil {
		t.Fatal(err)
	}
	wantB := "Twin Show-" + shortHash(urlB)
	dirA, dirB := filepath.Join(e.dir, "Twin Show"), filepath.Join(e.dir, wantB)
	for _, dir := range []string{dirA, dirB} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("show dir %q missing: %v (same-titled feeds must not share a directory)", dir, err)
		}
		if len(entries) != 2 { // episode file + podcast.md
			t.Errorf("dir %q has %d entries, want 2", dir, len(entries))
		}
	}
	if rows := e.rowsIn(t, dirA); len(rows) != 1 || rows[0].GUID != "ga" {
		t.Errorf("dir A rows = %v, want the A episode only", rows)
	}
	if rows := e.rowsIn(t, dirB); len(rows) != 1 || rows[0].GUID != "gb" {
		t.Errorf("dir B rows = %v, want the B episode only", rows)
	}
}

func TestExtFor(t *testing.T) {
	cases := []struct {
		name string
		ep   feed.Episode
		want string
	}{
		{"audio/mp4", feed.Episode{EnclosureType: "audio/mp4"}, ".m4a"},
		{"mime with params", feed.Episode{EnclosureType: "audio/mpeg; rate=64"}, ".mp3"},
		{"unknown mime, m4a url", feed.Episode{EnclosureType: "application/octet-stream", EnclosureURL: "https://x.example/e.m4a"}, ".m4a"},
		{"unknown mime, php url", feed.Episode{EnclosureType: "application/octet-stream", EnclosureURL: "https://x.example/e.php"}, ".mp3"},
		{"empty type, no url extension", feed.Episode{EnclosureURL: "https://x.example/stream"}, ".mp3"},
	}
	for _, c := range cases {
		if got := extFor(c.ep); got != c.want {
			t.Errorf("%s: extFor = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestEpisodeFileNameDateParsing(t *testing.T) {
	cases := []struct {
		pubDate string
		want    string
	}{
		{"Mon, 29 Aug 2026 12:00:00 -0700", "2026-08-29"},
		{"Mon, 29 Aug 2026 12:00:00 +0000", "2026-08-29"},
		{"Mon, 29 Aug 2026 12:00:00 UTC", "2026-08-29"},
		{"Mon, 29 Aug 2026 12:00:00 PST", "2026-08-29"},
		{"29 Aug 2026 12:00:00 -0700", "2026-08-29"},
		{"29 Aug 2026 12:00:00 UTC", "2026-08-29"},
		{"Mon, 29 Aug 26 12:00:00 -0700", "2026-08-29"},
		{"29 Aug 26 12:00:00 -0700", "2026-08-29"},
		{"2026-08-29T12:00:00-07:00", "2026-08-29"},
		{"2026-08-29", "2026-08-29"},
		{"Mon, 29 Aug 2026 12:00:00 +0000 (GMT)", "2026-08-29"},
		{"Mon, 29 Aug 2026 12:00:00Z", "2026-08-29"},
		{"garbage", "2026-08-31"}, // unparseable: download date fallback
	}
	for _, c := range cases {
		name, date := EpisodeFileName(feed.Episode{Title: "T", PubDate: c.pubDate}, testNow)
		if date != c.want {
			t.Errorf("pubDate %q: date = %q, want %q", c.pubDate, date, c.want)
		}
		if want := c.want + " - T.mp3"; name != want {
			t.Errorf("pubDate %q: filename = %q, want %q", c.pubDate, name, want)
		}
	}
}

// --- round-2 patch tests ---

func TestFeedShrinksArchiveUntouched(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	names := []string{
		"2026-08-29 - Ep 1.mp3",
		"2026-08-29 - Ep 2.mp3",
		"2026-08-30 - Ep 3.mp3",
	}
	snapshots := make(map[string][]byte, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(e.showDir(), name))
		if err != nil {
			t.Fatalf("missing %q before shrink: %v", name, err)
		}
		snapshots[name] = data
	}
	// The feed is pruned down to a single episode.
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()[:1]...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	for name, want := range snapshots {
		data, err := os.ReadFile(filepath.Join(e.showDir(), name))
		if err != nil {
			t.Errorf("pruned episode %q vanished from the archive: %v", name, err)
			continue
		}
		if !bytes.Equal(data, want) {
			t.Errorf("pruned episode %q changed bytes", name)
		}
	}
	if rows := e.rows(t); len(rows) != 3 {
		t.Errorf("registry rows = %d, want 3 (pruned episodes keep their rows)", len(rows))
	}
}

func TestMultibyteTitleFitsByteBudget(t *testing.T) {
	e := newEnv(t)
	cjk := strings.Repeat("播", 150) // 3 bytes per rune: 450 bytes raw
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: cjk, guid: "gcjk", file: "/cjk.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(e.showDir())
	if err != nil {
		t.Fatal(err)
	}
	var mp3 string
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".mp3") {
			mp3 = en.Name()
		}
	}
	if mp3 == "" {
		t.Fatalf("no episode file was created; entries: %v", entries)
	}
	if len(mp3) > maxFileNameBytes {
		t.Errorf("on-disk name is %d bytes, exceeds the %d byte budget: %q", len(mp3), maxFileNameBytes, mp3)
	}
	rows := e.rows(t)
	if len(rows) != 1 || rows[0].File != mp3 {
		t.Errorf("registry rows = %v, want one row for %q", rows, mp3)
	}
}

func TestStaleTempCleanupKeepsArchiveFiles(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()...))
	dir := e.showDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A verified registry (matching Feed line) so the poller reuses this
	// directory instead of treating it as unverifiable.
	header := "# Test Show\n\nFeed: " + e.feedURL("/feed.xml") + "\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n"
	if err := os.WriteFile(filepath.Join(dir, "podcast.md"), []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "2026-08-29 - Ep 1.mp3")
	if err := os.WriteFile(archive, []byte("keep-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "2026-08-29 - Ep 1.mp3.part-7")
	if err := os.WriteFile(planted, []byte("stale temp"), 0o644); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(dir, ".part-123")
	if err := os.WriteFile(hidden, []byte("user file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planted); !os.IsNotExist(err) {
		t.Error("planted stale temp file was not removed")
	}
	if _, err := os.Stat(hidden); err != nil {
		t.Errorf("hidden user file .part-123 must survive the poll: %v", err)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep-me" {
		t.Errorf("archive file was modified: %q", data)
	}
}

// waitTempInFlight waits until the in-flight download's temp file appears in
// a show directory (created by the poller) and returns that directory.
func (e *env) waitTempInFlight(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(e.dir)
		if err == nil {
			for _, en := range entries {
				if !en.IsDir() {
					continue
				}
				subDir := filepath.Join(e.dir, en.Name())
				sub, serr := os.ReadDir(subDir)
				if serr != nil {
					continue
				}
				for _, se := range sub {
					if IsTempName(se.Name()) {
						return subDir
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("download temp file never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// releaseSlowOnCleanup makes sure a test failure cannot leave the /slow.mp3
// handler blocked (which would hang the test binary's server cleanup).
func releaseSlowOnCleanup(t *testing.T, e *env) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-e.releaseSlow:
		default:
			close(e.releaseSlow)
		}
	})
}

func TestPreRenameGuardKeepsConcurrentFile(t *testing.T) {
	e := newEnv(t)
	releaseSlowOnCleanup(t, e)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Slow", guid: "gs", file: "/slow.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	errCh := make(chan error, 1)
	go func() {
		_, err := e.m.PollShow(context.Background(), e.feedURL("/feed.xml"))
		errCh <- err
	}()
	// Wait until the download is in flight (its temp file exists).
	showDir := e.waitTempInFlight(t)
	target := filepath.Join(showDir, "2026-08-29 - Slow.mp3")
	// A concurrent writer claims the target path mid-download.
	if err := os.WriteFile(target, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	close(e.releaseSlow)
	// A per-episode download failure is logged, not fatal to the show cycle.
	if err := <-errCh; err != nil {
		t.Fatalf("poll returned %v, want nil (single-episode show)", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sentinel" {
		t.Errorf("concurrent file was overwritten: %q", data)
	}
	if rows := e.rowsIn(t, showDir); len(rows) != 0 {
		t.Errorf("registry rows = %d, want 0 (aborted download leaves no row)", len(rows))
	}
	entries, _ := os.ReadDir(showDir)
	for _, en := range entries {
		if IsTempName(en.Name()) {
			t.Errorf("temp file not removed: %v", en.Name())
		}
	}
}

func TestContextCancelMidDownload(t *testing.T) {
	e := newEnv(t)
	releaseSlowOnCleanup(t, e)
	// Two episodes: the first download is blocked mid-stream; the second one
	// exercises the between-episodes ctx check that aborts the poll.
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL,
		epSpec{title: "Slow", guid: "gs", file: "/slow.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
		epSpec{title: "Next", guid: "gn", file: "/next.mp3", pubDate: "Mon, 29 Aug 2026 12:00:00 -0700"},
	))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := e.m.PollShow(ctx, e.feedURL("/feed.xml"))
		errCh <- err
	}()
	showDir := e.waitTempInFlight(t)
	cancel()
	if err := <-errCh; err == nil {
		t.Fatal("poll returned nil after context cancellation")
	}
	if _, err := os.Stat(filepath.Join(showDir, "2026-08-29 - Slow.mp3")); !os.IsNotExist(err) {
		t.Error("cancelled download must not leave a target file")
	}
	if _, err := os.Stat(filepath.Join(showDir, "2026-08-29 - Next.mp3")); !os.IsNotExist(err) {
		t.Error("aborted poll must not start the next episode")
	}
	if rows := e.rowsIn(t, showDir); len(rows) != 0 {
		t.Errorf("registry rows = %d, want 0", len(rows))
	}
	entries, _ := os.ReadDir(showDir)
	for _, en := range entries {
		if IsTempName(en.Name()) {
			t.Errorf("temp file not removed after cancel: %v", en.Name())
		}
	}
}

func TestLedgerSelfHealAfterAppendFailure(t *testing.T) {
	e := newEnv(t)
	e.setFeedBody("/feed.xml", feedXML(e.Server.URL, baseEps()[:1]...))
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if rows := e.rows(t); len(rows) != 1 {
		t.Fatalf("registry rows = %d, want 1 after first poll", len(rows))
	}
	regPath := filepath.Join(e.showDir(), "podcast.md")
	// Simulate the row being lost (e.g. a one-time failed Append): keep the
	// header, drop the row.
	headerOnly := "# Test Show\n\nFeed: " + e.feedURL("/feed.xml") + "\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n"
	if err := os.WriteFile(regPath, []byte(headerOnly), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read-only: the self-heal Append must fail this cycle.
	if err := os.Chmod(regPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	if rows := e.rows(t); len(rows) != 0 {
		t.Fatalf("registry rows = %d, want 0 while the registry is read-only", len(rows))
	}
	// Writable again: the self-heal Append succeeds.
	if err := os.Chmod(regPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.poll(t); err != nil {
		t.Fatal(err)
	}
	rows := e.rows(t)
	if len(rows) != 1 {
		t.Fatalf("registry rows = %d, want 1 (self-healed)", len(rows))
	}
	if rows[0].GUID != "g1" || rows[0].File != "2026-08-29 - Ep 1.mp3" {
		t.Errorf("self-healed row = %+v, want g1 / 2026-08-29 - Ep 1.mp3", rows[0])
	}
	entries, err := os.ReadDir(e.showDir())
	if err != nil {
		t.Fatal(err)
	}
	mp3s := 0
	for _, en := range entries {
		if !en.IsDir() && strings.HasSuffix(en.Name(), ".mp3") {
			mp3s++
		}
	}
	if mp3s != 1 {
		t.Errorf("episode files = %d, want 1 (no duplicate download)", mp3s)
	}
}

func TestIsTempName(t *testing.T) {
	for _, name := range []string{
		"episode.mp3.part-123",
		"episode.part-0",
	} {
		if !IsTempName(name) {
			t.Errorf("IsTempName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"episode.mp3",
		".part-123", // empty prefix — not a temp
		"episode.part-",
		"episode.part-abc",
		"",
	} {
		if IsTempName(name) {
			t.Errorf("IsTempName(%q) = true, want false", name)
		}
	}
}
