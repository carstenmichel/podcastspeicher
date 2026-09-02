---
title: 'Core mirroring + archive'
type: 'feature'
created: '2026-08-30'
status: 'done'
review_loop_iteration: 1
baseline_commit: 'NO_VCS'
context:
  - /Users/carstenmichel/develop/podcastspeicher/_bmad-output/specs/spec-podcast-backup/SPEC.md
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** Podcast owners prune their feeds, so a feed is not a durable archive: episodes disappear and become unreachable. This story builds the core of the backup appliance — it mirrors subscribed feeds to local disk and never deletes anything, so the archive outlives feeds and app (SPEC: CAP-1, CAP-2, CAP-3).

**Approach:** A Go poller fetches each show's RSS 2.0 feed on a ~6h cycle, downloads every episode whose GUID is not recorded in the show's `podcast.md` registry (or whose file is missing), and stores episodes as plain files in per-show folders, recording GUIDs in the Markdown registry as the dedupe ledger.

## Boundaries & Constraints

**Always:**
- Never delete or overwrite an existing file on disk. A download is skipped when the target file exists; a GUID present in the registry but a missing file triggers a re-download (gap-fill, per prefer-duplicates-over-gaps).
- The per-podcast `podcast.md` Markdown registry is the single dedupe ledger; the episode GUIDs recorded in it drive dedupe. No other state: no database, no central registry file.
- Archive on disk is plain files + Markdown only.
- Go stdlib only for this story; RSS 2.0 parsed with `encoding/xml`.
- Shows are listed in `<DATA_DIR>/shows.txt` (one feed URL per line, `#` comments) — the storage story 2's config page will manage. Data dir from `DATA_DIR` (default `./data`), poll interval from `POLL_INTERVAL` (default `6h`).
- One poll cycle processes shows sequentially; an initial poll runs at startup.

**Ask First:**
- Adding any external Go dependency.
- Changing the data layout, the `podcast.md` registry format, or the `shows.txt` mechanism.

**Never:**
- No config page or HTTP server in this story (story 2).
- No UI playback, search corpus, sha256 content dedupe, or feed 404/rate-limit special handling — a failed fetch simply waits for the next poll.
- No Atom support in v1 (RSS 2.0 only); non-RSS feeds are logged and skipped.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| FIRST_POLL | feed with 3 episodes, empty show | all 3 files downloaded; 3 registry rows | N/A |
| REPEAT_POLL | same feed, files present | zero downloads; registry unchanged | N/A |
| FEED_GROWS | 4th episode appears | only the 4th downloaded | N/A |
| FILE_VANISHED | GUID in registry, file deleted | re-downloaded (gap-fill) | N/A |
| FETCH_FAILS | 404 / timeout / malformed XML | show skipped this cycle, error logged | retried on next poll |
| DOWNLOAD_INTERRUPTED | audio fetch fails mid-stream | temp file removed; no registry row | retried on next poll |

</frozen-after-approval>

## Code Map

Greenfield repo (no tracked files yet). Planned layout:

- `go.mod` -- module `podcastspeicher`, Go 1.27, stdlib only
- `main.go` -- env config (DATA_DIR, POLL_INTERVAL), shows.txt loader, poller loop (initial + ticker)
- `internal/feed/feed.go` -- fetch + RSS 2.0 parse; `Episode{GUID, Title, EnclosureURL, PubDate}`
- `internal/registry/registry.go` -- podcast.md read (GUID → file map) / append row / create header
- `internal/mirror/mirror.go` -- per-show cycle: episode → target path → exists guard → temp download → rename → registry row
- `Dockerfile` -- multi-stage: golang builder with CGO_ENABLED=0, `gcr.io/distroless/static`, `/data` volume
- `*_test.go` alongside each internal package; mirror tests use `net/http/httptest` with a fixture feed

## Tasks & Acceptance

**Execution:**
- [x] `go.mod`, `main.go` -- scaffold module, env config, shows.txt loader (create empty file if missing), poller loop -- binary entrypoint
- [x] `internal/feed/feed.go` + `feed_test.go` -- fetch with timeout, RSS 2.0 parse, GUID chain itunes:episodeGuid → guid → link, first downloadable enclosure selection -- dedupe identity
- [x] `internal/registry/registry.go` + `registry_test.go` -- podcast.md parse/write roundtrip, header creation -- the ledger
- [x] `internal/mirror/mirror.go` + `mirror_test.go` -- download cycle: exists guard, temp+rename, gap-fill, sequential shows -- never-delete core
- [x] `Dockerfile` -- distroless single-binary image -- deployment constraint

**Acceptance Criteria:**
- Given a show is configured in shows.txt, when the first poll completes, then every feed episode exists under `<DATA_DIR>/<Show>/` as `<YYYY-MM-DD> - <Title>.<ext>` and in `podcast.md` with its GUID.
- Given a mirrored episode, when the feed later drops it, then the file and its registry row remain untouched.
- Given a process restart, when the next poll runs, then no already-mirrored episode is re-downloaded.
- Given shows.txt is missing, when the binary starts, then it creates an empty shows.txt and logs that no shows are configured.

## Spec Change Log

