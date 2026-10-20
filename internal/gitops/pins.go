// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"fmt"

	"github.com/pipozzz/goliash/internal/versions"
)

// Pin is one image's change in a pull request: where it is pinned, and from which tag to which.
type Pin struct {
	Path, Image, From, To string
}

// Pins are the changes a bump makes: its own, then the others it carries (a promotion of a whole application).
func (b Bump) Pins() []Pin {
	return append([]Pin{{Path: b.Path, Image: b.Image, From: b.From, To: b.To}}, b.Also...)
}

// at is the bump narrowed to one pin's path, for reading the files under it.
func (b Bump) at(p Pin) Bump {
	b.Path, b.Image, b.From, b.To = p.Path, p.Image, p.From, p.To
	return b
}

// edit is a file changed by one or more pins.
type edit struct {
	path, sha string
	old, new  []byte
}

// edits gathers the changes of several pins, in the order files were first changed; a file two pins change is
// changed twice, in turn.
type edits struct {
	order []string
	files map[string]*edit
}

// apply changes one file for one pin; it says whether the pin found the image there by digest only.
func (e *edits) apply(path, sha string, raw []byte, p Pin) (changed, pinned bool) {
	if e.files == nil {
		e.files = map[string]*edit{}
	}
	cur := raw
	if f, ok := e.files[path]; ok {
		cur = f.new
	}
	res := versions.BumpImageTag(cur, p.Image, p.From, p.To)
	if res.Changes == 0 {
		return false, res.Pinned
	}
	if f, ok := e.files[path]; ok {
		f.new = res.Content
	} else {
		e.files[path] = &edit{path: path, sha: sha, old: raw, new: res.Content}
		e.order = append(e.order, path)
	}
	return true, res.Pinned
}

// list is the changed files, in order.
func (e *edits) list() []*edit {
	out := make([]*edit, 0, len(e.order))
	for _, p := range e.order {
		out = append(out, e.files[p])
	}
	return out
}

// pinErr says why a pin changed nothing: pinned by digest, or not pinned at that version. With several pins it
// names the image.
func pinErr(b Bump, p Pin, pinned bool) error {
	err := ErrNoTag
	if pinned {
		err = ErrPinned
	}
	if len(b.Also) == 0 {
		return err
	}
	return fmt.Errorf("%s %s: %w", p.Image, p.From, err)
}
