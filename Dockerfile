# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Builds from source (the quickstart uses this):
#   docker build --target server -t goliash .
#   docker build --target agent  -t goliash-agent .
# Release images are built by goreleaser from build/*.Dockerfile instead.

FROM golang:1.26 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN for b in goliash goliash-agent; do \
      CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X github.com/pipozzz/goliash/pkg/buildinfo.Version=${VERSION}" \
        -o /out/$b ./cmd/$b || exit 1; \
    done

FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/goliash-agent /usr/local/bin/goliash-agent
ENV GOLIASH_DATA_DIR=/data
VOLUME /data
WORKDIR /data
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/goliash-agent"]

FROM gcr.io/distroless/static-debian12:nonroot AS server
COPY --from=build /out/goliash /usr/local/bin/goliash
# SQLite lives in /data; set GOLIASH_DATABASE_URL=postgres://… to use PostgreSQL.
ENV GOLIASH_DATABASE_URL=/data/goliash.db GOLIASH_LISTEN=:8080
VOLUME /data
WORKDIR /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/goliash"]
