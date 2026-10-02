# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Release image for goliash, built by goreleaser from the prebuilt binary.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/goliash /usr/local/bin/goliash
ENV GOLIASH_DATABASE_URL=/data/goliash.db GOLIASH_LISTEN=:8080
VOLUME /data
WORKDIR /data
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/goliash"]
