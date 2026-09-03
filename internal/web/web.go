// Package web serves the config page: one embedded HTML document plus a
// small JSON API for managing shows and the poll interval. The page is
// unauthenticated by design (v1 assumes a trusted/local network).
package web

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"podcastspeicher/internal/feed"
	"podcastspeicher/internal/settings"
	"podcastspeicher/internal/subs"
)

//go:embed index.html
var indexHTML string

// maxBodyBytes caps JSON request bodies; the API takes only short values.
const maxBodyBytes = 4 << 10

// Server is the config page and its JSON API.
type Server struct {
	Subs     *subs.Store
	Settings *settings.Store
	// DefaultInterval is the effective interval string when settings.json
	// holds no valid override: the POLL_INTERVAL env value or "6h".
	DefaultInterval string
	Log             *slog.Logger
}

// Handler returns the HTTP handler for the config page and API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/shows", s.handleListShows)
	mux.HandleFunc("POST /api/shows", s.handleAddShow)
	mux.HandleFunc("DELETE /api/shows", s.handleRemoveShow)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSetSettings)
	return mux
}

// NewServer returns a Server with the given stores. defaultInterval is the
// interval string to report when settings.json holds no valid override.
func NewServer(subsStore *subs.Store, setStore *settings.Store, defaultInterval string, log *slog.Logger) *Server {
	return &Server{
		Subs:            subsStore,
		Settings:        setStore,
		DefaultInterval: defaultInterval,
		Log:             log,
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// "GET /" is the mux subtree pattern; only the root serves the page.
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) handleListShows(w http.ResponseWriter, _ *http.Request) {
	shows, err := s.Subs.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if shows == nil {
		shows = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shows": shows})
}

func (s *Server) handleAddShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	u := strings.TrimSpace(req.URL)
	if !feed.IsHTTPURL(u) {
		writeError(w, http.StatusBadRequest, "feed URL must be an absolute http(s) URL")
		return
	}
	if err := s.Subs.Add(u); err != nil {
		if errors.Is(err, subs.ErrAlreadySubscribed) {
			writeError(w, http.StatusConflict, "already subscribed")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("show added via config page", "feed", u)
	writeJSON(w, http.StatusCreated, map[string]any{"url": u})
}

func (s *Server) handleRemoveShow(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("url")
	if u == "" {
		writeError(w, http.StatusBadRequest, "missing url query parameter")
		return
	}
	if err := s.Subs.Remove(u); err != nil {
		if errors.Is(err, subs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not subscribed")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("show removed via config page", "feed", u)
	writeJSON(w, http.StatusOK, map[string]any{"removed": u})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"poll_interval": s.effectiveInterval()})
}

func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PollInterval string `json:"poll_interval"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	v := strings.TrimSpace(req.PollInterval)
	if _, err := settings.ParseInterval(v); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Settings.SetPollInterval(v); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"poll_interval": v})
}

// effectiveInterval reports the interval the poller is currently using, as a
// string: the settings.json override when it holds a valid value, otherwise
// the startup fallback (POLL_INTERVAL env or "6h").
func (s *Server) effectiveInterval() string {
	if v, err := s.Settings.Get(); err == nil && v != "" {
		if _, perr := settings.ParseInterval(v); perr == nil {
			return v
		}
	}
	return s.DefaultInterval
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
