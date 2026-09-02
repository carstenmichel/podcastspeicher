// Package registry reads and appends to the per-show podcast.md registry,
// the human-legible Markdown ledger that drives mirror dedupe.
package registry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Entry is one row of the podcast.md episode table.
type Entry struct {
	Date  string
	Title string
	GUID  string
	File  string
}

// Registry is a per-show podcast.md registry.
type Registry struct {
	path    string
	entries []Entry
}

// Load reads the registry at path. A missing file yields an empty registry.
func Load(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	r.entries = parse(string(data))
	return r, nil
}

// EnsureHeader creates the registry file with a header block when it does
// not exist. An existing registry is never overwritten. Creation is atomic
// (O_EXCL), so a file created concurrently is never truncated.
func (r *Registry) EnsureHeader(showTitle, feedURL string) error {
	header := "# " + sanitizeHeaderTitle(showTitle) + "\n\n" +
		"Feed: " + feedURL + "\n\n" +
		"| Date | Title | GUID | File |\n" +
		"|------|-------|------|------|\n"
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("registry: create %s: %w", r.path, err)
	}
	if _, werr := f.WriteString(header); werr != nil {
		f.Close()
		os.Remove(r.path)
		return fmt.Errorf("registry: write %s: %w", r.path, werr)
	}
	if serr := f.Sync(); serr != nil {
		f.Close()
		os.Remove(r.path)
		return fmt.Errorf("registry: sync %s: %w", r.path, serr)
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(r.path)
		return fmt.Errorf("registry: close %s: %w", r.path, cerr)
	}
	return nil
}

// sanitizeHeaderTitle makes a channel title safe for the single-line
// "# <Show Title>" header: control characters become spaces so a hostile
// feed cannot inject fake header lines (e.g. a "Feed: " line) into the
// registry.
func sanitizeHeaderTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// Append adds one episode row to the registry file.
func (r *Registry) Append(e Entry) error {
	row := "| " + escapeCell(e.Date) + " | " +
		escapeCell(e.Title) + " | " +
		escapeCell(e.GUID) + " | " +
		escapeCell(e.File) + " |\n"
	f, err := os.OpenFile(r.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("registry: open %s: %w", r.path, err)
	}
	defer f.Close()
	if err := ensureTrailingNewline(f); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if _, err := f.WriteString(row); err != nil {
		return fmt.Errorf("registry: write %s: %w", r.path, err)
	}
	r.entries = append(r.entries, e)
	return f.Sync()
}

// HeaderFeedURL returns the feed URL recorded on the "Feed:" line of the
// podcast.md header at path, if the file exists and records one. A value
// containing control characters is rejected (not ok) so a tampered or
// malformed header can never bind a directory to a forged feed URL.
func HeaderFeedURL(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.HasPrefix(line, "Feed: ") {
			continue
		}
		value := strings.TrimSpace(line[len("Feed: "):])
		if hasControlChars(value) {
			return "", false
		}
		return value, true
	}
	return "", false
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Entries returns all parsed episode rows.
func (r *Registry) Entries() []Entry { return r.entries }

// EntriesWithGUID returns the rows recorded for guid.
func (r *Registry) EntriesWithGUID(guid string) []Entry {
	var out []Entry
	for _, e := range r.entries {
		if e.GUID == guid {
			out = append(out, e)
		}
	}
	return out
}

// HasEntry reports whether a row with exactly this GUID and file exists.
func (r *Registry) HasEntry(guid, file string) bool {
	for _, e := range r.entries {
		if e.GUID == guid && e.File == file {
			return true
		}
	}
	return false
}

func ensureTrailingNewline(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() == 0 {
		return nil
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return err
	}
	var b [1]byte
	if _, err := f.Read(b[:]); err != nil {
		return err
	}
	if b[0] != '\n' {
		_, err := f.WriteString("\n")
		return err
	}
	return nil
}

func escapeCell(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

func parse(data string) []Entry {
	var entries []Entry
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 2 || !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			continue
		}
		cells := splitRow(trimmed)
		if len(cells) != 4 {
			continue
		}
		if cells[0] == "Date" || isSeparator(cells[0]) {
			continue
		}
		entries = append(entries, Entry{
			Date:  cells[0],
			Title: cells[1],
			GUID:  cells[2],
			File:  cells[3],
		})
	}
	return entries
}

func splitRow(row string) []string {
	inner := strings.TrimSpace(row)
	inner = strings.TrimPrefix(inner, "|")
	inner = strings.TrimSuffix(inner, "|")
	inner = strings.TrimSpace(inner)
	var cells []string
	var sb strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case c == '\\' && i+1 < len(inner) && inner[i+1] == '|':
			sb.WriteByte('|')
			i++
		case c == '|':
			cells = append(cells, strings.TrimSpace(sb.String()))
			sb.Reset()
		default:
			sb.WriteByte(c)
		}
	}
	cells = append(cells, strings.TrimSpace(sb.String()))
	return cells
}

func isSeparator(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '-' && r != ':' && r != ' ' {
			return false
		}
	}
	return true
}
