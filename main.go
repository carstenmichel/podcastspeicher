// podcastspeicher mirrors subscribed podcast feeds to local disk and never
// deletes anything, so the archive outlives feeds and app.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"podcastspeicher/internal/mirror"
	"podcastspeicher/internal/settings"
	"podcastspeicher/internal/subs"
	"podcastspeicher/internal/web"
)

const (
	showsFileName      = "shows.txt"
	settingsFileName   = "settings.json"
	defaultDataDir     = "./data"
	defaultInterval    = 6 * time.Hour
	defaultHTTPAddr    = ":8080"
	defaultIntervalStr = "6h"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) > 1 {
		if os.Args[1] == "--health" {
			os.Exit(healthCheck(logger))
		}
		logger.Error("unknown argument", "arg", os.Args[1], "usage", "podcastspeicher [--health]")
		os.Exit(2)
	}
	if err := run(ctx, logger); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// healthCheck performs one GET on the config server's /healthz endpoint and
// returns a process exit code: 0 when the server answers, 1 otherwise. It
// exists so the shell-less distroless image can run a Docker HEALTHCHECK.
func healthCheck(logger *slog.Logger) int {
	addr := envOr("HTTP_ADDR", defaultHTTPAddr)
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		logger.Warn("health check failed", "error", err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		logger.Warn("health check failed", "status", resp.StatusCode)
		return 1
	}
	return 0
}

func run(ctx context.Context, logger *slog.Logger) error {
	dataDir := envOr("DATA_DIR", defaultDataDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	subStore := &subs.Store{Path: filepath.Join(dataDir, showsFileName), Log: logger}
	setStore := &settings.Store{Path: filepath.Join(dataDir, settingsFileName), Log: logger}
	envInterval := os.Getenv("POLL_INTERVAL")

	interval, err := effectiveInterval(setStore, envInterval, logger)
	if err != nil {
		return err
	}

	m := mirror.New(dataDir, logger)

	shows, err := subStore.List()
	if err != nil {
		return err
	}
	if len(shows) == 0 {
		logger.Warn("no shows configured", "hint", "add a show on the config page or one RSS feed URL per line to "+subStore.Path)
	}

	srv := &http.Server{
		Addr:    envOr("HTTP_ADDR", defaultHTTPAddr),
		Handler: web.NewServer(subStore, setStore, envIntervalString(envInterval), logger).Handler(),
	}
	srvDone := make(chan error, 1)
	go func() {
		logger.Info("config page listening", "addr", srv.Addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		srvDone <- err
	}()

	// Initial poll at startup, then one per interval. The interval is
	// re-resolved after every poll so a config-page change applies from the
	// next cycle without a restart.
	pollOnce(ctx, m, subStore, logger)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logger.Info("poller running", "data_dir", dataDir, "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			srv.Shutdown(shutdownCtx)
			cancel()
			if err := <-srvDone; err != nil {
				return err
			}
			return nil
		case err := <-srvDone:
			// A bind failure (e.g. port in use) is fatal: the config page
			// is a required capability, so failing loudly beats continuing
			// without it.
			return err
		case <-ticker.C:
			pollOnce(ctx, m, subStore, logger)
			if cur, cerr := effectiveInterval(setStore, envInterval, logger); cerr == nil && cur != interval {
				interval = cur
				ticker.Reset(interval)
				logger.Info("poll interval changed", "interval", interval.String())
			}
		}
	}
}

func pollOnce(ctx context.Context, m *mirror.Mirror, subStore *subs.Store, logger *slog.Logger) {
	// Re-read shows.txt each cycle so edits (or config-page changes)
	// take effect on the next poll without a restart.
	shows, err := subStore.List()
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

// effectiveInterval resolves the poll interval: a valid override in
// settings.json wins over the POLL_INTERVAL env, which wins over the
// default. An invalid settings.json value falls back with a warning (the
// config page validates what it writes); an invalid env value is fatal.
func effectiveInterval(setStore *settings.Store, envInterval string, logger *slog.Logger) (time.Duration, error) {
	if v, err := setStore.Get(); err == nil && v != "" {
		if d, perr := settings.ParseInterval(v); perr == nil {
			return d, nil
		} else {
			logger.Warn("settings poll_interval invalid; using fallback", "value", v, "error", perr)
		}
	} else if err != nil {
		logger.Warn("settings unreadable; using fallback", "error", err)
	}
	if envInterval != "" {
		return settings.ParseInterval(envInterval)
	}
	return defaultInterval, nil
}

// envIntervalString reports the interval string to show on the config page
// when settings.json holds no override: the POLL_INTERVAL env value or "6h".
func envIntervalString(envInterval string) string {
	if v := strings.TrimSpace(envInterval); v != "" {
		return v
	}
	return defaultIntervalStr
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
