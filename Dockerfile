# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# The build stage runs on the build host's native platform and cross-compiles
# with GOOS/GOARCH; only the runtime stage below is per-target-platform.
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine3.23@sha256:5978cc992ad5ef96a7469713c8af849c1433824761ce3be2c56381403cd8d9a3 AS build
ENV GOTOOLCHAIN=local GOWORK=off
ARG BUILD_CACHE_NAMESPACE=default

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=oberth-server-${BUILD_CACHE_NAMESPACE}-gomod,target=/go/pkg/mod,sharing=locked go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
# VERSION feeds `oberth version` and the dashboard/status version display;
# release builds pass the exact tag, dev builds keep "dev".
ARG VERSION=dev
RUN --mount=type=cache,id=oberth-server-${BUILD_CACHE_NAMESPACE}-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=oberth-server-${BUILD_CACHE_NAMESPACE}-gobuild,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /out/oberth ./cmd/oberth

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN apk add --no-cache \
    ca-certificates=20260611-r0 \
    git=2.52.0-r0 \
    openssh-client-default=10.2_p1-r0 \
    tzdata=2026c-r0 \
    && rm -f /var/log/apk.log
COPY --from=build /out/oberth /usr/local/bin/oberth
ENV HOME=/tmp
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/oberth"]
CMD ["serve"]
