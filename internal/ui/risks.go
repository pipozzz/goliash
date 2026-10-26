// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// RiskView is an accepted risk on the Security page: an acknowledgement, waiting for approval or in force.
type RiskView struct {
	ID, Service, Env, Kind, Reason string
	By, ApprovedBy, Until, Created string
	Pending                        bool
}

// acceptedRisks are the acknowledgements waiting for an admin's approval, then those in force.
func (s *Server) acceptedRisks(ctx context.Context, sc store.Scope, now time.Time) ([]RiskView, error) {
	pending, err := s.store.ListPendingAcks(ctx, sc)
	if err != nil {
		return nil, err
	}
	approved, err := s.store.ListAcks(ctx, sc)
	if err != nil {
		return nil, err
	}
	svcs, err := s.store.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, svc := range svcs {
		names[svc.ID] = svc.Name
	}
	for _, e := range envs {
		names[e.ID] = e.Name
	}
	var out []RiskView
	for _, a := range append(pending, approved...) {
		if !a.UntilAt.IsZero() && !a.UntilAt.After(now) {
			continue
		}
		out = append(out, RiskView{
			ID: a.ID, Service: names[a.ServiceID], Env: names[a.EnvironmentID], Kind: ackKindText(a.Kind), Reason: a.Reason,
			By: a.CreatedBy, ApprovedBy: a.ApprovedBy, Until: ackUntilText(a), Created: a.CreatedAt.Format("2 Jan 2006"), Pending: a.Pending,
		})
	}
	return out, nil
}

// PendingRisks counts the accepted risks waiting for approval.
func (v SecurityView) PendingRisks() int {
	n := 0
	for _, r := range v.Risks {
		if r.Pending {
			n++
		}
	}
	return n
}

func (s *Server) approveRisk(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	const page = "/security#risks"
	id := r.PathValue("id")
	if err := s.store.ApproveAck(r.Context(), p.Scope, id, p.Name()); errors.Is(err, store.ErrNotFound) {
		return back(w, r, page, "error", "It is no longer waiting for approval.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "ack.approve", "id", id)
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, page, "notice", "Approved: the risk is accepted and its notifications stay quiet.")
}

func (s *Server) rejectRisk(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	const page = "/security#risks"
	id := r.PathValue("id")
	if err := s.store.DeleteAck(r.Context(), p.Scope, id); errors.Is(err, store.ErrNotFound) {
		return back(w, r, page, "error", "It is already gone.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "ack.reject", "id", id)
	return back(w, r, page, "notice", "Turned down: the risk is not accepted.")
}
