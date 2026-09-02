// Package mirror downloads new podcast episodes into the local archive.
//
// The archive never loses a file: a download is skipped when the target file
// already exists, and a registry row whose file is missing triggers a
// re-download (gap fill, prefer-duplicates-over-gaps).
package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"podcastspeicher/internal/feed"
	"podcastspeicher/internal/registry"
)

const (
	registryName    = "podcast.md"
	downloadTimeout = 15 * time.Minute
	// defaultMaxDownload caps a single enclosure download at 2 GiB.
	defaultMaxDownload = 2 << 30
)

const (
	maxNameRunes = 180
	maxNameBytes = 240
	// maxFileNameBytes is the byte budget for the total on-disk episode file
	// name "<date> - <title><ext>"; it stays well under the 255-byte
	// filesystem limit.
	maxFileNameBytes = 240
)

// Mirror mirrors shows into DataDir.
type Mirror struct {
	DataDir          string
	FeedClient       *http.Client
	DownloadClient   *http.Client
	MaxDownloadBytes int64
	Now              func() time.Time
	Log              *slog.Logger
}

// New returns a Mirror rooted at dataDir.
func New(dataDir string, log *slog.Logger) *Mirror {
	return &Mirror{
		DataDir:          dataDir,
		FeedClient:       &http.Client{Timeout: feed.FeedTimeout},
		DownloadClient:   &http.Client{Timeout: downloadTimeout},
		MaxDownloadBytes: defaultMaxDownload,
		Now:              time.Now,
		Log:              log,
	}
}

// PollShow fetches one show's feed and downloads every episode that is not
// already mirrored. Episodes are processed sequentially; a cancelled context
// aborts the poll promptly. A fetch or parse error is returned so the caller
// can skip the show until the next cycle.
func (m *Mirror) PollShow(ctx context.Context, feedURL string) error {
	show, err := feed.Fetch(ctx, m.FeedClient, feedURL)
	if err != nil {
		return err
	}
	dir, err := m.resolveShowDir(feedURL, show.Title)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mirror: mkdir %s: %w", dir, err)
	}
	m.removeStaleTempFiles(dir)
	reg, err := registry.Load(filepath.Join(dir, registryName))
	if err != nil {
		return err
	}
	headerTitle := strings.TrimSpace(show.Title)
	if headerTitle == "" {
		headerTitle = filepath.Base(dir)
	}
	if err := reg.EnsureHeader(headerTitle, feedURL); err != nil {
		return err
	}
	for _, ep := range show.Episodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mirrorEpisode(ctx, dir, reg, ep)
	}
	return nil
}

// resolveShowDir determines the archive directory for a feed. Existing show
// directories are scanned for a podcast.md header recording this feed URL
// and the match is reused (stable across channel renames, no re-download).
// When no directory records the feed URL, one is created from the sanitized
// channel title; if that name is already recorded by a different feed URL, a
// deterministic short hash of the feed URL is appended so two same-titled
// shows never share a directory.
func (m *Mirror) resolveShowDir(feedURL, showTitle string) (string, error) {
	name := ShowName(showTitle, feedURL)
	entries, err := os.ReadDir(m.DataDir)
	if err != nil {
		return "", fmt.Errorf("mirror: read data dir: %w", err)
	}
	nameTaken := false
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		regURL, ok := registry.HeaderFeedURL(filepath.Join(m.DataDir, en.Name(), registryName))
		if ok && regURL == feedURL {
			return filepath.Join(m.DataDir, en.Name()), nil
		}
		// The directory does not verify as ours: it records a different feed
		// URL, or it has no readable registry at all. Never reuse a directory
		// whose recorded feed URL cannot be verified.
		if en.Name() == name {
			nameTaken = true
		}
	}
	if nameTaken {
		name += "-" + shortHash(feedURL)
	}
	return filepath.Join(m.DataDir, name), nil
}

func (m *Mirror) mirrorEpisode(ctx context.Context, dir string, reg *registry.Registry, ep feed.Episode) {
	targetName, date := m.targetNameFor(reg, ep)
	target := filepath.Join(dir, targetName)
	if fileExists(target) {
		m.Log.Debug("skip, file exists", "file", targetName)
		// Ledger self-heal: a successful download whose registry append once
		// failed leaves the file present but unrecorded; without this the
		// row is lost forever (the exists guard skips before Append is
		// retried). Episodes without a stable id are never self-healed:
		// their ownership is ambiguous.
		if ep.GUID != "" && !reg.HasEntry(ep.GUID, targetName) {
			if err := reg.Append(registry.Entry{
				Date:  date,
				Title: ep.Title,
				GUID:  ep.GUID,
				File:  targetName,
			}); err != nil {
				m.Log.Error("ledger self-heal append failed", "file", targetName, "error", err)
			} else {
				m.Log.Info("ledger self-heal: appended missing registry row", "guid", ep.GUID, "file", targetName)
			}
		}
		return
	}
	if ep.GUID != "" {
		for _, e := range reg.EntriesWithGUID(ep.GUID) {
			if fileExists(filepath.Join(dir, e.File)) {
				m.Log.Debug("skip, already mirrored", "guid", ep.GUID, "file", e.File)
				return
			}
		}
	}
	if err := m.download(ctx, ep.EnclosureURL, target); err != nil {
		m.Log.Error("download failed", "title", ep.Title, "url", ep.EnclosureURL, "error", err)
		return
	}
	if !reg.HasEntry(ep.GUID, targetName) {
		if err := reg.Append(registry.Entry{
			Date:  date,
			Title: ep.Title,
			GUID:  ep.GUID,
			File:  targetName,
		}); err != nil {
			m.Log.Error("registry append failed", "file", targetName, "error", err)
		}
	}
	m.Log.Info("downloaded", "title", ep.Title, "file", targetName)
}

