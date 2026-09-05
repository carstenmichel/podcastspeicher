# UBI Base Image Trial — Differences Report

Branch: `tmp/ubi-dockerfile` · Date: 2026-09-05 · Docker 28.3.3 (macOS, arm64)

Trial of Red Hat certified (UBI9) base images as an alternative to the current
`golang` / `distroless` base in `Dockerfile`. Built as `podcastspeicher-ubi:tmp`
and smoke-tested; original `Dockerfile` left untouched for side-by-side use.

## Result

| | Current (`Dockerfile`) | UBI variant (`Dockerfile.ubi`) |
|---|---|---|
| Build base | `golang:1.27` (1.31 GB) | `registry.redhat.io/ubi9/go-toolset:1.26` (1.62 GB) |
| Runtime base | `gcr.io/distroless/static` (6.42 MB) | `registry.redhat.io/ubi9/ubi-micro` (33.1 MB) |
| Final image | **16.2 MB** | **42.9 MB** (+26.7 MB, ~2.6×) |
| Container user | `nonroot` = uid 65532 | numeric `1001:1001` (UBI's `nonroot`) |
| Vendor support | none (community) | Red Hat SLA, RHSA advisories, UBI9 supported to ~2032 |

Smoke test: container starts, `/healthz` → HTTP 200, `shows.txt` created in a
bind-mounted `/data` (uid 1001), config server + poller running. **Pass.**

## File-by-file differences

### `Dockerfile` — unchanged

Kept exactly as-is so both variants can be built and compared in one repo:
`docker build -f Dockerfile` vs `docker build -f Dockerfile.ubi`.

### `Dockerfile.ubi` — new file, line-by-line against `Dockerfile`

| # | Original (`Dockerfile`) | UBI variant (`Dockerfile.ubi`) | Why |
|---|---|---|---|
| 1 | `FROM golang:1.27 AS build` | `FROM registry.redhat.io/ubi9/go-toolset:1.26 AS build` | No `go-toolset:1.27` tag exists yet in the Red Hat registry (checked 1.26/1.25/1.24 — 1.26 is newest). |
| 2 | — | `ENV PATH=/usr/local/go/bin:${PATH}` | UBI puts Go on PATH via `/etc/profile.d`, which only applies to login shells; `docker RUN` is a non-login shell, so `go` would not be found. |
| 3 | — | `ENV GOTOOLCHAIN=auto` | The toolset image ships `GOTOOLCHAIN=local`, which hard-fails on `go.mod` (`go 1.27`). `auto` fetches the go1.27.x toolchain from the module proxy at build time. (Note: `GOTOOLCHAIN=go1.27` is invalid — it must be a full toolchain version like `go1.27.0`.) |
| 4 | — | `USER root` | Toolset's default user is the unprivileged `builder` (uid 1001); the build writes to `/out`, which is only root-writable. Build stage is throwaway, so root is fine. |
| 5 | `RUN CGO_ENABLED=0 … chown 65532:65532 /datadir` | `RUN CGO_ENABLED=0 … chown 1001:1001 /datadir` | UBI's `nonroot` user is uid/gid **1001**, not distroless' 65532. Build command itself (`CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"`) unchanged. |
| 6 | `FROM gcr.io/distroless/static` | `FROM registry.redhat.io/ubi9/ubi-micro:latest` | Certified, vendor-supported runtime base (smallest UBI variant). |
| 7 | `COPY --from=build --chown=65532:65532 /datadir /data` | `COPY --from=build --chown=1001:1001 /datadir /data` | Same uid/gid change as #5. |
| 8 | `USER nonroot:nonroot` | `USER 1001:1001` | **`ubi-micro` has no `/etc/passwd`** — the name `nonroot` does not resolve (`unable to find user nonroot: no matching entries in passwd file`). Numeric form is required. |
| 9 | `ENV DATA_DIR=/data` | unchanged | — |
| 10 | `COPY --from=build /out/podcastspeicher /usr/local/bin/…` | unchanged | Static binary runs on any base. |
| 11 | `VOLUME /data` | unchanged | — |
| 12 | `EXPOSE 8080` | unchanged | — |
| 13 | `HEALTHCHECK … CMD ["/usr/local/bin/podcastspeicher", "--health"]` | unchanged | Exec-form healthcheck works without a shell (ubi-micro has none, same as distroless). |
| 14 | `ENTRYPOINT ["/usr/local/bin/podcastspeicher"]` | unchanged | — |

Net: 4 instructions added in the build stage, 4 instructions changed
(3 uid-related, 1 runtime base), 6 instructions identical.

## Issues hit during the trial (and fixes)

1. **Registry auth**: `registry.redhat.io` requires `docker login` (free Red Hat
   account); anonymous pulls are rejected.
2. **`go-toolset:1.27` missing** → use `:1.26` + `GOTOOLCHAIN=auto` (#3 above).
3. **`GOTOOLCHAIN=local` baked into toolset image** → first build failed with
   `go.mod requires go >= 1.27 (running go 1.26.7; GOTOOLCHAIN=local)`.
4. **`mkdir /out/: permission denied`** → toolset default user is `builder`
   (uid 1001), not root → `USER root` in build stage.
5. **`unable to find user nonroot`** → `ubi-micro` ships no `/etc/passwd`
   → numeric `USER 1001:1001`.

Environment note (not a UBI issue): bind mounts from paths outside Docker
Desktop's shared file area (e.g. `/var/folders/…`) present differently and a
non-root container user cannot write them; under the normal shared area
(`~/develop/…`) both uid 65532 and uid 1001 write fine.

## Verdict

- **Runtime**: `distroless/static` remains the better choice for this app on
  size (6.4 MB vs 33 MB base) and attack surface. UBI only wins if you need
  vendor support/SLA, certification for regulated environments, or OpenShift
  parity.
- **Build stage**: low stakes (never shipped). Switching to `ubi9/go-toolset`
  is viable (worked with the fixes above) if Red Hat coverage of the whole
  pipeline is desired.
- **Cost of switching runtime to UBI**: +26.7 MB image, login required to pull,
  numeric-user Dockerfile quirks, toolchain auto-download at build time.

## Reproduce

```sh
docker login registry.redhat.io          # free Red Hat account
docker build -f Dockerfile.ubi -t podcastspeicher-ubi:tmp .
docker run --rm -p 18080:8080 -v $PWD/data:/data podcastspeicher-ubi:tmp
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:18080/healthz  # → 200
```

## Cleanup (when done)

```sh
docker rmi podcastspeicher-ubi:tmp
herdr worktree remove   # or: git worktree remove ../podcastspeicher-ubi
git branch -d tmp/ubi-dockerfile
```
