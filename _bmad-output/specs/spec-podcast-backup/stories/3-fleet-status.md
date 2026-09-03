---
title: 'Fleet status'
type: 'feature'
created: '2026-08-30'
status: 'done'
review_loop_iteration: 0
context:
  - /Users/de156490/develop/podcastspeicher/_bmad-output/specs/spec-podcast-backup/SPEC.md
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** The config page shows subscribed feeds but no operational data — the user cannot tell when a show was last fetched, how many episodes are archived, or how much disk space it uses (SPEC: CAP-5).

**Approach:** Add an in-memory `status.Store` that the poller populates after each successful `PollShow` call (last-fetched timestamp, episode count, disk bytes). A new `GET /api/status` endpoint serves this data as JSON; the config page renders it in a Fleet status table. The store is ephemeral (reset on restart) — no new files on disk.

## Boundaries & Constraints

**Always:**
- Status is ephemeral: it is populated from polls in the current process lifetime. The table shows "No polls completed yet." until at least one poll has run.
- `disk_bytes` is computed at record time by scanning non-temp files in the show directory.
- `episode_count` is the number of rows in the show's `podcast.md` registry after the poll, as returned by `mirror.PollShow`.
- The status endpoint is read-only (`GET /api/status`); no writes.
- Status is keyed by feed URL; recording the same feed a second time overwrites the previous entry (updates after each poll cycle).

**Ask First:**
- Persisting status to disk so it survives restarts.
- Exposing per-episode detail in the status API.

**Never:**
- No status for shows that have never been polled (not subscribed but lingering in the data dir).
- No authentication on the status endpoint (same as the rest of the config page in v1).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| EMPTY | GET /api/status, no polls yet | `{"shows":[]}` 200 | N/A |
| ONE_SHOW | After one poll completes | `{"shows":[{feed, last_fetched, episode_count, disk_bytes}]}` | N/A |
| REPEAT_POLL | Same feed polled twice | Only one entry; values reflect the latest poll | N/A |
| DIR_MISSING | Show dir removed after record | `disk_bytes` is 0 (computed from disk at record time, not query time) | N/A |

</frozen-after-approval>

## Code Map

- `internal/status/status.go` -- `Store` (in-memory, RWMutex-guarded); `ShowStatus` JSON struct; `Record(feedURL, showDir, episodeCount, now)`, `All()`; `diskBytes()` scans non-temp files; `isTempName()` mirrors mirror's logic
- `internal/status/status_test.go` -- unit tests: empty store, record, overwrite, diskBytes edge cases, isTempName
- `internal/mirror/mirror.go` -- `PollResult{ShowDir, EpisodeCount}` returned by `PollShow` (was `error` only)
- `internal/mirror/mirror_test.go` -- updated `pollPath`, `poll` helpers and goroutine calls to consume the new two-value return
- `internal/web/web.go` -- `Server.Status *status.Store` field; `handleGetStatus` handler; `GET /api/status` registered in `Handler()`; `NewServer` gains `statStore` parameter
- `internal/web/web_test.go` -- `newTestServer` passes `status.NewStore()`; `TestStatusAPI` covers empty/populated/overwrite
- `internal/web/index.html` -- Fleet status section: `<table id="fleet-table">` with feed/last-fetched/episodes/disk columns; `renderFleet()`, `fmtBytes()`, `fmtDate()` helpers; `load()` now parallel-fetches `/api/status`
- `main.go` -- `status.NewStore()` created in `run()`; passed to `web.NewServer` and `pollOnce`; `pollOnce` calls `statStore.Record` after each successful `PollShow`
- `main_test.go` -- updated `pollOnce` calls to pass `status.NewStore()`

## Tasks & Acceptance

**Execution:**
- [x] `internal/status/status.go` + `status_test.go` -- `Store`, `ShowStatus`, `diskBytes`, `isTempName`
- [x] `internal/mirror/mirror.go` -- `PollResult` type, `PollShow` returns `(PollResult, error)`
- [x] `internal/mirror/mirror_test.go` -- update three call sites to consume two-value return
- [x] `internal/web/web.go` -- `Status` field, `handleGetStatus`, `GET /api/status`, updated `NewServer`
- [x] `internal/web/web_test.go` -- `status.NewStore()` in `newTestServer`, `TestStatusAPI`
- [x] `internal/web/index.html` -- Fleet status section, `renderFleet`, `fmtBytes`, `fmtDate`, parallel `load()`
- [x] `main.go` -- wire `statStore`, update `pollOnce` signature and call sites
- [x] `main_test.go` -- pass `status.NewStore()` to `pollOnce` calls

**Acceptance Criteria:**
- Given no polls have completed, when `GET /api/status` is called, then `{"shows":[]}` is returned with status 200.
- Given a poll completes for a subscribed show, when `GET /api/status` is called, then the response includes one entry with `feed`, `last_fetched`, `episode_count`, and `disk_bytes` matching the show's actual state.
- Given the same show is polled twice, when `GET /api/status` is called, then only one entry is returned reflecting the most recent poll.
- Given the config page is loaded after a poll, when the Fleet status section is rendered, then a table row per polled show displays feed URL, last fetched time, episode count, and formatted disk size.
- Given no polls have run, when the Fleet status section is rendered, then "No polls completed yet." is displayed in the table.

## Spec Change Log

## Verification

**Commands:**
- `go build ./...` -- expected: clean build
- `go vet ./...` -- expected: no findings
- `go test ./...` -- expected: all pass (including `internal/status`, `internal/web`, `internal/mirror`, root package)