// targetNameFor computes the archive file name for an episode. When the
// computed name is already recorded in the registry by a different GUID, a
// deterministic short hash suffix (of the GUID, or of the enclosure URL when
// the episode has no stable id) separates the two files so no episode is
// silently dropped. The same episode always computes the same name.
func (m *Mirror) targetNameFor(reg *registry.Registry, ep feed.Episode) (string, string) {
	name, date := EpisodeFileName(ep, m.Now())
	if nameTakenByOther(reg, ep.GUID, name) {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		key := ep.GUID
		if key == "" {
			key = ep.EnclosureURL
		}
		name = base + "-" + shortHash(key) + ext
	}
	return name, date
}

func nameTakenByOther(reg *registry.Registry, guid, file string) bool {
	for _, e := range reg.Entries() {
		if e.File == file && e.GUID != guid {
			return true
		}
	}
	return false
}

// shortHash returns the first 8 hex chars of the sha256 of s.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// removeStaleTempFiles deletes temp files left by a previous interrupted run
// so they do not accumulate. Only temp files are touched; archive files are
// never deleted.
func (m *Mirror) removeStaleTempFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, en := range entries {
		if en.IsDir() || !isStaleTempName(en.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, en.Name())); err != nil {
			m.Log.Warn("remove stale temp file failed", "path", en.Name(), "error", err)
		}
	}
}

// isStaleTempName reports whether name is a temp file created by download
// ("<archive name>.part-<digits>"). The non-empty prefix requirement keeps
// hidden user files named exactly ".part-<digits>" out of scope, and archive
// names always end in a media extension, so this never matches an archived
// episode.
func isStaleTempName(name string) bool {
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

// download streams rawURL into a temp file next to target and moves it into
// place. The transfer is bounded by MaxDownloadBytes; a cap breach, a
// Content-Length mismatch, a zero-byte body, or any transfer error aborts
// with the temp file removed (no registry row, retried on the next poll).
// target is never overwritten.
func (m *Mirror) download(ctx context.Context, rawURL, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("mirror: new request: %w", err)
	}
	req.Header.Set("User-Agent", feed.UserAgent)
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".part-*")
	if err != nil {
		return fmt.Errorf("mirror: create temp: %w", err)
	}
	tmpName := tmp.Name()
	removeTemp := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	resp, err := m.DownloadClient.Do(req)
	if err != nil {
		removeTemp()
		return fmt.Errorf("mirror: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		removeTemp()
		return fmt.Errorf("mirror: HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, m.MaxDownloadBytes+1))
	if err != nil {
		removeTemp()
		if ctx.Err() != nil {
			return fmt.Errorf("mirror: aborted: %w", ctx.Err())
		}
		return fmt.Errorf("mirror: write: %w", err)
	}
	if n > m.MaxDownloadBytes {
		removeTemp()
		return fmt.Errorf("mirror: download exceeds %d byte cap", m.MaxDownloadBytes)
	}
	if n == 0 {
		removeTemp()
		return fmt.Errorf("mirror: empty response body")
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		removeTemp()
		return fmt.Errorf("mirror: content length mismatch (wrote %d, declared %d)", n, resp.ContentLength)
	}
	if err := tmp.Sync(); err != nil {
		removeTemp()
		return fmt.Errorf("mirror: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("mirror: close temp: %w", err)
	}
	if fileExists(target) {
		os.Remove(tmpName)
		return fmt.Errorf("mirror: target appeared during download")
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("mirror: rename: %w", err)
	}
	m.syncDir(filepath.Dir(target))
	return nil
}

// syncDir best-effort syncs a directory after a rename so the new entry
// survives a crash. Failures are logged, never fatal.
func (m *Mirror) syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		m.Log.Warn("directory sync failed", "dir", dir, "error", err)
		return
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		m.Log.Warn("directory sync failed", "dir", dir, "error", err)
	}
}

// ShowName derives the per-show folder (and registry header fallback) name
// from the feed's channel title, falling back to the feed URL's host.
func ShowName(title, feedURL string) string {
	if n := sanitizeName(strings.TrimSpace(title)); n != "" {
		return n
	}
	if u, err := url.Parse(feedURL); err == nil && u.Hostname() != "" {
		return sanitizeName(u.Hostname())
	}
	return "untitled-show"
}

// EpisodeFileName builds the archive file name
// "<YYYY-MM-DD> - <Title>.<ext>" and returns it together with the date part
// recorded in the registry row. The publish date is used; when it is missing
// or unparseable the download date is the fallback. The title is truncated
// so the total on-disk name stays within maxFileNameBytes.
func EpisodeFileName(ep feed.Episode, now time.Time) (string, string) {
	t, ok := parsePubDate(ep.PubDate)
	if !ok {
		t = now
	}
	date := t.Format("2006-01-02")
	ext := extFor(ep)
	title := sanitizeName(ep.Title)
	if title == "" {
		title = "untitled"
	}
	if budget := maxFileNameBytes - len(date) - len(" - ") - len(ext); len(title) > budget {
		if truncated := truncateName(title, budget); truncated != "" {
			title = truncated
		}
	}
	return date + " - " + title + ext, date
}

var unsafeNameChars = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	"\"", "_",
	"<", "_",
	">", "_",
	"|", "_",
)

