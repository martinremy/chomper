package gh

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseHost(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/owner/repo.git", "github.com"},
		{"https://github.com/owner/repo", "github.com"},
		{"http://gh.internal.example.com/o/r.git", "gh.internal.example.com"},
		{"git@github.com:owner/repo.git", "github.com"},
		{"git@github.enterprise.example.com:owner/repo.git", "github.enterprise.example.com"},
		{"", "github.com"},                          // empty -> default
		{"ssh://something/weird", "github.com"},     // unparseable -> default
		{"https://", "github.com"},                  // truncated -> default
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := parseHost(tt.url)
			if got != tt.want {
				t.Errorf("parseHost(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestFilter_NoFilters_ReturnsAllSorted(t *testing.T) {
	issues := []Issue{
		{Number: 7, Title: "g"},
		{Number: 3, Title: "c"},
		{Number: 9, Title: "i"},
	}
	got := Filter(issues, nil, "")
	want := []int{3, 7, 9}
	if !reflect.DeepEqual(numbers(got), want) {
		t.Errorf("got %v, want %v", numbers(got), want)
	}
}

func TestFilter_TitleMatch_CaseInsensitive(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "Fix the bug"},
		{Number: 2, Title: "Add feature"},
		{Number: 3, Title: "FIX another thing"},
	}
	got := Filter(issues, nil, "fix")
	want := []int{1, 3}
	if !reflect.DeepEqual(numbers(got), want) {
		t.Errorf("title filter: got %v, want %v", numbers(got), want)
	}
}

func TestFilter_LabelOR(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "a", Labels: []Label{{Name: "bug"}}},
		{Number: 2, Title: "b", Labels: []Label{{Name: "enhancement"}}},
		{Number: 3, Title: "c", Labels: []Label{{Name: "bug"}, {Name: "p1"}}},
		{Number: 4, Title: "d", Labels: nil},
	}
	got := Filter(issues, []string{"bug", "p0"}, "")
	want := []int{1, 3}
	if !reflect.DeepEqual(numbers(got), want) {
		t.Errorf("label filter: got %v, want %v", numbers(got), want)
	}
}

func TestFilter_TitleAndLabelBothRequired(t *testing.T) {
	issues := []Issue{
		{Number: 1, Title: "fix x", Labels: []Label{{Name: "bug"}}},
		{Number: 2, Title: "fix y", Labels: []Label{{Name: "enhancement"}}},
		{Number: 3, Title: "feature", Labels: []Label{{Name: "bug"}}},
	}
	got := Filter(issues, []string{"bug"}, "fix")
	want := []int{1}
	if !reflect.DeepEqual(numbers(got), want) {
		t.Errorf("title+label filter: got %v, want %v", numbers(got), want)
	}
}

func TestClassifyChecks(t *testing.T) {
	tests := []struct {
		name     string
		buckets  string
		grace    bool
		want     string
	}{
		{"empty in grace -> wait", `[]`, true, "wait-for-registration"},
		{"empty after grace -> pass", `[]`, false, "pass"},
		{"all pass", `["pass","pass","skipping"]`, false, "pass"},
		{"all pass mixed with skipping", `["pass","skipping","pass"]`, false, "pass"},
		{"one fail -> fail", `["pass","fail","pass"]`, false, "fail"},
		{"one cancel -> fail", `["cancel"]`, false, "fail"},
		{"in-progress -> pending", `["pass","pending"]`, false, "pending"},
		{"malformed -> pending (tolerant)", `not json`, false, "pending"},
		{"single pass", `["pass"]`, false, "pass"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyChecks(tt.buckets, tt.grace)
			if got != tt.want {
				t.Errorf("classifyChecks(%s, grace=%v) = %q, want %q",
					tt.buckets, tt.grace, got, tt.want)
			}
		})
	}
}

