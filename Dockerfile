FROM golang:1.27 AS build
WORKDIR /src
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/podcastspeicher . \
    && mkdir -p /datadir && chown 65532:65532 /datadir

FROM gcr.io/distroless/static
# /data must be nonroot-owned (uid/gid 65532 = distroless "nonroot") so a
# named volume seeded from it is writable by the nonroot container user.
# distroless has no shell, so the directory is prepared in the build stage
# and copied with an explicit --chown (numeric: distroless has no
# /etc/passwd for user-name resolution).
COPY --from=build --chown=65532:65532 /datadir /data
USER nonroot:nonroot
ENV DATA_DIR=/data
COPY --from=build /out/podcastspeicher /usr/local/bin/podcastspeicher
VOLUME /data
EXPOSE 8080
# distroless has no shell, so the healthcheck is the binary itself doing one
# GET on the config server's /healthz endpoint.
HEALTHCHECK --interval=1m --timeout=5s --start-period=15s --retries=3 \
    CMD ["/usr/local/bin/podcastspeicher", "--health"]
ENTRYPOINT ["/usr/local/bin/podcastspeicher"]
