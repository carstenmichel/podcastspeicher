package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"podcastspeicher/internal/settings"
	"podcastspeicher/internal/status"
	"podcastspeicher/internal/subs"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return NewServer(
		&subs.Store{Path: filepath.Join(dir, "shows.txt"), Log: discardLogger()},
		&settings.Store{Path: filepath.Join(dir, "settings.json"), Log: discardLogger()},
		status.NewStore(),
		"6h",
		discardLogger(),
	)
}

func do(t *testing.T, s *Server, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response %q is not valid JSON: %v", rec.Body.String(), err)
	}
	return m
}

func TestIndex(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "podcastspeicher") {
		t.Error("index does not look like the config page")
	}
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}

func TestUnknownPathAndMethod(t *testing.T) {
	s := newTestServer(t)
	if rec := do(t, s, http.MethodGet, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

func TestShowsAPI(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/api/shows", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/shows = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != `{"shows":[]}`+"\n" {
		t.Errorf("GET /api/shows = %s, want {\"shows\":[]}", body)
	}

	rec = do(t, s, http.MethodPost, "/api/shows", `{"url":"https://a.example/feed"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/shows = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}

	rec = do(t, s, http.MethodGet, "/api/shows", "")
	m := decodeBody(t, rec)
	shows, _ := m["shows"].([]any)
	if len(shows) != 1 || shows[0] != "https://a.example/feed" {
		t.Errorf("shows after add = %v, want the added URL", m["shows"])
	}

	rec = do(t, s, http.MethodPost, "/api/shows", `{"url":" https://a.example/feed "}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate add = %d, want 409", rec.Code)
	}

	for _, bad := range []string{
		`{"url":"not a url"}`,
		`{"url":"ftp://example.com/feed"}`,
		`{"url":"http://"}`,
		`{"url":"/relative/feed"}`,
		`{}`,
		`nope`,
	} {
		rec = do(t, s, http.MethodPost, "/api/shows", bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/shows %s = %d, want 400", bad, rec.Code)
		}
	}

	rec = do(t, s, http.MethodDelete, "/api/shows?url=https://a.example/feed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/shows = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet, "/api/shows", "")
	if body := rec.Body.String(); body != `{"shows":[]}`+"\n" {
		t.Errorf("shows after remove = %s, want empty", body)
	}

	rec = do(t, s, http.MethodDelete, "/api/shows?url=https://a.example/feed", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown = %d, want 404", rec.Code)
	}
	rec = do(t, s, http.MethodDelete, "/api/shows", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("DELETE without url = %d, want 400", rec.Code)
	}
}

func TestSettingsAPI(t *testing.T) {
	s := newTestServer(t)

	rec := do(t, s, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["poll_interval"] != "6h" {
		t.Errorf("poll_interval = %v, want the default fallback 6h", m["poll_interval"])
	}

	for _, bad := range []string{
		`{"poll_interval":"0s"}`,
		`{"poll_interval":"-1h"}`,
		`{"poll_interval":"garbage"}`,
		`{"poll_interval":""}`,
		`{}`,
		`nope`,
	} {
		rec = do(t, s, http.MethodPut, "/api/settings", bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT /api/settings %s = %d, want 400", bad, rec.Code)
		}
	}

	rec = do(t, s, http.MethodPut, "/api/settings", `{"poll_interval":"1h30m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	rec = do(t, s, http.MethodGet, "/api/settings", "")
	m = decodeBody(t, rec)
	if m["poll_interval"] != "1h30m" {
		t.Errorf("poll_interval after PUT = %v, want 1h30m", m["poll_interval"])
	}

	data, err := os.ReadFile(s.Settings.Path)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if !strings.Contains(string(data), `"poll_interval": "1h30m"`) {
		t.Errorf("settings.json = %s, want the stored override", data)
	}
}

func TestStatusAPI(t *testing.T) {
	s := newTestServer(t)

	// Empty store: endpoint returns an empty array, not null.
	rec := do(t, s, http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	shows, _ := m["shows"].([]any)
	if len(shows) != 0 {
		t.Errorf("shows before any poll = %v, want []", shows)
	}

	// Record one show and verify it appears.
	s.Status.Record("https://a.example/feed", "/data/A Example", 3, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))

	rec = do(t, s, http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status after record = %d, want 200", rec.Code)
	}
	m = decodeBody(t, rec)
	shows, _ = m["shows"].([]any)
	if len(shows) != 1 {
		t.Fatalf("shows after record = %v, want 1 entry", shows)
	}
	entry, _ := shows[0].(map[string]any)
	if entry["feed"] != "https://a.example/feed" {
		t.Errorf("feed = %v, want https://a.example/feed", entry["feed"])
	}
	if entry["episode_count"] != float64(3) {
		t.Errorf("episode_count = %v, want 3", entry["episode_count"])
	}
	if entry["last_fetched"] == nil || entry["last_fetched"] == "" {
		t.Errorf("last_fetched is missing or empty: %v", entry["last_fetched"])
	}
}
