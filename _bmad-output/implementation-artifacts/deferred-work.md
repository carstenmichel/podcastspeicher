- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Project-level scaffolding missing: CI (go vet/test + docker build) and a LICENSE decision; README is story 4 (CAP-7) and shows.txt is auto-created at runtime, so neither belongs to story 1.
  evidence: Story-1 review flagged the brand-new module has no CI job or LICENSE; neither is a story-1 acceptance criterion — both are project-level follow-ups.
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Poll cycles have no per-show budget or parallelism — one slow feed or large download delays every later show in the cycle.
  evidence: Spec mandates sequential processing (frozen "Always"); fleet-scale optimization is a later story, not a story-1 defect.
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: No download resume — an interrupted large transfer restarts from byte zero on the next poll.
  evidence: Safe (temp removed, no row, retried); bounded by the 2 GiB cap; resume is a later enhancement.
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Download timeout (15 min) and size cap (2 GiB) are hard-coded, not configurable via env.
  evidence: Sufficient for v1; configurability is a later enhancement.
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Show-directory matching compares feed URLs as exact strings — a URL change (trailing slash, scheme, query) spawns a new directory and re-download.
  evidence: Re-download is a tolerated duplicate per the dedupe policy (no data loss); URL normalization is a later enhancement.
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Single-instance assumption — two processes sharing one DATA_DIR can delete each other's in-progress .part temp files.
  evidence: One appliance per data dir is the intended deployment; must be documented in the README (story 4).
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: No operational affordances in the shellless image (--version, --once/cron mode, HEALTHCHECK target).
  evidence: Revisit when story 2 adds the HTTP config server (a health endpoint becomes possible then).
- source_spec: `_bmad-output/specs/spec-podcast-backup/stories/1-core-mirroring-archive.md`
  summary: Exotic collision: two episodes with NO stable id (no itunes:episodeGuid, no guid, no link) and identical sanitized date+title still collapse to one file.
  evidence: Resolving requires storing an enclosure-URL key in the frozen podcast.md registry format (Ask First: human-gated) or an explicit documented acceptance; needs a human decision.
