# syntax=docker/dockerfile:1
# A named build context supplies the native archive. The empty fallback makes
# a missing context fail at COPY instead of resolving an external image.
FROM scratch AS macos-helper

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
ARG RELEASE_CANDIDATE=""
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY migrations/ ./migrations/
COPY LICENSE ./
COPY scripts/stage-controller-licenses.sh ./scripts/
COPY ui/export.go ./ui/
COPY --from=browser /src/ui/out/ ./ui/out/
COPY --from=macos-helper /ShiftBox-Local-Helper.dmg ./ui/out/_shiftbox/ShiftBox-Local-Helper.dmg
# Go test links this package under its import path while go build links main.
RUN set -eu; \
    if [ -n "$RELEASE_CANDIDATE" ]; then \
        case "$RELEASE_CANDIDATE" in \
            *[!a-zA-Z0-9.-]*) echo 'RELEASE_CANDIDATE must be a canonical RC tag.' >&2; exit 2 ;; \
        esac; \
        if [ "${#RELEASE_CANDIDATE}" -gt 96 ] || ! printf '%s\n' "$RELEASE_CANDIDATE" \
            | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'; then \
            echo 'RELEASE_CANDIDATE must be a canonical RC tag of at most 96 characters.' >&2; exit 2; \
        fi; \
    fi; \
    controller_release=${RELEASE_CANDIDATE:-development}; \
    test_ldflags="-X github.com/jflatten/ShiftBox/cmd/shiftbox.controllerRelease=$controller_release"; \
    SHIFTBOX_EXPECT_CONTROLLER_RELEASE="$controller_release" SHIFTBOX_REQUIRE_MACOS_HELPER=1 CGO_ENABLED=0 \
        go test -tags shiftbox_ui -ldflags "$test_ldflags" ./cmd/shiftbox \
        -run '^Test(EmbeddedBrowserAssets|EmbeddedMacOSHelper|ControllerReleaseMatchesBuild)$' -count=1; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
        go build -trimpath -tags shiftbox_ui -ldflags "-X main.controllerRelease=$controller_release" -o /release/shiftbox ./cmd/shiftbox; \
    sh scripts/stage-controller-licenses.sh /release/shiftbox /release/licenses

FROM ghcr.io/dexidp/dex:v2.46.1 AS dex

FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates jq postgresql-client-17 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 shiftbox \
    && useradd --uid 10001 --gid shiftbox --home-dir /var/lib/shiftbox --shell /usr/sbin/nologin shiftbox \
    && install -d -m 0700 -o shiftbox -g shiftbox /var/lib/shiftbox
COPY --from=controller /release/shiftbox /usr/local/bin/shiftbox
COPY --from=controller /release/licenses/ /usr/share/licenses/shiftbox/
# The controller discovers the managed Dex binary next to its own executable.
COPY --from=dex /usr/local/bin/dex /usr/local/bin/dex
COPY platform/container/cloud-start.sh /usr/local/bin/shiftbox-cloud
USER 10001:10001
WORKDIR /var/lib/shiftbox
ENTRYPOINT ["/usr/local/bin/shiftbox"]
CMD ["serve", "--config", "/var/lib/shiftbox/config.json"]
