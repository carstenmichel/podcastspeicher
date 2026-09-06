# podcastspeicher

A lean podcast backup appliance: a single Go binary (distroless Docker image) that mirrors the RSS feeds you subscribe to onto local disk — and never deletes anything.

> ## ⚠️ Read this before adding shows
>
> **Feeds are truncated — you can only get what the feed still lists.**
> Most podcast hosts cap their RSS feed at roughly the last 20–50 episodes.
> If you add a podcast late, episodes that the feed no longer offers are
> **unreachable via the feed**. This app can only mirror what the stream
> still serves.
>
> **This is a personal backup tool** for your own subscriptions.

## Why

Podcast owners regularly remove old episodes from their feeds, so a feed is not a durable archive: a podcast's history is not what you can get from it. podcastspeicher exists to solve that — it saves your subscriptions to local disk, one folder per show, so the archive outlives both the feed and the app.

The design principle: **the feed is untrustworthy; the disk is the truth.** The archive is plain files plus Markdown, fully legible without the app ever running — stop the container, browse the folders, read a `podcast.md`, play an episode from its file path. The app is a replaceable appliance; the archive is what you keep.

## How it works

- Every `POLL_INTERVAL` (default 6 h) the poller fetches each show's RSS 2.0 feed.
- Episodes whose GUID is not yet recorded in the show's `podcast.md` registry are downloaded into the show's folder.
- Nothing is ever deleted or overwritten. If a file goes missing, it is re-downloaded on the next poll (prefer duplicates over gaps).
- A failed fetch or download simply waits for the next poll — no special handling, no state beyond the files.

## Install

### Docker (recommended)

```sh
docker build -t podcastspeicher .

# Prepare the data directory with your first show
mkdir -p ~/podcastspeicher-data
echo "https://example.com/feed.xml" > ~/podcastspeicher-data/shows.txt

docker run -d --name podcastspeicher \
  -p 8080:8080 \
  -v ~/podcastspeicher-data:/data \
  podcastspeicher
```

The config page is at <http://localhost:8080> — add and remove shows and change the poll interval from the browser.

Notes:

- The container runs as `nonroot` (uid 65532) and writes to `/data`. On Linux, make sure the host directory is writable by that uid (`chown 65532:65532 ~/podcastspeicher-data`); on Docker Desktop (macOS/Windows) it just works.
- Named volumes work too; on Linux the image ships a nonroot-owned `/data` so the seeded volume is writable. On Docker Desktop (macOS) the volume driver ignores image ownership — pre-seed the volume's `shows.txt` with matching ownership instead.
- One container per data directory — do not run two instances against the same volume.

### Synology (DiskStation)

Use the committed [`docker-compose.yml`](docker-compose.yml) — it builds the image on the NAS (no registry or cross-compile needed):

```sh
# 1. Package Center → install "Docker" and "Git" (if not present).
# 2. Clone the repo into a shared folder:
git clone <repo> /volume1/docker/podcastspeicher-src

# 3. Create the data dir and hand it to the container user (uid 65532);
#    shared folders are root-owned, so this step is required:
sudo mkdir -p /volume1/docker/podcastspeicher
sudo chown -R 65532:65532 /volume1/docker/podcastspeicher

# 4. Start:
cd /volume1/docker/podcastspeicher-src
docker compose up -d        # or: Container Manager → Projects → Create

# 5. Verify:
curl http://localhost:8080/healthz
```

The config page is at <http://<nas-ip>:8080>. Host path and port are overridable via `PODCASTSPEICHER_DATA_DIR` and `PODCASTSPEICHER_PORT`. **Keep port 8080 on the LAN only** — the config page is unauthenticated; for remote access use the Synology VPN or a QuickConnect tunnel, not a port forward.

### Go binary

Requires Go ≥ 1.27.

```sh
go build -o podcastspeicher .
./podcastspeicher        # archives to ./data
```

Or run it from source: `go run .`.

## Shows setup

