// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const outboxExt = ".json.gz"

// outbox keeps snapshots on disk until the server has them. Files are named by
// snapshot ULID, so their names sort in the order the snapshots were taken.
// When more than max snapshots wait, the oldest are dropped: every snapshot is a
// full state, so newer ones still describe the targets correctly.
type outbox struct {
	dir string
	max int

	mu     sync.Mutex
	notify chan struct{}
}

func newOutbox(dir string, maxSnapshots int) (*outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	return &outbox{dir: dir, max: maxSnapshots, notify: make(chan struct{}, 1)}, nil
}

// put stores a gzip-compressed snapshot and returns how many old snapshots were dropped.
func (o *outbox) put(snapshotID string, gz []byte) (dropped int, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	final := filepath.Join(o.dir, snapshotID+outboxExt)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, gz, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return 0, err
	}

	ids, err := o.listLocked()
	if err != nil {
		return 0, err
	}
	for len(ids) > o.max {
		if err := os.Remove(o.path(ids[0])); err != nil && !errors.Is(err, os.ErrNotExist) {
			return dropped, err
		}
		ids = ids[1:]
		dropped++
	}

	select {
	case o.notify <- struct{}{}:
	default:
	}
	return dropped, nil
}

// list returns the IDs of waiting snapshots, oldest first.
func (o *outbox) list() ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.listLocked()
}

func (o *outbox) listLocked() ([]string, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), outboxExt); ok && !e.IsDir() {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (o *outbox) read(id string) ([]byte, error) {
	return os.ReadFile(o.path(id))
}

func (o *outbox) remove(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	err := os.Remove(o.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (o *outbox) len() int {
	ids, err := o.list()
	if err != nil {
		return 0
	}
	return len(ids)
}

func (o *outbox) path(id string) string { return filepath.Join(o.dir, id+outboxExt) }
