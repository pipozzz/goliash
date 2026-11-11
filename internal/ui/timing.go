// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// slowRequest is how long a request takes before it is logged.
const slowRequest = time.Second

// Timing says how long the server took for each answer, in a Server-Timing header (the browser's developer tools
// show it beside the network's time), and logs requests slower than slowRequest: what is slow on a server is then
// in its log, not guessed at. Event streams are left out, they stay open.
func Timing(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Accept"), "text/event-stream") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		tw := &timingWriter{ResponseWriter: w, start: start}
		next.ServeHTTP(tw, r)
		if d := time.Since(start); d > slowRequest && log != nil {
			log.Warn("slow request", "method", r.Method, "path", r.URL.Path, "status", tw.status, "ms", d.Milliseconds())
		}
	})
}

type timingWriter struct {
	http.ResponseWriter
	start  time.Time
	status int
}

func (w *timingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		ms := float64(time.Since(w.start).Microseconds()) / 1000
		w.Header().Set("Server-Timing", "app;dur="+strconv.FormatFloat(ms, 'f', 1, 64))
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *timingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *timingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *timingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
