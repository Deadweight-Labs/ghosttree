# syntax=docker/dockerfile:1
#
# The ghosttree server as an image: one static ctx, a small base without a shell
# or a package manager, not run as root. Data lives in /data (a volume), the
# server key comes in as a secret (GHOSTTREE_SECRET_KEY_FILE). The release workflow
# builds it for amd64 and arm64 and pushes it to ghcr.io/deadweight-labs/ghosttree;
# compose.yaml (deploy/downloads/ghosttree/) runs it.
#
#   docker build --build-arg VERSION=0.1.0 -t ghosttree .

ARG GO_IMAGE=golang:1.26@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f
ARG BASE_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ctx ./cmd/ctx
# The data and backup directories, owned by the user the server runs as, so a
# new named volume starts out writable for it.
RUN mkdir -p /out/data /out/backups

FROM ${BASE_IMAGE}
COPY --from=build /out/ctx /usr/local/bin/ctx
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/backups /backups
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8474
ENTRYPOINT ["/usr/local/bin/ctx"]
CMD ["serve", "--db", "/data/ghosttree.db", "--listen", "0.0.0.0:8474"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/ctx", "server", "health"]
