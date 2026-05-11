// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package judge

import "testing"

type sample struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func TestExtractFirstJSON_Plain(t *testing.T) {
	var s sample
	if err := ExtractFirstJSON(`{"state":"working","reason":"thinking"}`, &s); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.State != "working" || s.Reason != "thinking" {
		t.Errorf("got %+v", s)
	}
}

func TestExtractFirstJSON_CodeFenced(t *testing.T) {
	raw := "Here's my answer:\n```json\n{\"state\":\"waiting\",\"reason\":\"q\"}\n```\nDone."
	var s sample
	if err := ExtractFirstJSON(raw, &s); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.State != "waiting" {
		t.Errorf("got state %q", s.State)
	}
}

func TestExtractFirstJSON_NestedBraces(t *testing.T) {
	// Matters because the parser tracks depth, not just first '{' to first '}'.
	var s struct {
		Q       string         `json:"q"`
		Options map[string]any `json:"options"`
	}
	raw := `{"q":"do the thing","options":{"a":1,"b":{"c":2}}}`
	if err := ExtractFirstJSON(raw, &s); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.Q != "do the thing" {
		t.Errorf("got %+v", s)
	}
}

func TestExtractFirstJSON_PreAndPostChatter(t *testing.T) {
	var s sample
	raw := `Sure thing! My verdict is: {"state":"errored","reason":"panic"} -- hope that helps`
	if err := ExtractFirstJSON(raw, &s); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.State != "errored" {
		t.Errorf("got state %q", s.State)
	}
}

func TestExtractFirstJSON_NoJSON_Errors(t *testing.T) {
	var s sample
	if err := ExtractFirstJSON("nothing structured here", &s); err == nil {
		t.Fatal("expected err for no-JSON input")
	}
}

func TestExtractFirstJSON_UnbalancedBraces_Errors(t *testing.T) {
	var s sample
	if err := ExtractFirstJSON(`{"state": "waiting"`, &s); err == nil {
		t.Fatal("expected err for unbalanced braces")
	}
}

func TestExtractFirstJSON_MalformedJSON_Errors(t *testing.T) {
	var s sample
	// braces balanced but contents not valid JSON
	if err := ExtractFirstJSON(`{this is { not } json}`, &s); err == nil {
		t.Fatal("expected err for malformed JSON")
	}
}

// First-balanced-object semantics: if two objects appear, we take the
// first complete one. (The judge prompts ask for ONE object — this
// makes the parser predictable when models occasionally emit two.)
func TestExtractFirstJSON_FirstWins(t *testing.T) {
	var s sample
	raw := `{"state":"working","reason":"a"} also {"state":"waiting","reason":"b"}`
	if err := ExtractFirstJSON(raw, &s); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s.State != "working" {
		t.Errorf("expected first object's state, got %q", s.State)
	}
}
