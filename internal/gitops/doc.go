// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package gitops opens pull requests in GitOps repositories: an upgrade as a change to the file that pins the
// image's tag, for the people who own the repository to review and merge. It writes nothing else.
package gitops
