# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0
#
# Release image for goliash-agent, built by goreleaser from the prebuilt binary.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/goliash-agent /usr/local/bin/goliash-agent
ENV GOLIASH_DATA_DIR=/data
VOLUME /data
WORKDIR /data
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/goliash-agent"]
