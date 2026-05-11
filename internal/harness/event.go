// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Event is one stream-json event from the worker harness. Either it's
// a structured event (Type/Subtype/Message populated) or a raw line
// (Raw populated) — the latter handles codex's non-JSON output mode.
type Event struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype,omitempty"`
	Message map[string]any `json:"message,omitempty"`

	// Raw carries the original line for non-JSON output. When set, the
	// other fields are empty. We forward Raw events verbatim.
	Raw string `json:"-"`
}

// IsTerminal reports whether this event marks normal worker completion.
// stream-json emits a "result" event when the agent loop finishes.
func (e Event) IsTerminal() bool {
	return e.Type == "result"
}

// ToolUseID extracts the id of the latest tool_use block, if any.
// The supervisor keeps the most recent id around so it can key
// synthesized tool_result events back to the right call.
func (e Event) ToolUseID() string {
	if e.Type != "assistant" {
		return ""
	}
	content, ok := e.Message["content"].([]any)
	if !ok {
		return ""
	}
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_use" {
			if id, ok := block["id"].(string); ok {
				return id
			}
		}
	}
	return ""
}

// useColor caches whether stdout is a TTY (decided at package init).
// Skip ANSI escape codes when redirected to a file/pipe.
var useColor = func() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}()

func ansi(code, text string) string {
	if !useColor {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

// FormatEvent renders an Event as a single human-readable line.
// Returns empty string to suppress (e.g., the noisy system:init event).
//
// Format vocabulary (matches the bash judge.py output):
//
//	→ <tool>: <summary>     — tool call
//	← (N lines, M bytes)    — tool result
//	· <text>                 — assistant text / thinking
//	[done: <subtype>]        — terminal event
//	[system:<subtype>]       — non-init system event
func FormatEvent(e Event) string {
	if e.Raw != "" {
		return strings.TrimRight(e.Raw, "\n")
	}
	switch e.Type {
	case "system":
		if e.Subtype == "init" {
			return ""
		}
		return ansi("2", fmt.Sprintf("[system:%s]", e.Subtype))
	case "assistant":
		return formatAssistant(e.Message)
	case "user":
		return formatUser(e.Message)
	case "result":
		return ansi("32", fmt.Sprintf("[done: %s]", e.Subtype))
	}
	return ""
}

func formatAssistant(msg map[string]any) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			text, _ := block["text"].(string)
			text = strings.TrimSpace(text)
			if text == "" {
				return ""
			}
			first := strings.SplitN(text, "\n", 2)[0]
			return ansi("2", "· "+truncate(first, 200))
		case "tool_use":
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			arrow := ansi("33", "→")
			return fmt.Sprintf("%s %s: %s", arrow, name, summarizeToolInput(input))
		}
	}
	return ""
}

func formatUser(msg map[string]any) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_result" {
			body := fmt.Sprint(block["content"])
			lines := strings.Count(body, "\n") + 1
			if body == "" {
				lines = 0
			}
			return ansi("2", fmt.Sprintf("← (%d lines, %d bytes)", lines, len(body)))
		}
	}
	return ""
}

// summarizeToolInput pulls the most informative field out of a tool's
// input map. Keys checked in priority order — first hit wins.
func summarizeToolInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "url", "query", "pattern", "description"} {
		if v, ok := input[key].(string); ok && v != "" {
			first := strings.SplitN(v, "\n", 2)[0]
			return truncate(first, 120)
		}
	}
	b, _ := json.Marshal(input)
	return truncate(string(b), 120)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ringBuffer keeps the last N events, oldest first. Used by the
// Supervisor to feed the silence classifier a bounded context buffer
// without growing memory unboundedly over long runs.
type ringBuffer struct {
	items []Event
	cap   int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

func (r *ringBuffer) push(ev Event) {
	r.items = append(r.items, ev)
	if len(r.items) > r.cap {
		// Drop oldest. Allocation here is O(N) per push when full —
		// fine for N=200 and event rates measured in seconds.
		r.items = r.items[len(r.items)-r.cap:]
	}
}

// tail renders the buffer as a single string suitable for inclusion in
// a judge prompt, capped at charLimit bytes from the end.
func (r *ringBuffer) tail(charLimit int) string {
	var b strings.Builder
	for _, ev := range r.items {
		line := FormatEvent(ev)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	s := b.String()
	if len(s) > charLimit {
		return s[len(s)-charLimit:]
	}
	return s
}

// SynthesizeToolResult builds a stream-json user event carrying the
// answer text. If toolUseID is non-empty, the event is a tool_result
// keyed to that id (proper stream-json contract). Otherwise it's a
// plain user message — a fallback for harnesses that printed a
// free-text question rather than invoking a structured tool.
//
// Returns the JSON-encoded event terminated by a newline, ready to
// write to the worker's stdin.
func SynthesizeToolResult(answer, toolUseID string) []byte {
	var event map[string]any
	if toolUseID != "" {
		event = map[string]any{
			"type": "user",
			"message": map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": toolUseID,
					"content":     answer,
				}},
			},
		}
	} else {
		event = map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": answer,
			},
		}
	}
	b, _ := json.Marshal(event)
	return append(b, '\n')
}
