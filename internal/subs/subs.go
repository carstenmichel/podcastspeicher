// Package subs manages the shows.txt subscription list: one RSS feed URL per
// line, with "#" comments. Add and Remove edit the file line-wise, so
// comments and any unrecognized lines survive app rewrites.
package subs

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// ErrAlreadySubscribed is returned by Add when the URL is already listed.
var ErrAlreadySubscribed = errors.New("subs: already subscribed")

// ErrNotFound is returned by Remove when the URL is not listed.
var ErrNotFound = errors.New("subs: not subscribed")

// Store is a shows.txt subscription list at Path.
type Store struct {
	Path string
	Log  *slog.Logger

	mu sync.Mutex // serializes Add/Remove; List is read-only
}

// List returns the subscribed feed URLs in file order. A missing file is
// created empty so there is something to edit.
func (s *Store) List() ([]string, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		f, cerr := os.Create(s.Path)
		if cerr != nil {
			return nil, fmt.Errorf("subs: create %s: %w", s.Path, cerr)
		}
		if cerr := f.Close(); cerr != nil {
			return nil, fmt.Errorf("subs: close %s: %w", s.Path, cerr)
		}
		s.Log.Warn("shows.txt missing; created empty file", "path", s.Path)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subs: read %s: %w", s.Path, err)
	}
	return Parse(string(data)), nil
}

// Parse extracts the feed URLs from shows.txt content: one URL per line,
// trailing "#" comments stripped, blanks skipped, duplicates removed (first
// occurrence wins).
func Parse(text string) []string {
	text = strings.TrimPrefix(text, "\uFEFF")
	seen := make(map[string]bool)
	var shows []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(stripComment(line))
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		shows = append(shows, line)
	}
	return shows
}

// Add subscribes to feedURL. It appends one line and preserves all existing
// lines (comments included). Adding an already-listed URL (same stripped
// form) is ErrAlreadySubscribed.
func (s *Store) Add(feedURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkAndWrite(func(lines []string) ([]string, error) {
		for _, l := range lines {
			if strings.TrimSpace(stripComment(l)) == feedURL {
				return nil, ErrAlreadySubscribed
			}
		}
		return append(lines, feedURL), nil
	}); err != nil {
		return err
	}
	s.Log.Info("show added", "feed", feedURL)
	return nil
}

// Remove unsubscribes feedURL by deleting the first line whose stripped form
// matches. All other lines are preserved. Removing an unlisted URL is
// ErrNotFound. Files of the removed show are never touched.
func (s *Store) Remove(feedURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	if err := s.checkAndWrite(func(lines []string) ([]string, error) {
		for i, l := range lines {
			if strings.TrimSpace(stripComment(l)) == feedURL {
				removed = true
				return append(lines[:i], lines[i+1:]...), nil
			}
		}
		return nil, ErrNotFound
	}); err != nil {
		return err
	}
	if removed {
		s.Log.Info("show removed", "feed", feedURL)
	}
	return nil
}

// checkAndWrite reads the raw lines, applies edit, and atomically writes the
// result (temp file + rename) so a crash never leaves a truncated shows.txt.
func (s *Store) checkAndWrite(edit func(lines []string) ([]string, error)) error {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		// Create the file first so a concurrent Add never sees "missing"
		// and clobbers a list another Add just wrote.
		if cerr := os.WriteFile(s.Path, nil, 0o644); cerr != nil {
			return fmt.Errorf("subs: create %s: %w", s.Path, cerr)
		}
		data = nil
	} else if err != nil {
		return fmt.Errorf("subs: read %s: %w", s.Path, err)
	}
	lines := splitLines(string(data))
	next, err := edit(lines)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.Path, strings.Join(next, "\n")+"\n"); err != nil {
		return fmt.Errorf("subs: write %s: %w", s.Path, err)
	}
	return nil
}

// splitLines keeps every line (including blanks and comments) for
// line-preserving rewrites. A trailing newline does not produce a phantom
// empty line.
func splitLines(data string) []string {
	data = strings.TrimSuffix(data, "\n")
	if data == "" {
		return nil
	}
	return strings.Split(data, "\n")
}

// writeAtomic writes content to path via a temp file in the same directory
// plus rename, so readers never observe a partial file.
func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(tmpName)
		return e
	}
	if _, err := tmp.WriteString(content); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// stripComment removes a trailing comment: everything from a '#' that is at
// the start of the line or preceded by whitespace. A '#' inside a URL (e.g.
// a fragment) is preserved.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || unicode.IsSpace(rune(line[i-1])) {
			return line[:i]
		}
	}
	return line
}
