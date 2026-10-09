// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	spec "github.com/pipozzz/goliash/api"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Request size limits for the agent protocol.
const (
	maxSnapshotBody     = 10 << 20  // as sent, i.e. compressed
	maxSnapshotDecoded  = 100 << 20 // after decompression
	maxSmallRequestBody = 1 << 20
)

// AgentHandler serves the agent protocol under /agent/v1.
type AgentHandler struct {
	store  *store.Store
	ingest *ingest.Service
	log    *slog.Logger
	spec   *openapi3.T
	// clientIP names the caller for rate limiting enrollment.
	clientIP func(*http.Request) string
}

// SetClientIP sets how the caller's address is found (behind trusted proxies, from
// X-Forwarded-For). The default is the connection's address.
func (h *AgentHandler) SetClientIP(f func(*http.Request) string) { h.clientIP = f }

// NewAgentHandler returns the agent protocol handler. It fails if the embedded spec is invalid.
func NewAgentHandler(st *store.Store, svc *ingest.Service, log *slog.Logger) (*AgentHandler, error) {
	doc, err := openapi3.NewLoader().LoadFromData(spec.AgentV1)
	if err != nil {
		return nil, fmt.Errorf("load agent spec: %w", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("agent spec: %w", err)
	}
	return &AgentHandler{store: st, ingest: svc, log: log, spec: doc}, nil
}

// Register adds the agent routes to mux.
func (h *AgentHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /agent/v1/enroll", h.enroll)
	mux.Handle("POST /agent/v1/register", h.authed(maxSmallRequestBody, h.register))
	mux.Handle("GET /agent/v1/config", h.authed(0, h.config))
	mux.Handle("POST /agent/v1/snapshot", h.authed(maxSnapshotBody, h.snapshot))
	// Registry results carry the package lists of private images' SBOMs: as large as snapshots.
	mux.Handle("POST /agent/v1/registry-results", h.authed(maxSnapshotBody, h.registryResults))
	mux.Handle("POST /agent/v1/heartbeat", h.authed(maxSmallRequestBody, h.heartbeat))
	mux.HandleFunc("/agent/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "Not found", "unknown agent protocol endpoint")
	})
}

type agentHandlerFunc func(w http.ResponseWriter, r *http.Request, a store.Agent, body []byte)

// authed authenticates the agent token, reads and decompresses the body (up to maxBody
// bytes as sent), validates the request against the spec, and calls next.
func (h *AgentHandler) authed(maxBody int64, next agentHandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized",
				"no Authorization header arrived: set the agent token, and check that a proxy in front of the server passes the header on")
			return
		}
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || !tokens.Valid(token, tokens.Agent) {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized",
				"a valid glsh_agent_ bearer token is required; this one is not an agent token or was damaged when copied")
			return
		}
		agent, err := h.store.AgentByTokenHash(r.Context(), tokens.Hash(token))
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "unknown or revoked token")
			return
		}
		if err != nil {
			h.internalError(w, r, err)
			return
		}

		var body []byte
		if maxBody > 0 {
			body, err = readBody(w, r, maxBody)
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) || errors.Is(err, errDecodedTooLarge) {
					writeProblem(w, http.StatusRequestEntityTooLarge, "Request too large", err.Error())
					return
				}
				writeProblem(w, http.StatusBadRequest, "Bad request", err.Error())
				return
			}
		}
		if err := h.validate(r, body); err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid request", err.Error())
			return
		}
		next(w, r, agent, body)
	})
}

var errDecodedTooLarge = fmt.Errorf("decompressed body exceeds %d MiB", maxSnapshotDecoded>>20)

func readBody(w http.ResponseWriter, r *http.Request, maxBody int64) ([]byte, error) {
	var src io.Reader = http.MaxBytesReader(w, r.Body, maxBody)
	switch enc := strings.ToLower(r.Header.Get("Content-Encoding")); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		src = io.LimitReader(zr, maxSnapshotDecoded+1)
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	if len(body) > maxSnapshotDecoded {
		return nil, errDecodedTooLarge
	}
	return body, nil
}

