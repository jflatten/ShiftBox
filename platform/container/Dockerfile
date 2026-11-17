# syntax=docker/dockerfile:1
# The exported browser assets are platform independent.
FROM --platform=$BUILDPLATFORM node:26.13.0-trixie-slim AS browser
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS controller
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY migrations/ ./migrations/
COPY ui/export.go ./ui/
COPY --from=browser /src/ui/out/ ./ui/out/
RUN CGO_ENABLED=0 go test -tags shiftbox_ui ./cmd/shiftbox -run '^TestEmbeddedBrowserAssets$' -count=1 \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -tags shiftbox_ui -o /release/shiftbox ./cmd/shiftbox

FROM ghcr.io/dexidp/dex:v2.46.1 AS dex

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates jq postgresql-client-17 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 shiftbox \
    && useradd --uid 10001 --gid shiftbox --home-dir /var/lib/shiftbox --shell /usr/sbin/nologin shiftbox \
    && install -d -m 0700 -o shiftbox -g shiftbox /var/lib/shiftbox
COPY --from=controller /release/shiftbox /usr/local/bin/shiftbox
# The controller discovers the managed Dex binary next to its own executable.
COPY --from=dex /usr/local/bin/dex /usr/local/bin/dex
COPY platform/container/cloud-start.sh /usr/local/bin/shiftbox-cloud
USER 10001:10001
WORKDIR /var/lib/shiftbox
ENTRYPOINT ["/usr/local/bin/shiftbox"]
CMD ["serve", "--config", "/var/lib/shiftbox/config.json"]
