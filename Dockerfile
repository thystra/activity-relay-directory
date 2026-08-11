# syntax=docker/dockerfile:1.7

FROM docker.io/library/golang:1.26.5-alpine3.24 AS build

ARG VERSION=development
ARG GO_BUILD_PARALLELISM=2
ARG GOMAXPROCS=2

ENV GOMAXPROCS=${GOMAXPROCS}

WORKDIR /src

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    mkdir -p /rootfs/usr/bin && \
    go build \
      -p "${GO_BUILD_PARALLELISM}" \
      -trimpath \
      -ldflags "-s -w -X github.com/thystra/activity-relay-directory/internal/buildinfo.Version=${VERSION}" \
      -o /rootfs/usr/bin/activity-relay-directory \
      ./cmd/activity-relay-directory

FROM public.ecr.aws/docker/library/alpine:3.22.1

RUN apk add --no-cache ca-certificates && \
    addgroup -S directory && \
    adduser -S -G directory -h /nonexistent -s /sbin/nologin directory && \
    mkdir -p /var/lib/activity-relay-directory && \
    chown directory:directory /var/lib/activity-relay-directory && \
    chmod 0700 /var/lib/activity-relay-directory

COPY --from=build \
  /rootfs/usr/bin/activity-relay-directory \
  /usr/bin/activity-relay-directory

COPY LICENCE /usr/share/licenses/activity-relay-directory/LICENCE

USER directory:directory

EXPOSE 8080

HEALTHCHECK \
  --interval=15s \
  --timeout=5s \
  --start-period=5s \
  --retries=5 \
  CMD wget --quiet --output-document=/dev/null http://127.0.0.1:8080/readyz || exit 1

ENTRYPOINT ["/usr/bin/activity-relay-directory"]
