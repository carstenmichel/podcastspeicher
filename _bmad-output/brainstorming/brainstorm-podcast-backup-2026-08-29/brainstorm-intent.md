# Intent: podcastspeicher

## Concept
A lean podcast backup appliance: a single Go binary in a distroless Docker image that mirrors RSS podcast episodes to disk and never deletes anything.

## Motivation / Problem
Podcast owners remove old episodes from their feeds, so a podcast's history is not a durable archive. A late subscriber cannot get episodes the feed no longer lists. The user wants their subscriptions saved to local disk, folder-per-show, outliving any single app or feed. Scope is a personal backup of the operator's own subscriptions; the README states this.

## Core Benefit
The backup is independent of the tool and survives it: the archive outlives the app, so the app is a replaceable appliance.

**Kill drill property:** stop the container, browse folders, read a `podcast.md`, play an episode from its file path — the archive is fully legible without the app ever running.

## Confirmed Architecture Decisions
- Single Go binary in a small distroless Docker image; consumes almost no resources at idle.
- Single embedded HTML config page served by the Go binary (`go:embed`) — no React, no Node toolchain.
- Per-podcast `podcast.md` Markdown registry inside each podcast's download folder (chosen over a central registry file); the episode GUIDs recorded in it are the poll dedupe ledger.
- Folder structure by podcast name + date.
- Mirror RSS episodes on a ~6-hour (configurable) poll; never delete.
- Dedupe policy: prefer duplicates over gaps — download when seen, tolerate a handful of duplicates per podcast; content dedupe (sha256) is a later release.
- Config page is unauthenticated in v1 (trusted/local network assumed).
- Removing a show stops future downloads; its files stay on disk (never-delete).

## Scope — v1
**MUST:**
- Fetch + download episodes on ~6h poll
- Never delete
- Folders by podcast name + date
- Per-podcast `podcast.md` registry with episode GUIDs
- Config page: add/remove shows
- Docker single-binary image
- README feed-truncation warning + personal-backup scope note

**SHOULD:**
- Fleet status: last fetch, episode count, disk usage
- Poll interval override

## Later-Release Backlog
- Feed 404 / rate-limit handling so a failed fetch is retried on the next poll (demoted out of v1)
- sha256 content dedupe to clean up the tolerated duplicates (confirmed out of v1)

## Explicit Non-Goals
- No playback in the UI
- No React SPA
- No static-site generation
- No search corpus
- No access control / auth on the config page in v1

## README Requirement
Prominently documented limitation (callout/warning near the top): feeds are capped at roughly the last 20–50 episodes, so late subscribers cannot catch up on episodes the feed no longer lists. The app can only mirror what the stream still serves. Users must see this before adding shows.

## Design Principles
- **The feed is untrustworthy; the disk is the truth.** Markdown registry + prefer-duplicates-over-gaps + feed-truncation warning are one idea in three hats: `podcast.md` is the ledger that outlives the feed's lies.
- **No lock-in; human-readable archive.** Plain files + Markdown keep the whole archive usable (grep, Obsidian, any reader) if the app dies.
- **Don't over-engineer.** Scope stays a lean backup tool; every rejected feature (player, site generation, search corpus) would have moved value back into the tool.
- **The tool is a replaceable appliance the archive outlives.** Shrink the tool until only files remain.
