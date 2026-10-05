// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// bootstrapConfig is applied when the server starts, so a deployment comes up ready
// without manual steps (a Nomad pack, a Compose file, GitOps). It only creates what is
// missing; changes made later in the UI are kept.
type bootstrapConfig struct {
	// Owner is the e-mail of the first owner. While they have never signed in, every
	// start logs a fresh sign-in link.
	Owner        string `yaml:"owner"`
	Environments []struct {
		Name     string `yaml:"name"`
		Position int    `yaml:"position"`
	} `yaml:"environments"`
	Targets []struct {
		Name        string         `yaml:"name"`
		Environment string         `yaml:"environment"`
		Platform    string         `yaml:"platform"`
		Agent       string         `yaml:"agent"` // empty: the server collects it
		Poll        int            `yaml:"poll_interval_seconds"`
		Settings    map[string]any `yaml:"settings"`
	} `yaml:"targets"`
}

// loadBootstrap reads GOLIASH_BOOTSTRAP (YAML or JSON) or the file in
// GOLIASH_BOOTSTRAP_FILE. It returns nil when neither is set.
func loadBootstrap() (*bootstrapConfig, error) {
	raw := os.Getenv("GOLIASH_BOOTSTRAP")
	if path := os.Getenv("GOLIASH_BOOTSTRAP_FILE"); raw == "" && path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			return nil, fmt.Errorf("GOLIASH_BOOTSTRAP_FILE: %w", err)
		}
		raw = string(b)
	}
	if raw == "" {
		return nil, nil
	}
	var c bootstrapConfig
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("bootstrap configuration: %w", err)
	}
	return &c, nil
}

// applyBootstrap creates the environments, targets and first owner that do not exist yet.
func applyBootstrap(ctx context.Context, db *store.Store, ws store.Workspace, c *bootstrapConfig, publicURL string, log *slog.Logger) error {
	sc := ws.Scope()
	audit := func(action string, kv ...string) {
		details := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			details[kv[i]] = kv[i+1]
		}
		_ = db.Audit(ctx, store.AuditEntry{OrgID: ws.OrgID, WorkspaceID: ws.ID, Actor: "bootstrap", Action: action, Details: details})
	}

	for _, e := range c.Environments {
		if _, err := db.GetEnvironmentByName(ctx, sc, e.Name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := db.CreateEnvironment(ctx, sc, e.Name, e.Position); err != nil {
			return fmt.Errorf("environment %s: %w", e.Name, err)
		}
		audit("environment.create", "environment", e.Name)
		log.Info("bootstrap: environment created", "environment", e.Name)
	}

	existing, err := db.ListTargets(ctx, sc)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range existing {
		have[t.Name] = true
	}
	for _, t := range c.Targets {
		if have[t.Name] {
			continue
		}
		if !agentproto.Platform(t.Platform).Valid() {
			return fmt.Errorf("target %s: unknown platform %q", t.Name, t.Platform)
		}
		env, err := db.GetEnvironmentByName(ctx, sc, t.Environment)
		if err != nil {
			return fmt.Errorf("target %s: environment %q: %w", t.Name, t.Environment, err)
		}
		settings, err := json.Marshal(t.Settings)
		if err != nil || t.Settings == nil {
			settings = []byte(`{}`)
		}
		target := store.Target{Scope: sc, EnvironmentID: env.ID, Platform: t.Platform, Name: t.Name, Settings: settings, PollIntervalSeconds: t.Poll}
		if t.Agent != "" {
			a, err := db.GetAgentByName(ctx, sc, t.Agent)
			if err != nil {
				return fmt.Errorf("target %s: agent %q: %w", t.Name, t.Agent, err)
			}
			target.AgentID = a.ID
		}
		if _, err := db.CreateTarget(ctx, target); err != nil {
			return fmt.Errorf("target %s: %w", t.Name, err)
		}
		audit("target.create", "target", t.Name, "platform", t.Platform)
		log.Info("bootstrap: target created", "target", t.Name, "platform", t.Platform)
	}

	if c.Owner == "" {
		return nil
	}
	u, err := db.GetUserByEmail(ctx, ws.OrgID, c.Owner)
	if errors.Is(err, store.ErrNotFound) {
		n, err := db.CountUsers(ctx, ws.OrgID)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Warn("bootstrap: owner not created, the organization already has people", "owner", c.Owner)
			return nil
		}
		if u, err = db.CreateUser(ctx, ws.OrgID, c.Owner, "", store.RoleOwner); err != nil {
			return err
		}
		audit("user.create", "user", u.Email, "role", u.Role)
	} else if err != nil {
		return err
	}
	if !u.LastLoginAt.IsZero() {
		return nil
	}
	a, err := auth.New(db, log, publicURL, nil)
	if err != nil {
		return err
	}
	link, err := a.LoginLink(ctx, u)
	if err != nil {
		return err
	}
	log.Info("bootstrap: sign in as the owner with this link (works once, expires in 15 minutes; restart for a new one)",
		"owner", u.Email, "link", link)
	return nil
}

// recoveryLink gets a locked-out person back in where no shell is available (a
// container console, a platform without exec): while GOLIASH_RECOVERY_EMAIL is set,
// every start logs a one-time sign-in link for that person. With nobody in the
// organization yet, the person is created as its owner; otherwise they must exist,
// so the variable cannot add accounts. Whoever can set the environment and read the
// logs controls the server anyway.
func recoveryLink(ctx context.Context, db *store.Store, ws store.Workspace, email, publicURL string, log *slog.Logger) error {
	u, err := db.GetUserByEmail(ctx, ws.OrgID, email)
	if errors.Is(err, store.ErrNotFound) {
		n, cerr := db.CountUsers(ctx, ws.OrgID)
		if cerr != nil {
			return cerr
		}
		if n > 0 {
			log.Error("recovery: no such person; GOLIASH_RECOVERY_EMAIL must name someone who has an account", "email", email)
			return nil
		}
		if u, err = db.CreateUser(ctx, ws.OrgID, email, "", store.RoleOwner); err != nil {
			return err
		}
		log.Warn("recovery: the organization had nobody; created as owner", "email", u.Email)
	} else if err != nil {
		return err
	}
	a, err := auth.New(db, log, publicURL, nil)
	if err != nil {
		return err
	}
	link, err := a.RecoveryLink(ctx, u)
	if err != nil {
		return err
	}
	if err := db.Audit(ctx, store.AuditEntry{
		OrgID: ws.OrgID, WorkspaceID: ws.ID, Actor: "server", Action: "user.recovery_link",
		Details: map[string]string{"user": u.Email},
	}); err != nil {
		log.Error("audit log write failed", "err", err)
	}
	log.Warn("recovery: sign in with this link, set a password on your account page, then remove GOLIASH_RECOVERY_EMAIL "+
		"(the link works once and expires in 15 minutes; restart for a new one)", "email", u.Email, "link", link)
	return nil
}
