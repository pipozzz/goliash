// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

//go:build race

package versions

// raceDetector says the tests run with -race, whose instrumentation multiplies allocations.
const raceDetector = true
