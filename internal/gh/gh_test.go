package gh

import (
	"reflect"
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

func numbers(issues []Issue) []int {
	out := make([]int, len(issues))
	for i, iss := range issues {
		out[i] = iss.Number
	}
	return out
}
