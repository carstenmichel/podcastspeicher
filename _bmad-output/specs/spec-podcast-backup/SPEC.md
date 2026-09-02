---
id: SPEC-podcast-backup
companions:
  - ../../brainstorming/brainstorm-podcast-backup-2026-08-29/README-notes.md
sources:
  - ../../brainstorming/brainstorm-podcast-backup-2026-08-29/brainstorm-intent.md
---

> **Canonical contract.** This SPEC and the files in `companions:` are the complete, preservation-validated contract for what to build, test, and validate. Source documents listed in frontmatter are for traceability — consult them only if you need narrative rationale or prose color this contract intentionally omits.

# podcastspeicher — podcast backup appliance

## Why

Podcast owners prune old episodes from their feeds, so a feed is not a durable archive and a late subscriber can never get episodes the feed no longer lists. This spec exists to solve that pain: the operator wants their subscriptions saved to local disk, folder-per-show, outliving any single app or feed. The backup must stay independent of the tool — the archive outlives the app, which makes the app a replaceable appliance.

## Capabilities

- **CAP-1 — Episode mirroring (MUST)**
  - **intent:** The system can fetch the RSS feed of each subscribed show on a recurring ~6-hour poll and download to local disk every episode not yet recorded in that show's registry.
  - **success:** A newly published episode appears on disk as a playable file within one poll cycle; an episode whose GUID is already recorded is not re-downloaded.
- **CAP-2 — Permanent archive (MUST)**
  - **intent:** The system can retain every downloaded episode indefinitely and never deletes or overwrites any file on disk.
  - **success:** After repeated polls in which feeds prune episodes, every previously downloaded episode file still exists on disk.
- **CAP-3 — Human-readable archive (MUST)**
  - **intent:** The archive can be organized in folders by podcast name + date, with a per-podcast `podcast.md` Markdown registry recording each episode's GUID, so the archive stays fully usable without the app.
  - **success:** With the container stopped, a user can browse the folders, read a `podcast.md`, and play an episode directly from its file path.
- **CAP-4 — Subscription management (MUST)**
  - **intent:** The user can add and remove podcast shows, by RSS feed URL, on the built-in config page.
  - **success:** Adding a show starts mirroring on the next poll; removing a show stops new downloads while leaving its existing files on disk.
- **CAP-5 — Fleet status (SHOULD)**
  - **intent:** The user can see per-show last fetch time, episode count, and disk usage on the config page.
  - **success:** After a poll, each show's status matches its actual last fetch time, episode count, and disk usage.
- **CAP-6 — Poll interval override (SHOULD)**
  - **intent:** The user can override the default ~6-hour poll interval.
  - **success:** Changing the interval changes the cadence of subsequent fetches.
- **CAP-7 — README warning and scope note (MUST)**
  - **intent:** The project README can carry a prominent callout near the top stating that feeds are capped at roughly the last 20–50 episodes, so late subscribers cannot catch up on episodes the feed no longer lists, that the app only mirrors what the stream still serves, and that the scope is a personal backup of the operator's own subscriptions.
  - **success:** A new user reading the top of the README encounters the truncation warning and the personal-backup scope note before any instruction for adding shows.

## Constraints

- Single Go binary in a small distroless Docker image; near-zero resource use at idle.
- The config page is one embedded HTML document served by the Go binary (`go:embed`) — no React, no Node toolchain.
- The per-podcast `podcast.md` Markdown registry (chosen over a central registry file) is the poll dedupe ledger: the episode GUIDs recorded in it drive dedupe.
- v1 dedupe policy is prefer-duplicates-over-gaps: download when seen, tolerate a handful of duplicates per podcast; content (sha256) dedupe is a later release.
- The on-disk archive is plain files + Markdown only — no proprietary or binary formats — so it remains usable (grep, Obsidian, any reader) without the app.
- The config page is unauthenticated in v1; the container is assumed reachable only from a trusted/local network.

## Non-goals

- No playback in the UI
- No React SPA
- No static-site generation
- No search corpus
- No feed 404 / rate-limit special handling in v1 (a failed fetch simply waits for the next poll)
- No content (sha256) dedupe in v1
- No access control or authentication on the config page in v1

## Success signal

With the container stopped, a user can open any podcast folder, read its `podcast.md`, and play an episode from its file path — the whole archive is legible without the app or its feed ever running again (kill drill).

