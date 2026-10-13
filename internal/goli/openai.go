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
	"sort"
	"strings"
	"sync"
)

// The OpenAI-compatible Chat Completions API, streamed. Many servers speak it (OpenAI, Ollama, vLLM, LiteLLM,
// Groq, OpenRouter, …), so Goli asks only for what they share: messages, function tools and streaming.

const openAIBaseURL = "https://api.openai.com/v1"

// StatusError is an HTTP error from a model server; Describe turns it into words.
type StatusError struct {
	Status int
	Body   string // the start of the answer, for the server log only
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("model server answered %d: %s", e.Status, e.Body)
}

type oaFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaCall struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Function oaFunction `json:"function"`
}

type oaMessage struct {
	Role       string   `json:"role"`
	Content    *string  `json:"content"` // null for an assistant turn that only calls tools
	ToolCalls  []oaCall `json:"tool_calls,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
}

func oaText(role, text string) oaMessage { return oaMessage{Role: role, Content: &text} }

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// chat streams one completion: text goes to emit as it comes; the assistant message (with its tool calls) and why
// it stopped come back.
func (a *Assistant) chat(ctx context.Context, msgs []oaMessage, tools []oaTool, emit func(Event)) (oaMessage, string, error) {
	body := map[string]any{"model": a.cfg.Model, "messages": msgs, "stream": true}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if a.cfg.EffortSet {
		body["reasoning_effort"] = a.cfg.Effort
	}
	b, err := json.Marshal(body)
	if err != nil {
		return oaMessage{}, "", err
	}
	base := a.cfg.BaseURL
	if base == "" {
		base = openAIBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return oaMessage{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if a.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return oaMessage{}, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return oaMessage{}, "", &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(head))}
	}

	var text strings.Builder
	calls := map[int]*oaCall{}
	finish := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int        `json:"index"`
						ID       string     `json:"id"`
						Function oaFunction `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return oaMessage{}, "", fmt.Errorf("model server: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				emit(Event{Type: "text", Text: ch.Delta.Content})
			}
			for _, tc := range ch.Delta.ToolCalls {
				c := calls[tc.Index]
				if c == nil {
					c = &oaCall{Type: "function"}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				c.Function.Name += tc.Function.Name
				c.Function.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return oaMessage{}, "", err
	}
	reply := oaMessage{Role: "assistant"}
	if text.Len() > 0 || len(calls) == 0 {
		t := text.String()
		reply.Content = &t
	}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for n, i := range idx {
		c := *calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", n) // some servers leave it out; the result must name one
		}
		if c.Function.Arguments == "" {
			c.Function.Arguments = "{}"
		}
		reply.ToolCalls = append(reply.ToolCalls, c)
	}
	return reply, finish, nil
}

func oaHistory(history []Turn) []oaMessage {
	var out []oaMessage
	for _, t := range history {
		text := t.Text
		if len(text) > maxTurnText {
			text = text[:maxTurnText]
		}
		if strings.TrimSpace(text) == "" || (t.Role != "user" && t.Role != "assistant") {
			continue
		}
		out = append(out, oaText(t.Role, text))
	}
	return out
}

// askOpenAI is Ask through an OpenAI-compatible server.
func (a *Assistant) askOpenAI(ctx context.Context, tools Tools, list []Tool, history []Turn, where Context, emit func(Event)) ([]string, error) {
	var oaTools []oaTool
	for _, t := range list {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.Schema
		if ot.Function.Parameters == nil {
			ot.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		oaTools = append(oaTools, ot)
	}
	// Servers that cache prompts do so by prefix: the instructions come first, what varies after.
	msgs := []oaMessage{
		oaText("system", systemPrompt),
		oaText("system", whereLine(where)),
	}
	msgs = append(msgs, oaHistory(history)...)

	var used []string
	for range maxRounds {
		reply, finish, err := a.chat(ctx, msgs, oaTools, emit)
		if err != nil {
			return used, err
		}
		msgs = append(msgs, reply)
		switch {
		case len(reply.ToolCalls) > 0:
			results := make([]oaMessage, len(reply.ToolCalls))
			var wg sync.WaitGroup
			for i, call := range reply.ToolCalls {
				used = append(used, call.Function.Name)
				emit(Event{Type: "tool", Tool: call.Function.Name})
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, err := tools.Call(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
					if err != nil {
						out = "error: " + err.Error()
					} else {
						out = cutResult(out, toolLimit(where))
					}
					results[i] = oaText("tool", out)
					results[i].ToolCallID = call.ID
				}()
			}
			wg.Wait()
			msgs = append(msgs, results...)
		case finish == "content_filter":
			emit(Event{Type: "error", Text: "Goli cannot help with that one."})
			return used, nil
		case finish == "length":
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
