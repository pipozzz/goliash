// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package docker collects containers on a standalone Docker host through a
// docker-socket-proxy that allows only GET requests. It also holds the read-only
// Docker API client the swarm collector uses.
package docker
