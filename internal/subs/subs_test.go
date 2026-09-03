package subs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParse(t *testing.T) {
	content := "\uFEFFhttps://a.example/feed # primary\n" +
		"\n" +
		"\t\n" +
		"https://b.example/feed\n" +
		"https://a.example/feed\n" +
		"# full line comment\n" +
		"https://c.example/feed # note\n"
	got := Parse(content)
	want := []string{
		"https://a.example/feed",
		"https://b.example/feed",
		"https://c.example/feed",
	}
	if len(got) != len(want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Parse[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePreservesURLFragment(t *testing.T) {
	got := Parse("https://x.example/feed#section-1\nhttps://y.example/feed # real comment\n")
	want := []string{
		"https://x.example/feed#section-1",
		"https://y.example/feed",
	}
	if len(got) != len(want) {
		t.Fatalf("Parse = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Parse[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestListCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.txt")
	s := &Store{Path: path, Log: discardLogger()}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want none", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("shows.txt was not created: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("created shows.txt is not empty: %d bytes", fi.Size())
	}
}

func TestAdd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shows.txt")
	if err := os.WriteFile(path, []byte("# my shows\nhttps://a.example/feed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path, Log: discardLogger()}

	if err := s.Add("https://b.example/feed"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://a.example/feed", "https://b.example/feed"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("List after Add = %v, want %v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# my shows\nhttps://a.example/feed\nhttps://b.example/feed\n" {
		t.Errorf("file lost its comment line: %q", data)
	}

	// Duplicates are rejected in stripped form, including a commented line.
	if err := s.Add("https://a.example/feed"); err != ErrAlreadySubscribed {
		t.Errorf("Add duplicate = %v, want ErrAlreadySubscribed", err)
	}
	if err := s.Add("https://a.example/feed#frag"); err != nil {
		t.Errorf("Add of a URL only differing by fragment should be allowed (different URL): %v", err)
	}

	// Adding to a missing file works and creates it.
	s2 := &Store{Path: filepath.Join(dir, "fresh.txt"), Log: discardLogger()}
	if err := s2.Add("https://c.example/feed"); err != nil {
		t.Fatalf("Add to missing file: %v", err)
	}
	got, err = s2.List()
	if err != nil || len(got) != 1 || got[0] != "https://c.example/feed" {
		t.Errorf("fresh List = %v, %v; want one show", got, err)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shows.txt")
	content := "# keep me\nhttps://a.example/feed\nhttps://b.example/feed # trailing\nhttps://c.example/feed\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path, Log: discardLogger()}

	if err := s.Remove("https://b.example/feed"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep me\nhttps://a.example/feed\nhttps://c.example/feed\n"
	if string(data) != want {
		t.Errorf("file after Remove = %q, want %q", data, want)
	}

	if err := s.Remove("https://b.example/feed"); err != ErrNotFound {
		t.Errorf("Remove again = %v, want ErrNotFound", err)
	}
	if err := s.Remove("https://unknown.example/feed"); err != ErrNotFound {
		t.Errorf("Remove unknown = %v, want ErrNotFound", err)
	}

	// Removing from a missing file is ErrNotFound, not a crash.
	s2 := &Store{Path: filepath.Join(dir, "missing.txt"), Log: discardLogger()}
	if err := s2.Remove("https://x.example/feed"); err != ErrNotFound {
		t.Errorf("Remove from missing file = %v, want ErrNotFound", err)
	}
}

func TestAddRemoveConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shows.txt")
	s := &Store{Path: path, Log: discardLogger()}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Add("https://example.com/feed" + string(rune('a'+i))); err != nil {
				t.Errorf("Add %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Errorf("concurrent Adds left %d shows, want 20", len(got))
	}
}
