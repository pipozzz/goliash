// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agentproto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const specPath = "../../api/agent-v1.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	return doc
}

func TestSpecIsValid(t *testing.T) {
	loadSpec(t)
}

// Each testdata/<Schema>.json is an example message. It must validate against the
// schema of the same name and survive a round trip through the generated Go type
// without losing or inventing fields.
func TestExamples(t *testing.T) {
	doc := loadSpec(t)

	types := map[string]any{
		"RegisterRequest":   &RegisterRequest{},
		"RegisterResponse":  &RegisterResponse{},
		"AgentConfig":       &AgentConfig{},
		"Snapshot":          &Snapshot{},
		"SnapshotAck":       &SnapshotAck{},
		"RegistryResults":   &RegistryResults{},
		"Heartbeat":         &Heartbeat{},
		"HeartbeatResponse": &HeartbeatResponse{},
		"Problem":           &Problem{},
	}

	files, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(types) {
		t.Fatalf("found %d examples, want one per type (%d)", len(files), len(types))
	}

	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(file) //nolint:gosec // test fixture path from Glob
			if err != nil {
				t.Fatal(err)
			}

			schema, ok := doc.Components.Schemas[name]
			if !ok {
				t.Fatalf("no schema named %s", name)
			}
			var generic any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatal(err)
			}
			if err := schema.Value.VisitJSON(generic); err != nil {
				t.Fatalf("example does not match schema: %v", err)
			}

			typed, ok := types[name]
			if !ok {
				t.Fatalf("no Go type registered for %s", name)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(typed); err != nil {
				t.Fatalf("decode into %T: %v", typed, err)
			}
			out, err := json.Marshal(typed)
			if err != nil {
				t.Fatal(err)
			}
			var roundTripped any
			if err := json.Unmarshal(out, &roundTripped); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(generic, roundTripped) {
				t.Fatalf("round trip changed the message:\n in: %s\nout: %s", raw, out)
			}
		})
	}
}

func TestSchemaRejectsInvalidSnapshot(t *testing.T) {
	doc := loadSpec(t)
	schema := doc.Components.Schemas["Snapshot"].Value

	cases := map[string]string{
		"missing complete": `{"snapshot_id":"01J9ZQ5A1B2C3D4E5F6G7H8J9K","target_id":"01J9ZQ4M2N3P4Q5R6S7T8V9W0X","collected_at":"2026-10-02T08:05:00Z","workloads":[]}`,
		"bad ulid":         `{"snapshot_id":"not-a-ulid","target_id":"01J9ZQ4M2N3P4Q5R6S7T8V9W0X","collected_at":"2026-10-02T08:05:00Z","complete":true,"workloads":[]}`,
		"unknown kind":     `{"snapshot_id":"01J9ZQ5A1B2C3D4E5F6G7H8J9K","target_id":"01J9ZQ4M2N3P4Q5R6S7T8V9W0X","collected_at":"2026-10-02T08:05:00Z","complete":true,"workloads":[{"id":"x","kind":"pod","name":"x","containers":[]}]}`,
		"bad digest":       `{"snapshot_id":"01J9ZQ5A1B2C3D4E5F6G7H8J9K","target_id":"01J9ZQ4M2N3P4Q5R6S7T8V9W0X","collected_at":"2026-10-02T08:05:00Z","complete":true,"workloads":[{"id":"x","kind":"deployment","name":"x","containers":[{"name":"a","image":"a:1","digest":"md5:abc","running":1}]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(body), &v); err != nil {
				t.Fatal(err)
			}
			if err := schema.VisitJSON(v); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
