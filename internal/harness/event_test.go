package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

// disable color in tests so we can match strings without ANSI noise.
func init() { useColor = false }

func TestEvent_ToolUseID(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "thinking..."},
				map[string]any{"type": "tool_use", "id": "tu_abc123", "name": "Bash", "input": map[string]any{}},
			},
		},
	}
	if got := ev.ToolUseID(); got != "tu_abc123" {
		t.Errorf("ToolUseID = %q, want tu_abc123", got)
	}
}

func TestEvent_ToolUseID_NoTool(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "just thinking"},
			},
		},
	}
	if got := ev.ToolUseID(); got != "" {
		t.Errorf("ToolUseID for text-only = %q, want empty", got)
	}
}

func TestEvent_IsTerminal(t *testing.T) {
	terminal := Event{Type: "result", Subtype: "success"}
	if !terminal.IsTerminal() {
		t.Error("result event should be terminal")
	}
	assistant := Event{Type: "assistant"}
	if assistant.IsTerminal() {
		t.Error("assistant event should not be terminal")
	}
}

func TestFormatEvent_ToolUse(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: map[string]any{
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": "git status"}},
			},
		},
	}
	got := FormatEvent(ev)
	if !strings.Contains(got, "Bash") || !strings.Contains(got, "git status") {
		t.Errorf("FormatEvent(tool_use) = %q; want mention of Bash and 'git status'", got)
	}
}

func TestFormatEvent_ToolResult(t *testing.T) {
	ev := Event{
		Type: "user",
		Message: map[string]any{
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1",
					"content": "line1\nline2\nline3"},
			},
		},
	}
	got := FormatEvent(ev)
	if !strings.Contains(got, "3 lines") {
		t.Errorf("FormatEvent(tool_result) = %q; want line count", got)
	}
}

func TestFormatEvent_AssistantText(t *testing.T) {
	ev := Event{
		Type: "assistant",
		Message: map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": "Reading the issue carefully.\nMore detail follows."},
			},
		},
	}
	got := FormatEvent(ev)
	if !strings.Contains(got, "Reading the issue") {
		t.Errorf("FormatEvent(text) = %q; want first line", got)
	}
	if strings.Contains(got, "More detail") {
		t.Errorf("FormatEvent(text) should show only first line; got %q", got)
	}
}

func TestFormatEvent_SystemInitSuppressed(t *testing.T) {
	ev := Event{Type: "system", Subtype: "init"}
	if got := FormatEvent(ev); got != "" {
		t.Errorf("system:init should be suppressed; got %q", got)
	}
}

func TestFormatEvent_RawPassthrough(t *testing.T) {
	ev := Event{Raw: "plain text from codex\n"}
	if got := FormatEvent(ev); got != "plain text from codex" {
		t.Errorf("raw passthrough: got %q", got)
	}
}

func TestSynthesizeToolResult_WithID(t *testing.T) {
	out := SynthesizeToolResult("yes proceed", "tu_42")
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type       string `json:"type"`
				ToolUseID  string `json:"tool_use_id"`
				Content    string `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(out, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "user" {
		t.Errorf("Type = %q", ev.Type)
	}
	if len(ev.Message.Content) != 1 {
		t.Fatalf("Content length = %d", len(ev.Message.Content))
	}
	c := ev.Message.Content[0]
	if c.Type != "tool_result" || c.ToolUseID != "tu_42" || c.Content != "yes proceed" {
		t.Errorf("content block wrong: %+v", c)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Error("output must end with newline for stream-json line-delimited write")
	}
}

func TestSynthesizeToolResult_WithoutID_PlainUserMessage(t *testing.T) {
	out := SynthesizeToolResult("just continue", "")
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(out, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "user" {
		t.Errorf("Type = %q", ev.Type)
	}
	if ev.Message.Content != "just continue" {
		t.Errorf("Content = %q", ev.Message.Content)
	}
}

func TestRingBuffer_CapsAt(t *testing.T) {
	r := newRingBuffer(3)
	for i := 0; i < 10; i++ {
		r.push(Event{Type: "assistant", Message: map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "msg"}},
		}})
	}
	if len(r.items) != 3 {
		t.Errorf("ringBuffer should cap at 3; len=%d", len(r.items))
	}
}

func TestRingBuffer_TailRespectsCharLimit(t *testing.T) {
	r := newRingBuffer(100)
	for i := 0; i < 50; i++ {
		r.push(Event{Raw: "0123456789"})
	}
	got := r.tail(50)
	if len(got) > 50 {
		t.Errorf("tail(50) returned %d chars", len(got))
	}
}
