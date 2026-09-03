package settings

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGetMissingFile(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "settings.json"), Log: discardLogger()}
	v, err := s.Get()
	if err != nil {
		t.Fatalf("Get on missing file: %v", err)
	}
	if v != "" {
		t.Errorf("Get = %q, want empty", v)
	}
}

func TestSetGetRoundTrip(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "settings.json"), Log: discardLogger()}
	if err := s.SetPollInterval("1h30m"); err != nil {
		t.Fatalf("SetPollInterval: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "1h30m" {
		t.Errorf("Get = %q, want %q", got, "1h30m")
	}

	// The file on disk is plain, human-readable JSON.
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v (%q)", err, data)
	}
	if m["poll_interval"] != "1h30m" {
		t.Errorf("file poll_interval = %q, want %q", m["poll_interval"], "1h30m")
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("settings.json should end with a newline: %q", data)
	}
}

func TestGetCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path, Log: discardLogger()}
	if _, err := s.Get(); err == nil {
		t.Fatal("Get on corrupt file should fail")
	}
}

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"not-a-duration", 0, true},
		{"0s", 0, true},
		{"-1h", 0, true},
		{"500ms", 0, true},
		{" 5s ", 5 * time.Second, false},
		{"6h", 6 * time.Hour, false},
	}
	for _, c := range cases {
		got, err := ParseInterval(c.in)
		if (err != nil) != c.err {
			t.Errorf("ParseInterval(%q) error = %v, want error %v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("ParseInterval(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
