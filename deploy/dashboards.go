// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package deploy carries the ready-made dashboards and alerts, so a Goliash server can hand them out.
package deploy

import _ "embed"

// GrafanaDashboard is the Grafana dashboard for Goliash /metrics.
//
//go:embed grafana/goliash-dashboard.json
var GrafanaDashboard []byte

// SigNozDashboard is the SigNoz dashboard for Goliash /metrics.
//
//go:embed signoz/goliash-dashboard.json
var SigNozDashboard []byte

// PrometheusAlerts are the Prometheus alerting rules for Goliash /metrics.
//
//go:embed prometheus/goliash-alerts.yaml
var PrometheusAlerts []byte