Shows are managed on the [config page](#configuration) or, equivalently, in `shows.txt` inside the data directory (`DATA_DIR/shows.txt`). The file is created empty on first start if it does not exist.

```
# One RSS feed URL per line. Lines starting with # are comments.
https://lexfrg.com/feed/
https://changelog.com/podcast/feed
# a comment after a URL needs a space before the #
```

Behavior:

- **Edits take effect on the next poll** — add or remove a URL (config page or file) and the change applies without restarting the app.
- Removing a show stops new downloads; its files stay on disk (nothing is ever deleted).
- Duplicate URLs are ignored.
- A feed that fails (404, timeout, malformed XML, non-RSS) is skipped for that cycle and retried on the next poll — the other shows are not affected.
- Only **RSS 2.0** feeds are supported (Atom feeds are logged and skipped).
- With no shows configured the app logs a warning and keeps polling, waiting for `shows.txt` to be filled in.

To find a show's feed URL: look for a "RSS" link on the podcast's website, or use your player (e.g. in Apple Podcasts: show page → ⋯ → "Copy RSS Feed URL").

## Configuration

The config page (served by the binary, no Node/React) is the primary interface:

| What | Where |
|---|---|
| Add / remove shows by RSS feed URL | `GET/POST/DELETE /api/shows` (page: **Shows**) |
| Override the poll interval | `GET/PUT /api/settings` (page: **Poll interval**) |
| Health check | `GET /healthz` |

Removing a show stops new downloads; its files stay on disk (nothing is ever deleted). The page is unauthenticated — it is meant for a trusted/local network only.

Environment variables:

| Variable | Default | Description |
|---|---|---|
| `DATA_DIR` | `./data` (`/data` in the container) | Root of the archive: `shows.txt`, `settings.json`, show folders, registries. |
| `POLL_INTERVAL` | `6h` | Poll cycle as a Go duration (`30m`, `2h`, `1h30m`). Minimum `1s`. Overridden by `settings.json` when it holds a value. |
| `HTTP_ADDR` | `:8080` | Listen address of the config page. |

The poll interval set on the config page is persisted to `settings.json` in the data directory and wins over `POLL_INTERVAL`; a change applies from the next poll cycle without a restart. `shows.txt` is re-read every poll cycle.

## Archive layout

```
data/
├── shows.txt
├── settings.json
└── Lex Fridman/
    ├── podcast.md
    ├── 2026-08-29 - 415. The Big One.mp3
    └── 2026-08-30 - 416. Follow Up.mp3
```

- One folder per show, named after the feed's channel title (path-unsafe characters replaced, truncated to fit the filesystem).
- Episode files are named `<YYYY-MM-DD> - <Episode Title>.<ext>` using the publish date (download date when the feed gives none).
- `podcast.md` is the human-readable ledger the poller dedupes against:

```markdown
# Lex Fridman

Feed: https://lexfrg.com/feed/

| Date | Title | GUID | File |
|------|-------|------|------|
| 2026-08-29 | 415. The Big One | 9f2c1a... | 2026-08-29 - 415. The Big One.mp3 |
| 2026-08-30 | 416. Follow Up | b71d4e... | 2026-08-30 - 416. Follow Up.mp3 |
```

The archive is plain files and Markdown by design — grep it, sync it, open it in Obsidian. If the app ever dies, nothing is lost.

## Guarantees

- **Never deletes, never overwrites.** An existing file is always kept as-is.
- **Prefer duplicates over gaps.** When in doubt (missing file, changed feed entry), the episode is downloaded again.
- **No hidden state.** Everything the app knows is plain text in the data directory: `shows.txt`, `settings.json`, and the `podcast.md` ledgers.
- **Fails safe.** A failed download leaves no partial file and no ledger row; the next poll retries.

## Development

```sh
go test ./...       # unit + integration tests (local feed servers, no network)
go vet ./...
docker build -t podcastspeicher .
```

The story specs and planning artifacts live in `_bmad-output/` (not part of the application).

## License

See [LICENSE.txt](LICENSE.txt).
