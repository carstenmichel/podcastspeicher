// Package status tracks per-show poll results in memory so the config page
// can display last fetch time, episode count, and disk usage. The store is
// ephemeral: it is reset on restart and rebuilt as polls complete.
package status

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ShowStatus is the latest known state for one subscribed show.
type ShowStatus struct {
	// FeedURL is the subscribed feed URL.
	FeedURL string `json:"feed"`
	// ShowDir is the archive directory path; empty when the show has never
	// been polled successfully or has no directory yet.
	ShowDir string `json:"-"`
	// LastFetched is the time the most recent successful poll completed.
	// Zero when the show has not been polled in this process lifetime.
	LastFetched time.Time `json:"last_fetched,omitempty"`
	// EpisodeCount is the number of rows in podcast.md (registry entries).
	EpisodeCount int `json:"episode_count"`
	// DiskBytes is the total size of non-temp episode files in the show
	// directory. Zero when the directory does not exist.
	DiskBytes int64 `json:"disk_bytes"`
}

// Store holds per-show status records, keyed by feed URL.
type Store struct {
	mu      sync.RWMutex
	records map[string]*ShowStatus
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{records: make(map[string]*ShowStatus)}
}

// Record updates the status for feedURL after a successful poll. showDir is
// the archive directory path resolved by the mirror. DiskBytes and
// EpisodeCount are computed from disk at record time.
func (s *Store) Record(feedURL, showDir string, episodeCount int, now time.Time) {
	disk := diskBytes(showDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[feedURL] = &ShowStatus{
		FeedURL:      feedURL,
		ShowDir:      showDir,
		LastFetched:  now,
		EpisodeCount: episodeCount,
		DiskBytes:    disk,
	}
}

// All returns a snapshot of all recorded statuses in insertion order (feeds
// that have been polled at least once in this process lifetime).
func (s *Store) All() []ShowStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ShowStatus, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, *r)
	}
	return out
}

// diskBytes sums the sizes of non-temp files directly inside dir. It returns
// 0 when dir is missing or unreadable.
func diskBytes(dir string) int64 {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, en := range entries {
		if en.IsDir() || isTempName(en.Name()) {
			continue
		}
		info, err := en.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}

// isTempName reports whether a filename looks like a mirror temp file
// ("<name>.part-<digits>"). Mirrors the logic in mirror.isStaleTempName.
func isTempName(name string) bool {
	i := strings.LastIndex(name, ".part-")
	if i <= 0 {
		return false
	}
	suffix := name[i+len(".part-"):]
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// RegistryFile returns the path to podcast.md inside showDir.
func RegistryFile(showDir string) string {
	return filepath.Join(showDir, "podcast.md")
}
