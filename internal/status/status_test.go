package status

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreEmptyAll(t *testing.T) {
	s := NewStore()
	if got := s.All(); len(got) != 0 {
		t.Errorf("All on empty store = %v, want []", got)
	}
}

func TestStoreRecord(t *testing.T) {
	s := NewStore()
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	s.Record("https://a.example/feed", "/tmp/show-a", 5, now)

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All after one record = %d, want 1", len(all))
	}
	got := all[0]
	if got.FeedURL != "https://a.example/feed" {
		t.Errorf("FeedURL = %q, want %q", got.FeedURL, "https://a.example/feed")
	}
	if got.EpisodeCount != 5 {
		t.Errorf("EpisodeCount = %d, want 5", got.EpisodeCount)
	}
	if !got.LastFetched.Equal(now) {
		t.Errorf("LastFetched = %v, want %v", got.LastFetched, now)
	}
}

func TestStoreRecordOverwrite(t *testing.T) {
	s := NewStore()
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(6 * time.Hour)
	s.Record("https://a.example/feed", "/tmp/show-a", 3, t1)
	s.Record("https://a.example/feed", "/tmp/show-a", 7, t2)

	all := s.All()
	if len(all) != 1 {
		t.Fatalf("All after two records for same feed = %d, want 1", len(all))
	}
	if all[0].EpisodeCount != 7 {
		t.Errorf("EpisodeCount after overwrite = %d, want 7", all[0].EpisodeCount)
	}
	if !all[0].LastFetched.Equal(t2) {
		t.Errorf("LastFetched after overwrite = %v, want %v", all[0].LastFetched, t2)
	}
}

func TestDiskBytes(t *testing.T) {
	dir := t.TempDir()

	// Empty dir.
	if got := diskBytes(dir); got != 0 {
		t.Errorf("diskBytes empty dir = %d, want 0", got)
	}

	// Write two files.
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.mp3"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := diskBytes(dir); got != 300 {
		t.Errorf("diskBytes with two files = %d, want 300", got)
	}

	// Temp file is excluded.
	if err := os.WriteFile(filepath.Join(dir, "b.mp3.part-1234"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := diskBytes(dir); got != 300 {
		t.Errorf("diskBytes with temp file = %d, want 300 (temp excluded)", got)
	}
}

func TestDiskBytesMissingDir(t *testing.T) {
	if got := diskBytes("/no/such/dir"); got != 0 {
		t.Errorf("diskBytes missing dir = %d, want 0", got)
	}
	if got := diskBytes(""); got != 0 {
		t.Errorf("diskBytes empty string = %d, want 0", got)
	}
}