// sanitizeName replaces path-unsafe characters with "_", trims surrounding
// whitespace and dots, and truncates to well under the 255-byte filesystem
// limit.
func sanitizeName(s string) string {
	s = unsafeNameChars.Replace(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " \t.")
	s = truncateName(s, maxNameBytes)
	return strings.Trim(s, " \t.")
}

// truncateName limits a name to maxNameRunes runes and maxBytes bytes so it
// stays well under the 255-byte filesystem limit even for multi-byte
// scripts.
func truncateName(s string, maxBytes int) string {
	if len(s) <= maxBytes && utf8.RuneCountInString(s) <= maxNameRunes {
		return s
	}
	var b strings.Builder
	b.Grow(min(maxBytes, maxNameBytes))
	runes := 0
	for _, r := range s {
		if runes >= maxNameRunes || b.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

var mimeExtensions = map[string]string{
	"audio/mpeg":      ".mp3",
	"audio/mp3":       ".mp3",
	"audio/mpeg3":     ".mp3",
	"audio/x-mpeg-3":  ".mp3",
	"audio/x-mp3":     ".mp3",
	"audio/mp4":       ".m4a",
	"audio/m4a":       ".m4a",
	"audio/x-m4a":     ".m4a",
	"audio/aac":       ".aac",
	"audio/x-aac":     ".aac",
	"audio/ogg":       ".ogg",
	"application/ogg": ".ogg",
	"audio/opus":      ".opus",
	"audio/webm":      ".webm",
	"audio/flac":      ".flac",
	"audio/x-flac":    ".flac",
	"audio/wav":       ".wav",
	"audio/x-wav":     ".wav",
	"audio/wave":      ".wav",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
}

// knownFileExtensions is the set of URL extensions accepted for extension
// derivation; anything else (e.g. .php, .html) is ignored and the .mp3
// default applies.
var knownFileExtensions = map[string]struct{}{
	".mp3": {}, ".m4a": {}, ".aac": {}, ".ogg": {}, ".oga": {},
	".opus": {}, ".webm": {}, ".flac": {}, ".wav": {}, ".wma": {},
	".aiff": {}, ".aif": {}, ".mp4": {}, ".m4v": {},
}

func extFor(ep feed.Episode) string {
	mime := strings.ToLower(strings.TrimSpace(ep.EnclosureType))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	if mime != "" {
		if ext, ok := mimeExtensions[mime]; ok {
			return ext
		}
	}
	if ext := urlExt(ep.EnclosureURL); ext != "" {
		return ext
	}
	return ".mp3"
}

func urlExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(u.Path))
	if _, ok := knownFileExtensions[ext]; ok {
		return ext
	}
	return ""
}

var pubDateLayouts = []string{
	"Mon, 02 Jan 2006 15:04:05 -0700", // RFC 1123
	"Mon, 02 Jan 2006 15:04:05 MST",
	"02 Jan 2006 15:04:05 -0700", // no weekday
	"02 Jan 2006 15:04:05 MST",
	"Mon, 02 Jan 06 15:04:05 -0700", // two-digit year
	"Mon, 02 Jan 06 15:04:05 MST",
	"02 Jan 06 15:04:05 -0700",
	"02 Jan 06 15:04:05 MST",
	time.RFC3339,
	"2006-01-02",
}

func parsePubDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, ok := tryLayouts(s); ok {
		return t, true
	}
	if norm := normalizePubDate(s); norm != s {
		return tryLayouts(norm)
	}
	return time.Time{}, false
}

func tryLayouts(s string) (time.Time, bool) {
	for _, layout := range pubDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// normalizePubDate fixes two common real-world deviations: a trailing
// "(GMT)"-style zone name after the offset, and an RFC 3339 "Z" suffix.
func normalizePubDate(s string) string {
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[:i])
	}
	if strings.HasSuffix(s, "Z") {
		s = strings.TrimRight(strings.TrimSuffix(s, "Z"), " ") + " +0000"
	}
	return s
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
