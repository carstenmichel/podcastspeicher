// podcastspeicher mirrors subscribed podcast feeds to local disk and never
// deletes anything, so the archive outlives feeds and app.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"podcastspeicher/internal/mirror"
)

const showsFileName = "shows.txt"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := envOr("DATA_DIR", "./data")
	interval := 6 * time.Hour
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		d, err := parsePollInterval(v)
		if err != nil {
			return err
		}
		interval = d
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	showsFile := filepath.Join(dataDir, showsFileName)

	m := mirror.New(dataDir, logger)

	shows, err := loadShows(showsFile, logger)
	if err != nil {
		return err
	}
	if len(shows) == 0 {
		logger.Warn("no shows configured", "hint", "add one RSS feed URL per line to "+showsFile)
	}

	// Initial poll at startup, then one per interval.
	pollOnce(ctx, m, showsFile, logger)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logger.Info("poller running", "data_dir", dataDir, "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return nil
		case <-ticker.C:
			pollOnce(ctx, m, showsFile, logger)
		}
	}
}

func pollOnce(ctx context.Context, m *mirror.Mirror, showsFile string, logger *slog.Logger) {
	// Re-read shows.txt each cycle so edits (or story 2's config page)
	// take effect on the next poll without a restart.
	shows, err := loadShows(showsFile, logger)
	if err != nil {
		logger.Error("load shows failed", "error", err)
		return
	}
	for _, feedURL := range shows {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		if err := m.PollShow(ctx, feedURL); err != nil {
			logger.Error("show skipped this cycle", "feed", feedURL, "error", err)
			continue
		}
		logger.Info("show polled", "feed", feedURL, "took", time.Since(start).Round(time.Millisecond))
	}
}

// parsePollInterval parses the POLL_INTERVAL value. Surrounding whitespace is
// tolerated; the duration must be at least one second so a misconfiguration
// cannot hot-loop the poller.
func parsePollInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid POLL_INTERVAL %q: %w", v, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("POLL_INTERVAL must be at least 1s, got %q", v)
	}
	return d, nil
}

// loadShows reads feed URLs from path, one per line, with "#" comments.
// A missing file is created empty so there is something to edit.
func loadShows(path string, logger *slog.Logger) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		f, cerr := os.Create(path)
		if cerr != nil {
			return nil, fmt.Errorf("create %s: %w", path, cerr)
		}
		if cerr := f.Close(); cerr != nil {
			return nil, fmt.Errorf("close %s: %w", path, cerr)
		}
		logger.Warn("shows.txt missing; created empty file", "path", path)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	seen := make(map[string]bool)
	var shows []string
	for _, line := range strings.Split(text, "\n") {
		line = stripComment(line)
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		shows = append(shows, line)
	}
	return shows, nil
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