func TestParsePRState(t *testing.T) {
	tests := []struct {
		in   string
		want PRState
	}{
		{"OPEN", PRStateOpen},
		{"open", PRStateOpen}, // tolerate lowercase
		{"CLOSED", PRStateClosed},
		{"MERGED", PRStateMerged},
		{"  OPEN  ", PRStateOpen}, // tolerate whitespace
		{"", PRStateNone},
		{"WEIRD_FUTURE_STATE", PRStateNone},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := parsePRState(tt.in); got != tt.want {
				t.Errorf("parsePRState(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func numbers(issues []Issue) []int {
	out := make([]int, len(issues))
	for i, iss := range issues {
		out[i] = iss.Number
	}
	return out
}

// reviewerMatches must handle the [bot] suffix that GitHub adds to bot
// account logins on some endpoints but not others. CodeRabbit's login
// is "coderabbitai[bot]" from the issue-comments API but "coderabbitai"
// from gh pr view's normalized output — the user's config can use
// either form and we should accept it.
func TestReviewerMatches(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		actual     string
		want       bool
	}{
		{"exact match", []string{"coderabbitai"}, "coderabbitai", true},
		{"bot suffix on actual only", []string{"coderabbitai"}, "coderabbitai[bot]", true},
		{"bot suffix on config only", []string{"coderabbitai[bot]"}, "coderabbitai", true},
		{"bot suffix on both", []string{"coderabbitai[bot]"}, "coderabbitai[bot]", true},
		{"mismatch", []string{"coderabbitai"}, "greptileai", false},
		{"empty configured", []string{}, "coderabbitai", false},
		{"empty actual", []string{"coderabbitai"}, "", false},
		{"multiple, second matches", []string{"greptileai", "coderabbitai"}, "coderabbitai[bot]", true},
		{"case mismatch (logins are case-sensitive)", []string{"CodeRabbitAI"}, "coderabbitai", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewerMatches(tt.configured, tt.actual); got != tt.want {
				t.Errorf("reviewerMatches(%v, %q) = %v, want %v",
					tt.configured, tt.actual, got, tt.want)
			}
		})
	}
}

func TestParseRunIDFromCheckURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
		ok   bool
	}{
		{
			"actions run + job suffix",
			"https://github.com/owner/repo/actions/runs/12345/job/67890",
			"12345", true,
		},
		{
			"actions run no suffix",
			"https://github.com/owner/repo/actions/runs/99",
			"99", true,
		},
		{
			"actions run with query string",
			"https://github.com/owner/repo/actions/runs/42?attempt=2",
			"42", true,
		},
		{
			"coderabbit review link -> no run id",
			"https://github.com/owner/repo/pull/8#pullrequestreview-123",
			"", false,
		},
		{
			"empty",
			"",
			"", false,
		},
		{
			"marker present but no id after it",
			"https://github.com/owner/repo/actions/runs/",
			"", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRunIDFromCheckURL(tt.url)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseRunIDFromCheckURL(%q) = (%q, %v), want (%q, %v)",
					tt.url, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTruncateLogTail(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		n           int
		wantSubstr  []string
		wantNoTrunc bool // when true, output should be untouched
	}{
		{
			name:        "shorter than budget -> untouched",
			input:       "line1\nline2\n",
			n:           100,
			wantNoTrunc: true,
		},
		{
			name:       "longer than budget -> truncation marker + tail",
			input:      strings.Repeat("a", 5000) + "\nFINAL LINE\n",
			n:          50,
			wantSubstr: []string{"truncated", "FINAL LINE"},
		},
		{
			name:       "no trailing newline -> still truncates",
			input:      strings.Repeat("x", 1000),
			n:          20,
			wantSubstr: []string{"truncated"},
		},
		{
			name:       "exactly at budget -> untouched",
			input:      "0123456789",
			n:          10,
			wantNoTrunc: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateLogTail(tt.input, tt.n)
			if tt.wantNoTrunc {
				if got != tt.input {
					t.Errorf("expected untouched output, got modified:\n%s", got)
				}
				return
			}
			for _, sub := range tt.wantSubstr {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in truncated output:\n%s", sub, got)
				}
			}
			// Truncated output must be strictly shorter than the input
			// (otherwise truncation is pointless).
			if len(got) >= len(tt.input) {
				t.Errorf("truncated output not shorter: got %d, input %d", len(got), len(tt.input))
			}
		})
	}
}

func TestSeenReviews_TracksByKind(t *testing.T) {
	s := NewSeenReviews()
	r1 := &Review{Kind: "review", ID: 100}
	r2 := &Review{Kind: "comment", ID: 100} // same numeric ID, different kind
	c1 := &Review{Kind: "comment", ID: 200}

	if s.has("review", 100) {
		t.Error("fresh SeenReviews should be empty")
	}
	s.Mark(r1)
	if !s.has("review", 100) {
		t.Error("marked review not in seen-set")
	}
	if s.has("comment", 100) {
		t.Error("marking a review should not also mark a comment with the same numeric ID")
	}
	s.Mark(r2)
	s.Mark(c1)
	if !s.has("comment", 100) || !s.has("comment", 200) {
		t.Error("marked comments not in seen-set")
	}
}
