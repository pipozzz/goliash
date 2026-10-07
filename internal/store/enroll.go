// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Enrollment errors.
var (
	// ErrNoIdentity means an agent without an identity used a code for many agents.
	ErrNoIdentity = errors.New("no identity")
	// ErrCodeExpired means an expired code was used by an agent it does not know.
	ErrCodeExpired = errors.New("enrollment code expired")
	// ErrAgentRevoked means the agent that enrolled before was revoked; it has to be
	// deleted before it can enroll again.
	ErrAgentRevoked = errors.New("agent revoked")
)

// EnrollmentCode lets agents register themselves into an environment.
type EnrollmentCode struct {
	ID            string
	Scope         Scope
	EnvironmentID string
	CreatedBy     string
	CreatedAt     time.Time
	ExpiresAt     time.Time // zero: never
	RevokedAt     time.Time
	LastUsedAt    time.Time
	Agents        int // agents that enrolled with it
	// Single codes belong to the first agent that enrolls with them; others serve
	// many agents, told apart by identity.
	Single bool
}

// Expired reports whether the code no longer admits new agents.
func (c EnrollmentCode) Expired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
}

// CreateEnrollmentCode stores a code by its hash. A zero expiresAt never expires.
func (s *Store) CreateEnrollmentCode(ctx context.Context, sc Scope, environmentID, hash, createdBy string,
	expiresAt time.Time, single bool,
) (EnrollmentCode, error) {
	c := EnrollmentCode{
		ID: NewID(), Scope: sc, EnvironmentID: environmentID, CreatedBy: createdBy, CreatedAt: s.now(),
		ExpiresAt: expiresAt, Single: single,
	}
	var maxAgents sql.NullInt64
	if single {
		maxAgents = sql.NullInt64{Int64: 1, Valid: true}
	}
	var expires sql.NullTime
	if !expiresAt.IsZero() {
		expires = sql.NullTime{Time: expiresAt.UTC(), Valid: true}
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO enrollment_codes (id, org_id, workspace_id, environment_id, hash,
		created_by, created_at, expires_at, max_agents) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, sc.OrgID, sc.WorkspaceID, environmentID, hash, createdBy, c.CreatedAt, expires, maxAgents)
	return c, err
}

const codeColumns = `c.id, c.org_id, c.workspace_id, c.environment_id, c.created_by, c.created_at, c.expires_at,
	c.revoked_at, c.last_used_at, c.max_agents, (SELECT COUNT(*) FROM agents a WHERE a.enrollment_code_id = c.id)`

func scanCode(row scanner) (EnrollmentCode, error) {
	var (
		c                      EnrollmentCode
		expires, revoked, used sql.NullTime
		maxAgents              sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.Scope.OrgID, &c.Scope.WorkspaceID, &c.EnvironmentID, &c.CreatedBy, &c.CreatedAt,
		&expires, &revoked, &used, &maxAgents, &c.Agents); err != nil {
		return c, notFound(err)
	}
	c.CreatedAt, c.Single = c.CreatedAt.UTC(), maxAgents.Valid && maxAgents.Int64 == 1
	c.ExpiresAt, c.RevokedAt, c.LastUsedAt = timeOrZero(expires), timeOrZero(revoked), timeOrZero(used)
	return c, nil
}

