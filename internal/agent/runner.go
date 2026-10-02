// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// runner collects one target on its poll interval and, for watching collectors,
// shortly after each change.
type runner struct {
	target    agentproto.Target
	collector collectors.Collector
	emit      func(agentproto.Target, collectors.Result) (snapshotID string, err error)
	log       *slog.Logger

	mu     sync.Mutex
	status agentproto.CollectorStatus
}

func newRunner(t agentproto.Target, c collectors.Collector, emit func(agentproto.Target, collectors.Result) (string, error), log *slog.Logger) *runner {
	return &runner{
		target:    t,
		collector: c,
		emit:      emit,
		log:       log.With("target", t.Name, "platform", t.Platform),
		status:    agentproto.CollectorStatus{TargetID: t.ID, Status: agentproto.Ok},
	}
}

// failingRunner reports a target the agent cannot collect (no collector for its
// platform, or the collector could not be built).
func failingRunner(t agentproto.Target, reason string) *runner {
	return &runner{
		target: t,
		status: agentproto.CollectorStatus{TargetID: t.ID, Status: agentproto.Failing, LastError: &reason},
	}
}

func (r *runner) run(ctx context.Context) {
	if r.collector == nil {
		<-ctx.Done()
		return
	}

	poll := time.Duration(r.target.PollIntervalSeconds) * time.Second
	if poll <= 0 {
		poll = 5 * time.Minute
	}
	changed := make(chan struct{}, 1)
	if w, ok := r.collector.(collectors.Watcher); ok {
		go func() {
			err := w.Watch(ctx, func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})
			if err != nil && ctx.Err() == nil {
				r.log.Warn("watch stopped; falling back to polling", "err", err)
			}
		}()
	}
	debounce := 30 * time.Second
	if r.target.DebounceSeconds != nil {
		debounce = time.Duration(*r.target.DebounceSeconds) * time.Second
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var debounceC <-chan time.Time
	var debounceT *time.Timer

	r.collect(ctx)
	for {
		select {
		case <-ctx.Done():
			if debounceT != nil {
				debounceT.Stop()
			}
			return
		case <-ticker.C:
			r.collect(ctx)
		case <-changed:
			// Restart the debounce window on every change; collect once things settle.
			if debounceT != nil {
				debounceT.Stop()
			}
			debounceT = time.NewTimer(debounce)
			debounceC = debounceT.C
		case <-debounceC:
			debounceC = nil
			r.collect(ctx)
			ticker.Reset(poll)
		}
	}
}

func (r *runner) collect(ctx context.Context) {
	res, err := r.collector.Collect(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.log.Warn("collect failed", "err", err)
		r.setStatus(agentproto.Failing, err.Error(), "")
		return
	}
	id, err := r.emit(r.target, res)
	if err != nil {
		r.log.Error("snapshot not stored", "err", err)
		r.setStatus(agentproto.Failing, err.Error(), "")
		return
	}
	if res.Complete {
		r.setStatus(agentproto.Ok, "", id)
	} else {
		msg := "incomplete snapshot"
		if len(res.Errors) > 0 {
			msg = res.Errors[0]
		}
		r.setStatus(agentproto.Degraded, msg, id)
	}
	r.log.Debug("collected", "workloads", len(res.Workloads), "complete", res.Complete, "snapshot_id", id)
}

func (r *runner) setStatus(st agentproto.CollectorStatusStatus, lastError, snapshotID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Status = st
	r.status.LastError = nil
	if lastError != "" {
		r.status.LastError = &lastError
	}
	if snapshotID != "" {
		now := time.Now().UTC()
		r.status.LastSuccessAt = &now
		r.status.LastSnapshotID = &snapshotID
	}
}

func (r *runner) currentStatus() agentproto.CollectorStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}