// validate checks the request against the operation in the agent spec.
func (h *AgentHandler) validate(r *http.Request, body []byte) error {
	item := h.spec.Paths.Find(r.URL.Path)
	if item == nil {
		return fmt.Errorf("no spec for %s", r.URL.Path)
	}
	op := item.GetOperation(r.Method)
	if op == nil {
		return fmt.Errorf("no spec for %s %s", r.Method, r.URL.Path)
	}
	req := r.Clone(r.Context())
	req.Header.Del("Content-Encoding")
	req.Body = io.NopCloser(bytes.NewReader(body))
	return openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
		Request: req,
		Route:   &routers.Route{Spec: h.spec, Path: r.URL.Path, PathItem: item, Method: r.Method, Operation: op},
		Options: &openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, // done in authed
			MultiError:         false,
		},
	})
}

// Failed enrollments a client address may make in enrollWindow.
const (
	enrollFailures = 20
	enrollWindow   = 15 * time.Minute
)

// enroll needs no token: the code in the body is the credential. Failures are rate
// limited per client address.
func (h *AgentHandler) enroll(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if h.clientIP != nil {
		ip = h.clientIP(r)
	} else if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	key := tokens.Hash("enroll\x00" + ip)
	if n, err := h.store.SigninFailures(r.Context(), key, time.Now().Add(-enrollWindow)); err == nil && n >= enrollFailures {
		w.Header().Set("Retry-After", strconv.Itoa(int(enrollWindow/time.Second)))
		writeProblem(w, http.StatusTooManyRequests, "Too many requests", "too many failed enrollments from this address")
		return
	}
	body, err := readBody(w, r, maxSmallRequestBody)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad request", err.Error())
		return
	}
	if err := h.validate(r, body); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid request", err.Error())
		return
	}
	var req agentproto.EnrollRequest
	if !decode(w, body, &req) {
		return
	}
	resp, err := h.ingest.Enroll(r.Context(), req)
	if errors.Is(err, ingest.ErrBadCode) || errors.Is(err, ingest.ErrExpired) || errors.Is(err, ingest.ErrRevoked) ||
		errors.Is(err, ingest.ErrNoIdentity) {
		_ = h.store.AddSigninFailure(r.Context(), key)
		writeProblem(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AgentHandler) register(w http.ResponseWriter, r *http.Request, a store.Agent, body []byte) {
	var req agentproto.RegisterRequest
	if !decode(w, body, &req) {
		return
	}
	resp, err := h.ingest.Register(r.Context(), a, req)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AgentHandler) config(w http.ResponseWriter, r *http.Request, a store.Agent, _ []byte) {
	cfg, etag, err := h.ingest.Config(r.Context(), a)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (h *AgentHandler) snapshot(w http.ResponseWriter, r *http.Request, a store.Agent, body []byte) {
	var snap agentproto.Snapshot
	if !decode(w, body, &snap) {
		return
	}
	inserted, err := h.ingest.Snapshot(r.Context(), a, snap, body)
	if errors.Is(err, ingest.ErrUnknownTarget) {
		writeProblem(w, http.StatusNotFound, "Unknown target", err.Error())
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	status := agentproto.Accepted
	if !inserted {
		status = agentproto.Duplicate
	}
	writeJSON(w, http.StatusAccepted, agentproto.SnapshotAck{SnapshotID: snap.SnapshotID, Status: status})
}

func (h *AgentHandler) registryResults(w http.ResponseWriter, r *http.Request, a store.Agent, body []byte) {
	var res agentproto.RegistryResults
	if !decode(w, body, &res) {
		return
	}
	if err := h.ingest.RegistryResults(r.Context(), a, res); err != nil {
		h.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *AgentHandler) heartbeat(w http.ResponseWriter, r *http.Request, a store.Agent, body []byte) {
	var hb agentproto.Heartbeat
	if !decode(w, body, &hb) {
		return
	}
	resp, err := h.ingest.Heartbeat(r.Context(), a, hb)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *AgentHandler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "agent request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, http.StatusInternalServerError, "Internal server error", "")
}

func decode(w http.ResponseWriter, body []byte, v any) bool {
	if err := json.Unmarshal(body, v); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	p := agentproto.Problem{Title: title, Status: status}
	if detail != "" {
		p.Detail = &detail
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}
