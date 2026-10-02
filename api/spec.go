// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package api embeds the OpenAPI specifications so the server can validate
// requests against them and serve them.
package api

import _ "embed"

// AgentV1 is the agent protocol specification (agent-v1.yaml).
//
//go:embed agent-v1.yaml
var AgentV1 []byte
