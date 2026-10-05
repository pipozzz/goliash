// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package collectors

import (
	"context"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Result is the state of one target at one point in time.
type Result struct {
	Workloads []agentproto.Workload
	// Complete is false when part of the target could not be read (for example a
	// forbidden namespace). The server then does not treat missing workloads as removed.
	Complete bool
	// Errors explains what made the result incomplete.
	Errors []string
}

// Collector reads the full current state of one target. Implementations must only
// read: they never create, change or delete anything.
type Collector interface {
	Collect(ctx context.Context) (Result, error)
}

// Watcher is implemented by collectors that can tell when their target changed
// (Kubernetes informers), so the agent collects soon after a change instead of
// waiting for the next poll. Watch blocks until ctx ends and calls changed after
// every observed change.
type Watcher interface {
	Watch(ctx context.Context, changed func()) error
}

// KeychainSource is implemented by collectors that know registry credentials of
// their target (Kubernetes image pull secrets). The agent uses them to check private
// registries when no credential is configured for the registry host.
type KeychainSource interface {
	RegistryKeychain(ctx context.Context) (registry.Keychain, error)
}

// Factory builds a collector for a target from its configuration. Credentials are
// resolved locally from target.CredentialsRef; they never come from the server.
type Factory func(ctx context.Context, target agentproto.Target) (Collector, error)
