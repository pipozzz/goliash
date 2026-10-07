// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func TestEnrollmentCodes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		c, err := s.CreateEnrollmentCode(ctx, sc, f.env.ID, "code-hash", "ana@example.com", time.Time{}, false)
		if err != nil {
			t.Fatal(err)
		}
		k8s := agentproto.Target{Platform: agentproto.Kubernetes, Name: "prod-eu-1", Kubernetes: &agentproto.KubernetesSettings{}}
		got, err := s.Enroll(ctx, Enrollment{
			CodeHash: "code-hash", Identity: "kubernetes:abc", Name: "eu-cluster",
			TokenHash: "t1", Targets: []agentproto.Target{k8s},
		})
		if err != nil || !got.Created || got.Agent.Name != "eu-cluster-2" || len(got.Added) != 1 || got.Added[0].Name != "prod-eu-1-2" {
			t.Fatalf("enroll: %+v %v", got, err)
		}
		again, err := s.Enroll(ctx, Enrollment{
			CodeHash: "code-hash", Identity: "kubernetes:abc", Name: "x", TokenHash: "t2",
			Targets: []agentproto.Target{k8s},
		})
		if err != nil || again.Created || again.Agent.ID != got.Agent.ID || len(again.Added) != 0 {
			t.Fatalf("again: %+v %v", again, err)
		}
		if _, err := s.AgentByTokenHash(ctx, "t1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("old token works: %v", err)
		}
		if a, err := s.AgentByTokenHash(ctx, "t2"); err != nil || a.Identity != "kubernetes:abc" {
			t.Fatalf("new token: %+v %v", a, err)
		}
		codes, err := s.ListEnrollmentCodes(ctx, sc)
		if err != nil || len(codes) != 1 || codes[0].ID != c.ID || codes[0].Agents != 1 || codes[0].LastUsedAt.IsZero() ||
			!codes[0].ExpiresAt.IsZero() {
			t.Fatalf("codes: %+v %v", codes, err)
		}
		if _, err := s.Enroll(ctx, Enrollment{CodeHash: "nope", Identity: "x", Name: "x", TokenHash: "t3"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown code: %v", err)
		}
	})
}

func TestSingleEnrollmentCode(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		if _, err := s.CreateEnrollmentCode(ctx, sc, f.env.ID, "one", "", time.Now().Add(time.Hour), true); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateEnrollmentCode(ctx, sc, f.env.ID, "many", "", time.Time{}, false); err != nil {
			t.Fatal(err)
		}
		// No identity: fine for a single code, refused for a code for many.
		first, err := s.Enroll(ctx, Enrollment{CodeHash: "one", Name: "web-01", TokenHash: "a"})
		if err != nil || !first.Created || first.Agent.Identity != "" {
			t.Fatalf("single: %+v %v", first, err)
		}
		if _, err := s.Enroll(ctx, Enrollment{CodeHash: "many", Name: "x", TokenHash: "b"}); !errors.Is(err, ErrNoIdentity) {
			t.Fatalf("many without identity: %v", err)
		}
		// Whatever identity comes with the single code later gets the same agent.
		again, err := s.Enroll(ctx, Enrollment{CodeHash: "one", Identity: "docker:OTHER", Name: "y", TokenHash: "c"})
		if err != nil || again.Created || again.Agent.ID != first.Agent.ID {
			t.Fatalf("single again: %+v %v", again, err)
		}
		// Single codes keep identities out of the index, so a code for many can
		// still enroll the same installation as an agent of its own.
		other, err := s.Enroll(ctx, Enrollment{CodeHash: "many", Identity: "docker:OTHER", Name: "y", TokenHash: "d"})
		if err != nil || !other.Created || other.Agent.ID == first.Agent.ID {
			t.Fatalf("many: %+v %v", other, err)
		}
		codes, _ := s.ListEnrollmentCodes(ctx, sc)
		if len(codes) != 2 || codes[0].Single == codes[1].Single {
			t.Fatalf("codes: %+v", codes)
		}
	})
}
