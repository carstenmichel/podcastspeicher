package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "podcast.md"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := r.Entries(); len(got) != 0 {
		t.Errorf("Entries = %v, want empty", got)
	}
}

func TestEnsureHeaderCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := r.EnsureHeader("My Show", "https://example.com/feed"); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "# My Show\n\nFeed: https://example.com/feed\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n"
	if string(data) != want {
		t.Errorf("header file = %q, want %q", data, want)
	}
}

func TestEnsureHeaderNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	existing := "# Pre-existing\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(path)
	if err := r.EnsureHeader("Other", "https://other.example"); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != existing {
		t.Errorf("file changed: %q", data)
	}
}

func TestAppendAndParseRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, _ := Load(path)
	if err := r.EnsureHeader("My Show", "https://example.com/feed"); err != nil {
		t.Fatal(err)
	}
	rows := []Entry{
		{Date: "2026-08-29", Title: "Ep 1", GUID: "abc1", File: "2026-08-29 - Ep 1.mp3"},
		{Date: "2026-08-30", Title: "Ep 2", GUID: "abc2", File: "2026-08-30 - Ep 2.mp3"},
	}
	for _, e := range rows {
		if err := r.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := fresh.Entries()
	if len(got) != len(rows) {
		t.Fatalf("Entries = %v, want %v", got, rows)
	}
	for i := range rows {
		if got[i] != rows[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], rows[i])
		}
	}
}

func TestParseSpecExample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	content := "# <Show Title>\n\nFeed: <url>\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n" +
		"| 2026-08-29 | Ep 123 | abc123 | 2026-08-29 - Ep 123.mp3 |\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := r.Entries()
	if len(got) != 1 {
		t.Fatalf("Entries = %v, want 1", got)
	}
	want := Entry{Date: "2026-08-29", Title: "Ep 123", GUID: "abc123", File: "2026-08-29 - Ep 123.mp3"}
	if got[0] != want {
		t.Errorf("row = %+v, want %+v", got[0], want)
	}
}

func TestEntriesWithGUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, _ := Load(path)
	if err := r.EnsureHeader("S", "u"); err != nil {
		t.Fatal(err)
	}
	rows := []Entry{
		{Date: "d1", Title: "a", GUID: "g1", File: "f1"},
		{Date: "d2", Title: "b", GUID: "g2", File: "f2"},
		{Date: "d3", Title: "c", GUID: "g1", File: "f3"},
	}
	for _, e := range rows {
		if err := r.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	fresh, _ := Load(path)
	got := fresh.EntriesWithGUID("g1")
	if len(got) != 2 || got[0].File != "f1" || got[1].File != "f3" {
		t.Errorf("EntriesWithGUID(g1) = %v", got)
	}
	if got := fresh.EntriesWithGUID("nope"); len(got) != 0 {
		t.Errorf("EntriesWithGUID(nope) = %v", got)
	}
}

func TestPipeInTitleSurvivesRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, _ := Load(path)
	if err := r.EnsureHeader("S", "u"); err != nil {
		t.Fatal(err)
	}
	e := Entry{Date: "2026-08-29", Title: "A | B", GUID: "g", File: "f.mp3"}
	if err := r.Append(e); err != nil {
		t.Fatal(err)
	}
	fresh, _ := Load(path)
	got := fresh.Entries()
	if len(got) != 1 || got[0].Title != "A | B" {
		t.Errorf("roundtrip = %v, want title %q", got, "A | B")
	}
}

func TestAppendToFileWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	if err := os.WriteFile(path, []byte("no newline at end"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(path)
	if err := r.Append(Entry{Date: "d", Title: "t", GUID: "g", File: "f"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "no newline at end\n| d | t | g | f |\n"
	if string(data) != want {
		t.Errorf("content = %q, want %q", data, want)
	}
}

func TestHasEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, _ := Load(path)
	if err := r.EnsureHeader("S", "u"); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Entry{Date: "d", Title: "t", GUID: "g", File: "f.mp3"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := Load(path)
	if !fresh.HasEntry("g", "f.mp3") {
		t.Error("HasEntry(g, f.mp3) = false, want true")
	}
	if fresh.HasEntry("g", "other.mp3") || fresh.HasEntry("x", "f.mp3") {
		t.Error("HasEntry false positives")
	}
}

func TestParseIgnoresNonTableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	content := "# Show\n\nFeed: u\n\nSome prose with | pipes | in it.\n\n" +
		"| Date | Title | GUID | File |\n|------|-------|------|------|\n" +
		"| 2026-08-29 | Ep | abc | f.mp3 |\n| short row |\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Entries()
	if len(got) != 1 || got[0].GUID != "abc" {
		t.Errorf("Entries = %v, want the single table row", got)
	}
	if !strings.Contains(content, "Some prose") {
		t.Fatal("test fixture corrupted")
	}
}

func TestEnsureHeaderSanitizesControlChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	r, _ := Load(path)
	hostile := "Evil\nFeed: https://evil.example/feed"
	if err := r.EnsureHeader(hostile, "https://good.example/feed"); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var feedLines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Feed: ") {
			feedLines = append(feedLines, line)
		}
	}
	if len(feedLines) != 1 || feedLines[0] != "Feed: https://good.example/feed" {
		t.Errorf("header has %v Feed: lines, want exactly the real one (no injection): %q", feedLines, data)
	}
	url, ok := HeaderFeedURL(path)
	if !ok || url != "https://good.example/feed" {
		t.Errorf("HeaderFeedURL = %q, %v; want the real feed URL", url, ok)
	}
}

func TestHeaderFeedURLRejectsControlChars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	content := "# T\n\nFeed: https://x.example/feed\x01\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if url, ok := HeaderFeedURL(path); ok {
		t.Errorf("HeaderFeedURL accepted a Feed value with a control character: %q", url)
	}
}

func TestEnsureHeaderNeverTruncatesConcurrentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "podcast.md")
	// A file that already exists (created by a concurrent process in the
	// stat-then-create window) must survive EnsureHeader untouched.
	existing := "# concurrent owner\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(path)
	if err := r.EnsureHeader("Late Header", "https://late.example"); err != nil {
		t.Fatalf("EnsureHeader: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != existing {
		t.Errorf("existing registry file was modified: %q", data)
	}
}
