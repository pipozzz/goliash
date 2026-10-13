// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// The OpenAI Responses API, streamed: what OpenAI's newer models need for tool calls. Goli keeps nothing at
// OpenAI (store: false) and sends the model's own output items back, encrypted reasoning included, so a model
// that thought before calling a tool goes on from there.

type rTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type rCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func rMessage(role, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"role": role, "content": text})
	return b
}

// respond streams one response: text goes to emit as it comes; the output items (to send back), the tool calls
// and how it ended come back.
func (a *Assistant) respond(ctx context.Context, instructions string, input []json.RawMessage, tools []rTool, emit func(Event)) ([]json.RawMessage, []rCall, string, error) {
	body := map[string]any{
		"model": a.cfg.Model, "instructions": instructions, "input": input, "stream": true,
		"store": false, "include": []string{"reasoning.encrypted_content"},
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if a.cfg.EffortSet {
		body["reasoning"] = map[string]string{"effort": a.cfg.Effort}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, nil, "", err
	}
	base := a.cfg.BaseURL
	if base == "" {
		base = openAIBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/responses", bytes.NewReader(b))
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if a.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, nil, "", &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(head))}
	}

	var items []json.RawMessage
	var calls []rCall
	finish := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Item     json.RawMessage `json:"item"`
			Message  string          `json:"message"`
			Response struct {
				Status            string `json:"status"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				emit(Event{Type: "text", Text: ev.Delta})
			}
		case "response.output_item.done":
			if len(ev.Item) == 0 {
				continue
			}
			items = append(items, ev.Item)
			var it struct {
				Type string `json:"type"`
				rCall
			}
			if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
				if it.Arguments == "" {
					it.Arguments = "{}"
				}
				calls = append(calls, it.rCall)
			}
		case "response.completed":
			finish = "completed"
		case "response.incomplete":
			finish = "incomplete"
			if d := ev.Response.IncompleteDetails; d != nil {
				finish = d.Reason // max_output_tokens or content_filter
			}
		case "response.failed":
			msg := "the response failed"
			if ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			return nil, nil, "", fmt.Errorf("model server: %s", msg)
		case "error":
			return nil, nil, "", fmt.Errorf("model server: %s", ev.Message)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, "", err
	}
	return items, calls, finish, nil
}

func rHistory(history []Turn) []json.RawMessage {
	var out []json.RawMessage
	for _, m := range oaHistory(history) {
		out = append(out, rMessage(m.Role, *m.Content))
	}
	return out
}

// askResponses is Ask through OpenAI's Responses API.
func (a *Assistant) askResponses(ctx context.Context, tools Tools, list []Tool, history []Turn, where Context, emit func(Event)) ([]string, error) {
	var rTools []rTool
	for _, t := range list {
		params := t.Schema
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		rTools = append(rTools, rTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	// The instructions stay the same for every question (prefix caching); where it is asked comes with the input.
	input := []json.RawMessage{rMessage("developer", whereLine(where))}
	input = append(input, rHistory(history)...)

	var used []string
	for range maxRounds {
		items, calls, finish, err := a.respond(ctx, systemPrompt, input, rTools, emit)
		if err != nil {
			return used, err
		}
		input = append(input, items...)
		switch {
		case len(calls) > 0:
			results := make([]json.RawMessage, len(calls))
			var wg sync.WaitGroup
			for i, call := range calls {
				used = append(used, call.Name)
				emit(Event{Type: "tool", Tool: call.Name})
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, err := tools.Call(ctx, call.Name, json.RawMessage(call.Arguments))
					if err != nil {
						out = "error: " + err.Error()
					} else {
						out = cutResult(out, toolLimit(where))
					}
					results[i], _ = json.Marshal(map[string]string{"type": "function_call_output", "call_id": call.CallID, "output": out})
				}()
			}
			wg.Wait()
			input = append(input, results...)
		case finish == "content_filter":
			emit(Event{Type: "error", Text: "Goli cannot help with that one."})
			return used, nil
		case finish == "max_output_tokens":
			emit(Event{Type: "text", Text: "\n\n…(the answer was cut short; ask about something narrower)"})
			emit(Event{Type: "done"})
			return used, nil
		default:
			emit(Event{Type: "done"})
			return used, nil
		}
	}
	emit(Event{Type: "error", Text: "Goli looked at too many things for one question; ask about something narrower."})
	return used, nil
}
