// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package pkgscan

import (
	"errors"
	"io"
)

// ErrTooLarge means a file is larger than it may be read.
var ErrTooLarge = errors.New("larger than its limit")

// readAll reads r up to limit bytes.
func readAll(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}

// ReadAll reads r up to limit bytes, for files Wants wanted.
func ReadAll(r io.Reader, limit int64) ([]byte, error) { return readAll(r, limit) }
