// Package settings manages the app-level settings.json in the data
// directory. It is plain JSON, human-editable, and separate from the
// archive: settings never hold episode data.
package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MinInterval is the smallest poll interval the app accepts. It exists so a
// misconfiguration cannot hot-loop the poller.
const MinInterval = time.Second

// Store is a settings.json file at Path.
type Store struct {
	Path string
	Log  *slog.Logger
}

type data struct {
	PollInterval string `json:"poll_interval,omitempty"`
}

// Get returns the poll interval override stored in settings.json, or ""
// when the file is missing or records no override. A file that cannot be
// read or parsed yields an error.
func (s *Store) Get() (string, error) {
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("settings: read %s: %w", s.Path, err)
	}
	var d data
	if err := json.Unmarshal(b, &d); err != nil {
		return "", fmt.Errorf("settings: parse %s: %w", s.Path, err)
	}
	return strings.TrimSpace(d.PollInterval), nil
}

// SetPollInterval stores v (a raw duration string such as "1h30m") as the
// poll interval override. The value must already be validated with
// ParseInterval.
func (s *Store) SetPollInterval(v string) error {
	d := data{PollInterval: v}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: marshal: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), filepath.Base(s.Path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("settings: create temp: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}
	if _, err := tmp.Write(b); err != nil {
		return fail(fmt.Errorf("settings: write: %w", err))
	}
	if err := tmp.Sync(); err != nil {
		return fail(fmt.Errorf("settings: sync: %w", err))
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("settings: close: %w", err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("settings: rename: %w", err)
	}
	s.Log.Info("poll interval override saved", "interval", v, "path", s.Path)
	return nil
}

// ParseInterval validates a poll interval string. Surrounding whitespace is
// tolerated; the duration must be at least MinInterval.
func ParseInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid poll interval %q: %w", v, err)
	}
	if d < MinInterval {
		return 0, fmt.Errorf("poll interval must be at least %s, got %q", MinInterval, v)
	}
	return d, nil
}
