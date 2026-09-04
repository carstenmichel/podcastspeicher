# Deferred-work ledger — last swept 2026-09-04 (manual verification against codebase).
# status: open | resolved | needs-human-decision
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Project-level CI (go vet/test + docker build) missing.
  status: open
  evidence: "2026-09-04 sweep: no .github/workflows or any CI config in repo. (Original entry also covered LICENSE — now resolved, tracked as its own entry.)"
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: LICENSE decision.
  status: resolved
  evidence: "2026-09-04 sweep: LICENSE.txt present at repo root (commit ec72843 'License')."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Poll cycles have no per-show budget or parallelism — one slow feed or large download delays every later show in the cycle.
  status: open
  evidence: "2026-09-04 sweep: pollOnce still loops shows sequentially (main.go:157-169), no per-show timeout budget or worker pool. Spec mandates sequential processing (frozen \"Always\"); fleet-scale optimization remains a later story."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: No download resume — an interrupted large transfer restarts from byte zero on the next poll.
  status: open
  evidence: "2026-09-04 sweep: download issues a plain GET with no Range header and no partial-file bookkeeping (internal/mirror/mirror.go:277). Safe (temp removed, no row, retried); bounded by the 2 GiB cap; resume is a later enhancement."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Download timeout (15 min) and size cap (2 GiB) are hard-coded, not configurable via env.
  status: open
  evidence: "2026-09-04 sweep: constants still hard-coded (internal/mirror/mirror.go:29-30); only POLL_INTERVAL, DATA_DIR, HTTP_ADDR are env-configurable. Sufficient for v1; configurability is a later enhancement."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Show-directory matching compares feed URLs as exact strings — a URL change (trailing slash, scheme, query) spawns a new directory and re-download.
  status: open
  evidence: "2026-09-04 sweep: no feed-URL normalization in subs/registry/mirror (url.Parse usages are for hostname hash suffix and date handling only). Re-download is a tolerated duplicate per the dedupe policy (no data loss); URL normalization is a later enhancement."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Single-instance assumption — two processes sharing one DATA_DIR can delete each other's in-progress .part temp files.
  status: resolved
  evidence: "2026-09-04 sweep: documented in README.md:51 ('One container per data directory — do not run two instances against the same volume.'). One appliance per data dir remains the intended deployment."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Operational affordances in the shellless image (health endpoint, --health flag, Docker HEALTHCHECK).
  status: resolved
  evidence: "2026-09-04 sweep: /healthz served by config server (internal/web/web.go:75), --health exit-code flag for the shellless image (main.go:40,52-74), and HEALTHCHECK in Dockerfile:21. --version and --once/cron mode remain unimplemented — acceptable for the single-container deployment model."
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Exotic collision: two episodes with NO stable id (no itunes:episodeGuid, no guid, no link) and identical sanitized date+title still collapse to one file.
  status: needs-human-decision
  evidence: "2026-09-04 sweep: registry format unchanged (| Date | Title | GUID | File |, internal/registry/registry.go:14-19); no enclosure-URL key. Resolving requires storing an enclosure-URL key in the frozen podcast.md registry format (Ask First: human-gated) or an explicit documented acceptance."