- Trigger: step-04 review (blind + edge-case hunters) — show directory identity derived solely from the channel title: two same-titled feeds would share a directory (cross-contaminated dedupe, silent episode loss on filename collision) and a channel rename would spawn a new directory with a full re-download.
- Amended: Design Notes — show directory is resolved by the feed URL recorded in the podcast.md header (reused across renames); a deterministic feed-URL hash suffix separates same-titled shows; plus explicit safety guards (download cap, Content-Length/zero-byte checks, collision suffix, name truncation).
- Known-bad state avoided: episodes silently lost to a shared directory; duplicate full re-downloads after a feed rename; unbounded enclosure downloads.
- KEEP: package layout, podcast.md registry format, `<date> - <title>.<ext>` filename scheme, temp+rename download, file-exists guard, GUID chain, all existing tests.
- Doc sync (round-2 patches, no behavior change): Code Map annotation "first-enclosure selection" corrected to "first downloadable enclosure selection" to match the implementation, which selects the first enclosure with a valid absolute URL.

## Design Notes

GUID resolution: `itunes:episodeGuid` → `<guid>` → `<link>`; no stable id → download whenever the file is missing (duplicate tolerated).

The file-exists guard is the never-overwrite mechanism; the registry row is the dedupe ledger. A registry row without its file is a gap → re-download.

Registry format (written and read by the poller, human-legible):

```
# <Show Title>

Feed: <url>

| Date | Title | GUID | File |
|------|-------|------|------|
| 2026-08-29 | Ep 123 | abc123 | 2026-08-29 - Ep 123.mp3 |
```

Filename: publish date (RFC 822 `pubDate`, falling back to download date when unparseable) + sanitized title; sanitize only path-unsafe characters; truncate sanitized names to well under the 255-byte filesystem limit.

Show directory identity: the folder is named after the channel title, but is *resolved* by the feed URL recorded in the existing `podcast.md` header — scan existing show directories' headers first and reuse the matching directory (stable across channel renames, no re-download). When no directory records the feed URL, create one from the title-derived name; if that name is already taken by a directory recording a *different* feed URL, append a deterministic short hash of the feed URL to the folder name so two same-titled shows never share a directory.

Safety guards: enclosure downloads are size-capped (2 GiB); a `Content-Length` that does not match the bytes written, a zero-byte body, or a cap breach aborts the download (temp removed, no registry row, retried next poll). Sanitized episode-title collisions (two GUIDs, same date+title) get a deterministic short hash suffix on the filename so no episode is silently dropped.

## Verification

**Commands:**
- `go build ./...` -- expected: clean build
- `go vet ./...` -- expected: no findings
- `go test ./...` -- expected: all pass
- `CGO_ENABLED=0 go build -o /tmp/ps .` -- expected: statically linked binary builds (distroless readiness)
- `docker build -t podcastspeicher:local .` -- expected: image builds (distroless final stage, single binary)

**Manual checks (if no CLI):**
- `DATA_DIR=$(mktemp -d) POLL_INTERVAL=5s /tmp/ps` with one real feed URL in `shows.txt` → episodes land within seconds; kill the process; browse the folder and `podcast.md` (kill drill); re-run → zero re-downloads.
- `docker run --rm -v $(mktemp -d):/data -e POLL_INTERVAL=5s podcastspeicher:local` with one feed URL pre-seeded in the mounted volume's `shows.txt` → episodes land in the volume; `docker run --rm -v <same volume>:/data cat /data/<Show>/podcast.md` shows the registry rows; a second run with the same volume → zero re-downloads (container kill drill).

## Suggested Review Order

**Entry point & poll loop**

- Entry: env config, initial poll, ticker, graceful shutdown
  [`main.go:32`](../../../../main.go#L32)

- Per-cycle shows.txt reload and per-show error isolation
  [`main.go:76`](../../../../main.go#L76)

- shows.txt parsing: BOM, comments, dedupe, auto-create
  [`main.go:114`](../../../../main.go#L114)

**Mirror core — never-delete invariant**

- Per-show poll cycle
  [`mirror.go:69`](../../../../internal/mirror/mirror.go#L69)

- File-exists guard, GUID skip, ledger self-heal
  [`mirror.go:137`](../../../../internal/mirror/mirror.go#L137)

- Deterministic collision suffix so no episode is dropped
  [`mirror.go:191`](../../../../internal/mirror/mirror.go#L191)

- Download: temp+rename, cap, integrity checks, pre-rename guard, fsync
  [`mirror.go:265`](../../../../internal/mirror/mirror.go#L265)

- Stale temp cleanup that never touches archive files
  [`mirror.go:223`](../../../../internal/mirror/mirror.go#L223)

**Show directory identity**

- Feed-URL-resolved directory, rename-stable, collision suffix
  [`mirror.go:109`](../../../../internal/mirror/mirror.go#L109)

**Feed parsing**

- Fetch with timeout, 10 MiB cap, explicit errors
  [`feed.go:45`](../../../../internal/feed/feed.go#L45)

- RSS 2.0 only; missing channel rejected
  [`feed.go:73`](../../../../internal/feed/feed.go#L73)

- GUID chain: itunes:episodeGuid → guid → link
  [`feed.go:104`](../../../../internal/feed/feed.go#L104)

- First downloadable enclosure wins
  [`feed.go:113`](../../../../internal/feed/feed.go#L113)

**Registry ledger**

- Atomic, non-truncating header creation with sanitized title
  [`registry.go:44`](../../../../internal/registry/registry.go#L44)

- Row append with in-memory sync + fsync
  [`registry.go:88`](../../../../internal/registry/registry.go#L88)

- Defensive header feed-URL read (directory identity source)
  [`registry.go:112`](../../../../internal/registry/registry.go#L112)

**Deployment**

- nonroot /data ownership so named volumes work
  [`Dockerfile:13`](../../../../Dockerfile#L13)

**Supporting tests**

- AC-2: feed prunes episodes, archive untouched
  [`mirror_test.go:727`](../../../../internal/mirror/mirror_test.go#L727)
