---
title: 'Config page'
type: 'feature'
created: '2026-08-30'
status: 'done'
review_loop_iteration: 0
context:
  - /Users/de156490/develop/podcastspeicher/_bmad-output/specs/spec-podcast-backup/SPEC.md
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** The poller (story 1) reads shows from `shows.txt` and applies a poll interval from `POLL_INTERVAL`, but neither is user-facing: modifying them requires shell access to the host. Users need a browser-based UI to manage subscriptions and the poll interval without touching the filesystem or restarting the container (SPEC: CAP-4, CAP-6).

**Approach:** Serve a single embedded HTML page from the Go binary that calls a small JSON API (`GET/POST/DELETE /api/shows`, `GET/PUT /api/settings`). The page is unauthenticated in v1 (trusted/local network assumed). A `GET /healthz` endpoint enables the Docker `HEALTHCHECK`. The `subs.Store` and `settings.Store` back the API, so config-page changes are reflected in the next poll cycle without a restart.

## Boundaries & Constraints

**Always:**
- The config page is one `go:embed`-served HTML file — no React, no Node toolchain, no build step.
- API validates all input: `POST /api/shows` rejects non-absolute-HTTP(S) URLs; `PUT /api/settings` rejects durations < 1 s or unparseable strings.
- Removing a show stops new downloads; its archive files are never touched.
- `DELETE /api/shows` removes the URL from `shows.txt` via `subs.Store.Remove` — that file remains the poller's source of truth.
- The HTTP server is a required capability: a bind failure is fatal (the binary exits rather than silently running without a config page).
- The server listens on `HTTP_ADDR` (default `:8080`); `--health` performs a loopback GET on `/healthz` for the Docker HEALTHCHECK and exits.

**Ask First:**
- Adding authentication or session handling to the config page.
- Changing the `shows.txt` format or the `settings.json` schema.
- Adding new API endpoints.

**Never:**
- No playback, search corpus, or static-site generation.
- No authentication in v1.
- No React/SPA.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| LIST_EMPTY | GET /api/shows, empty shows.txt | `{"shows":[]}` 200 | N/A |
| ADD_SHOW | POST /api/shows `{"url":"https://…"}` | 201 `{"url":"…"}`; URL appended to shows.txt | N/A |
| ADD_DUPLICATE | POST same URL twice | 409 `{"error":"already subscribed"}` | N/A |
| ADD_INVALID_URL | POST non-HTTP(S) or relative URL | 400 `{"error":"…"}` | N/A |
| REMOVE_SHOW | DELETE /api/shows?url=… | 200 `{"removed":"…"}`; line removed from shows.txt | N/A |
| REMOVE_MISSING | DELETE unknown URL | 404 `{"error":"not subscribed"}` | N/A |
| REMOVE_NO_PARAM | DELETE /api/shows (no query param) | 400 `{"error":"missing url query parameter"}` | N/A |
| GET_SETTINGS | GET /api/settings, no override | `{"poll_interval":"6h"}` (env/default fallback) | N/A |
| SET_INTERVAL | PUT /api/settings `{"poll_interval":"1h30m"}` | 200; settings.json updated atomically | N/A |
| SET_INVALID | PUT with 0s / negative / garbage | 400 `{"error":"…"}` | N/A |
| HEALTH | GET /healthz | 200 `ok` | N/A |
| UNKNOWN_PATH | GET /anything-else | 404 JSON error | N/A |
| CLI_HEALTH | binary run with `--health` | exits 0 when server answers, 1 on error | N/A |

</frozen-after-approval>

## Code Map

- `internal/web/web.go` -- `Server` struct + `Handler()` mux; all HTTP handlers; `effectiveInterval()` for settings fallback
- `internal/web/index.html` -- single embedded config page: Shows section (list + add + remove), Poll interval section (get + set), status flash; pure vanilla JS
- `internal/web/web_test.go` -- unit tests for all API endpoints and edge cases from the I/O matrix
- `internal/subs/subs.go` -- `Store.List/Add/Remove` backed by atomic `shows.txt` writes; `ErrAlreadySubscribed`, `ErrNotFound`
- `internal/subs/subs_test.go` -- unit tests for shows.txt parse, add, remove
- `internal/settings/settings.go` -- `Store.Get/SetPollInterval`, `ParseInterval`; atomic `settings.json` writes
- `internal/settings/settings_test.go` -- unit tests for settings persistence and interval validation
- `main.go` -- `run()`: wires `subs.Store`, `settings.Store`, `web.NewServer`, starts HTTP server in goroutine; `healthCheck()` for `--health` flag; poller re-reads interval after each tick

## Tasks & Acceptance

**Execution:**
- [x] `internal/subs/subs.go` + `subs_test.go` -- `Store` with `List/Add/Remove`, atomic writes, comment preservation
- [x] `internal/settings/settings.go` + `settings_test.go` -- `Store` with `Get/SetPollInterval`, `ParseInterval`, atomic writes
- [x] `internal/web/web.go` -- `Server` + `Handler()` mux + all five JSON API handlers + health endpoint
- [x] `internal/web/index.html` -- embedded single-page UI (shows list, add form, remove buttons, poll interval form, status flash)
- [x] `internal/web/web_test.go` -- table-driven tests covering the full I/O matrix
- [x] `main.go` -- wire everything together: stores, web server, `--health` flag, interval re-resolution after each poll tick

**Acceptance Criteria:**
- Given the binary is running, when `GET /` is requested, then the config page HTML is returned with `Content-Type: text/html`.
- Given the binary is running, when `GET /healthz` is requested, then `200 ok` is returned.
- Given shows.txt is empty, when the user adds a show URL on the config page (`POST /api/shows`), then the URL appears in `GET /api/shows` and in shows.txt, and the next poll picks it up.
- Given a show is listed, when the user removes it (`DELETE /api/shows?url=…`), then it no longer appears in `GET /api/shows` and in shows.txt, but its archive files are untouched.
- Given no settings override, when `GET /api/settings` is called, then `poll_interval` reflects `POLL_INTERVAL` env (or `"6h"` default).
- Given the user saves a new interval (`PUT /api/settings {"poll_interval":"1h30m"}`), then settings.json is updated and subsequent polls use the new cadence without a restart.
- Given an invalid interval (`0s`, negative, garbage), when `PUT /api/settings` is called, then a `400` is returned and settings.json is unchanged.
- Given the binary is running and `--health` is passed as an argument, when /healthz responds 200, then the process exits 0; when the server is unreachable, it exits 1.

## Spec Change Log

## Verification

**Commands:**
- `go build ./...` -- expected: clean build
- `go vet ./...` -- expected: no findings
- `go test ./...` -- expected: all pass (including `internal/web`, `internal/subs`, `internal/settings`)
