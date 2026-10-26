// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package collectors

import (
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func TestDropIgnored(t *testing.T) {
	r := Result{Workloads: []agentproto.Workload{
		{Name: "a"}, {Name: "b", Labels: map[string]string{IgnoreLabel: "True"}}, {Name: "c", Labels: map[string]string{IgnoreLabel: "false"}},
	}}
	r.DropIgnored()
	if len(r.Workloads) != 2 || r.Workloads[0].Name != "a" || r.Workloads[1].Name != "c" {
		t.Errorf("kept %+v", r.Workloads)
	}
}