// ListEnrollmentCodes returns the workspace's codes that were not revoked, newest first.
func (s *Store) ListEnrollmentCodes(ctx context.Context, sc Scope) ([]EnrollmentCode, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+codeColumns+` FROM enrollment_codes c
		WHERE c.org_id = ? AND c.workspace_id = ? AND c.revoked_at IS NULL ORDER BY c.created_at DESC, c.id DESC`,
		sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []EnrollmentCode
	for rows.Next() {
		c, err := scanCode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetEnrollmentCode returns a code of the workspace.
func (s *Store) GetEnrollmentCode(ctx context.Context, sc Scope, id string) (EnrollmentCode, error) {
	return scanCode(s.queryRow(ctx, s.db, `SELECT `+codeColumns+` FROM enrollment_codes c
		WHERE c.org_id = ? AND c.workspace_id = ? AND c.id = ?`, sc.OrgID, sc.WorkspaceID, id))
}

// RevokeEnrollmentCode stops a code from enrolling any agent. Agents that enrolled
// with it keep working until they restart.
func (s *Store) RevokeEnrollmentCode(ctx context.Context, sc Scope, id string) error {
	res, err := s.exec(ctx, s.db, `UPDATE enrollment_codes SET revoked_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ? AND revoked_at IS NULL`, s.now(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// Enrollment is what an agent sends to enroll.
type Enrollment struct {
	CodeHash  string
	Identity  string // may be empty for a single code
	Name      string // the agent's preferred name; a suffix is added when it is taken
	TokenHash string // the agent's new token
	Targets   []agentproto.Target
}

// Enrolled is the outcome of an enrollment.
type Enrolled struct {
	Agent   Agent
	Created bool     // a new agent; false when a known installation came back
	Added   []Target // targets created for it
}

// Enroll admits an agent with a code. A single code gives the agent that enrolled
// with it before back, whatever its identity; a code for many agents gives a known
// identity its agent back. Either way the agent gets a new token (its old tokens stop
// working) as long as the code is not revoked. Otherwise a new agent is created if the
// code has not expired. Targets are matched to
// the agent's by key: new ones are created in the code's environment, known ones
// take the agent's settings. Targets the agent no longer reports are kept.
func (s *Store) Enroll(ctx context.Context, e Enrollment) (Enrolled, error) {
	var out Enrolled
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		code, err := scanCode(s.queryRow(ctx, tx, `SELECT `+codeColumns+` FROM enrollment_codes c
			WHERE c.hash = ? AND c.revoked_at IS NULL`, e.CodeHash))
		if err != nil {
			return err
		}
		sc, now := code.Scope, s.now()
		identity := sql.NullString{String: e.Identity, Valid: e.Identity != "" && !code.Single}
		switch {
		case code.Single:
			out.Agent, _, err = s.scanAgent(s.queryRow(ctx, tx, `SELECT `+agentColumns+`, '' FROM agents a
				WHERE a.org_id = ? AND a.workspace_id = ? AND a.enrollment_code_id = ?`, sc.OrgID, sc.WorkspaceID, code.ID))
		case e.Identity == "":
			return ErrNoIdentity
		default:
			out.Agent, _, err = s.scanAgent(s.queryRow(ctx, tx, `SELECT `+agentColumns+`, '' FROM agents a
				WHERE a.org_id = ? AND a.workspace_id = ? AND a.identity = ?`, sc.OrgID, sc.WorkspaceID, e.Identity))
		}
		switch {
		case errors.Is(err, ErrNotFound):
			if code.Expired(now) {
				return ErrCodeExpired
			}
			name, err := s.freeName(ctx, tx, `SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND name = ?`,
				sc.WorkspaceID, e.Name)
			if err != nil {
				return err
			}
			out.Agent = Agent{ID: NewID(), Scope: sc, Name: name, Platforms: []string{}, CreatedAt: now, Identity: identity.String}
			out.Created = true
			if _, err := s.exec(ctx, tx, `INSERT INTO agents (id, org_id, workspace_id, name, platforms, created_at,
				identity, enrollment_code_id) VALUES (?, ?, ?, ?, '[]', ?, ?, ?)`,
				out.Agent.ID, sc.OrgID, sc.WorkspaceID, name, now, identity, code.ID); err != nil {
				return err
			}
		case err != nil:
			return err
		case out.Agent.ActiveTokens == 0 && !out.Agent.RegisteredAt.IsZero():
			return ErrAgentRevoked
		default:
			if _, err := s.exec(ctx, tx, `UPDATE tokens SET revoked_at = ? WHERE agent_id = ? AND kind = 'agent'
				AND revoked_at IS NULL`, now, out.Agent.ID); err != nil {
				return err
			}
		}
		if _, err := s.exec(ctx, tx, `INSERT INTO tokens (id, org_id, workspace_id, kind, name, hash, agent_id, created_at)
			VALUES (?, ?, ?, 'agent', ?, ?, ?, ?)`,
			NewID(), sc.OrgID, sc.WorkspaceID, out.Agent.Name, e.TokenHash, out.Agent.ID, now); err != nil {
			return err
		}
		out.Agent.ActiveTokens = 1
		if _, err := s.exec(ctx, tx, `UPDATE enrollment_codes SET last_used_at = ? WHERE id = ?`, now, code.ID); err != nil {
			return err
		}
		out.Added, err = s.syncAgentTargets(ctx, tx, out.Agent, code.EnvironmentID, e.Targets)
		return err
	})
	return out, err
}

// syncAgentTargets creates or updates the targets an agent reported.
func (s *Store) syncAgentTargets(ctx context.Context, tx *sql.Tx, a Agent, environmentID string,
	targets []agentproto.Target,
) ([]Target, error) {
	var added []Target
	for _, pt := range targets {
		key := string(pt.Platform) + ":" + pt.Name
		settings, err := targetSettings(pt)
		if err != nil {
			return nil, err
		}
		poll := pt.PollIntervalSeconds
		if poll < 30 {
			poll = 300
		}
		res, err := s.exec(ctx, tx, `UPDATE targets SET settings = ?, poll_interval_seconds = ?
			WHERE workspace_id = ? AND agent_id = ? AND agent_key = ?`,
			settings, poll, a.Scope.WorkspaceID, a.ID, key)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err != nil || n > 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		name, err := s.freeName(ctx, tx, `SELECT COUNT(*) FROM targets WHERE workspace_id = ? AND name = ?`,
			a.Scope.WorkspaceID, pt.Name)
		if err != nil {
			return nil, err
		}
		t := Target{
			ID: NewID(), Scope: a.Scope, EnvironmentID: environmentID, AgentID: a.ID, Platform: string(pt.Platform),
			Name: name, Settings: json.RawMessage(settings), PollIntervalSeconds: poll, CreatedAt: s.now(),
			AgentKey: key,
		}
		if _, err := s.exec(ctx, tx, `
			INSERT INTO targets (id, org_id, workspace_id, environment_id, agent_id, platform, name, settings,
				poll_interval_seconds, created_at, agent_key)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Scope.OrgID, t.Scope.WorkspaceID, t.EnvironmentID, t.AgentID, t.Platform, t.Name, settings,
			t.PollIntervalSeconds, t.CreatedAt, key); err != nil {
			return nil, err
		}
		added = append(added, t)
	}
	return added, nil
}

// targetSettings is how a protocol target is stored: everything but what the target
// row holds in its own columns.
func targetSettings(pt agentproto.Target) (string, error) {
	if !pt.Platform.Valid() {
		return "", fmt.Errorf("unknown platform %q", pt.Platform)
	}
	pt.ID, pt.Name, pt.Platform, pt.PollIntervalSeconds = "", "", "", 0
	b, err := json.Marshal(pt)
	if err != nil {
		return "", err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	for _, k := range []string{"id", "name", "platform", "poll_interval_seconds"} {
		delete(m, k)
	}
	b, err = json.Marshal(m)
	return string(b), err
}

// freeName returns name, or name-2, name-3… when count (a query counting rows with
// workspace and name as its two arguments) finds it taken.
func (s *Store) freeName(ctx context.Context, tx *sql.Tx, count, workspaceID, name string) (string, error) {
	for i := 1; ; i++ {
		try := name
		if i > 1 {
			try = fmt.Sprintf("%s-%d", name, i)
		}
		var n int
		if err := s.queryRow(ctx, tx, count, workspaceID, try).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return try, nil
		}
	}
}
