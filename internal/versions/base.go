// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// BaseStatus is what Goliash knows of a base image: its release cycle and when that cycle's support ends.
type BaseStatus struct {
	Image   string // as declared, e.g. docker.io/library/node:18-alpine
	Short   string // as people write it: node:18-alpine
	Product string // the endoflife.date product, when the site lists the image
	Cycle   string
	EOL     string // the end-of-life date of the cycle, when known
	Passed  bool   // support has ended
	Soon    bool   // support ends within eolWarning
}

// BaseImage is the image svc's own image is built on: set in its policy, else as the image declares.
func BaseImage(svc store.Service) string {
	if p, err := ParsePolicy(svc.VersionPolicy); err == nil && p.BaseImage != "" {
		if p.BaseImage == "none" {
			return ""
		}
		return p.BaseImage
	}
	return svc.BaseImage
}

// BaseStatus looks up the release cycle of a base image and its end of life. Without end-of-life data
// (or for an image endoflife.date does not list) it returns the image alone.
func (c *Checker) BaseStatus(ctx context.Context, image string, now time.Time) BaseStatus {
	ref := ParseImage(strings.SplitN(image, "@", 2)[0])
	st := BaseStatus{Image: image, Short: strings.TrimPrefix(strings.TrimPrefix(ref.Repo(), "docker.io/"), "library/")}
	if ref.Tag != "" {
		st.Short += ":" + ref.Tag
	}
	if c == nil || c.eol == nil || ref.Tag == "" {
		return st
	}
	product, err := c.eol.Product(ctx, ref.Repo())
	if err != nil || product == "" {
		return st
	}
	cycles, err := c.eol.Cycles(ctx, product)
	if err != nil {
		return st
	}
	cy, ok := CycleFor(ref.Tag, cycles)
	if !ok {
		return st
	}
	st.Product, st.Cycle = product, cy.Name
	if !cy.EOLFrom.IsZero() {
		st.EOL = cy.EOLFrom.Format(time.DateOnly)
	}
	st.Passed = cy.IsEOL || (!cy.EOLFrom.IsZero() && !cy.EOLFrom.After(now))
	st.Soon = !st.Passed && !cy.EOLFrom.IsZero() && cy.EOLFrom.Sub(now) <= eolWarning
	return st
}
