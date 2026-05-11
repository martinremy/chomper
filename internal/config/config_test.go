package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaults_AreUsable(t *testing.T) {
	c := Defaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("Defaults() should pass Validate; got: %v", err)
	}
	if c.Harness != "claude" {
		t.Errorf("default harness = %q, want claude", c.Harness)
	}
	if c.TrunkBranch != "main" {
		t.Errorf("default trunk_branch = %q, want main", c.TrunkBranch)
	}
	if !reflect.DeepEqual(c.Filter.Labels, []string{"chomper"}) {
		t.Errorf("default labels = %v, want [chomper]", c.Filter.Labels)
	}
}

// Load merges YAML on top of Defaults; partial files leave other fields
// at their default values.
func TestLoad_MergesWithDefaults(t *testing.T) {
	withYAML(t, `
harness: codex
trunk_branch: develop
filter:
  labels:
    - bug
    - "good first issue"
  title_match: fix
`, func(c *Config) {
		if c.Harness != "codex" {
			t.Errorf("Harness = %q, want codex", c.Harness)
		}
		if c.TrunkBranch != "develop" {
			t.Errorf("TrunkBranch = %q, want develop", c.TrunkBranch)
		}
		if !reflect.DeepEqual(c.Filter.Labels, []string{"bug", "good first issue"}) {
			t.Errorf("Labels = %v", c.Filter.Labels)
		}
		if c.Filter.TitleMatch != "fix" {
			t.Errorf("TitleMatch = %q", c.Filter.TitleMatch)
		}
		// Unspecified fields keep their defaults.
		if c.MergeStrategy != "squash" {
			t.Errorf("MergeStrategy = %q, want default 'squash'", c.MergeStrategy)
		}
		if c.CITimeoutMinutes != 30 {
			t.Errorf("CITimeoutMinutes = %d, want default 30", c.CITimeoutMinutes)
		}
	})
}

// Strict KnownFields: typos should be a hard error, not silently
// dropped. The whole point of moving off the bash awk-fallback was to
// stop swallowing user mistakes.
func TestLoad_UnknownKeyIsError(t *testing.T) {
	withYAMLExpectError(t, `
harness: claude
truk_branch: main      # typo: missing 'n'
`, "field truk_branch")
}

// Same posture for an unknown nested key.
func TestLoad_UnknownNestedKeyIsError(t *testing.T) {
	withYAMLExpectError(t, `
filter:
  labls:               # typo
    - bug
`, "field labls")
}

func TestApply_CLIOverridesYAML(t *testing.T) {
	c := Defaults()
	c.Filter.Labels = []string{"from-yaml"}
	c.Harness = "codex"
	c.Filter.TitleMatch = ""

	c.Apply(Overrides{
		Harness:    "claude",
		Labels:     []string{"bug", "triage"},
		TitleMatch: "fix",
	})

	if c.Harness != "claude" {
		t.Errorf("CLI didn't override Harness: got %q", c.Harness)
	}
	if !reflect.DeepEqual(c.Filter.Labels, []string{"bug", "triage"}) {
		t.Errorf("CLI didn't override Labels: got %v", c.Filter.Labels)
	}
	if c.Filter.TitleMatch != "fix" {
		t.Errorf("CLI didn't set TitleMatch: got %q", c.Filter.TitleMatch)
	}
}

// CLI labels REPLACE, not merge. This is the v0.1 semantic.
func TestApply_CLILabelsReplace(t *testing.T) {
	c := Defaults() // has [chomper]
	c.Apply(Overrides{Labels: []string{"bug"}})
	if !reflect.DeepEqual(c.Filter.Labels, []string{"bug"}) {
		t.Errorf("CLI labels should replace, not merge; got %v", c.Filter.Labels)
	}
}

// Empty CLI fields are no-ops; defaults survive.
func TestApply_EmptyOverridesDoNotClobber(t *testing.T) {
	c := Defaults()
	c.Harness = "codex"
	c.Apply(Overrides{}) // all empty
	if c.Harness != "codex" {
		t.Errorf("empty Harness override clobbered prior value")
	}
	if !reflect.DeepEqual(c.Filter.Labels, []string{"chomper"}) {
		t.Errorf("empty Labels override clobbered prior value: got %v", c.Filter.Labels)
	}
}

func TestValidate_RejectsBadValues(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Config)
		wantSubstr string
	}{
		{"bad harness", func(c *Config) { c.Harness = "neither" }, "harness"},
		{"bad merge strategy", func(c *Config) { c.MergeStrategy = "hugmerge" }, "merge_strategy"},
		{"zero ci timeout", func(c *Config) { c.CITimeoutMinutes = 0 }, "ci_timeout_minutes"},
		{"negative max questions", func(c *Config) { c.AutoAnswerMaxQuestions = -1 }, "auto_answer_max_questions"},
		{"zero review timeout", func(c *Config) { c.WaitForReviews.TimeoutMinutes = 0 }, "timeout_minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mutate(c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not mention %q", err, tt.wantSubstr)
			}
		})
	}
}

// --- helpers ---------------------------------------------------------

// withYAML writes contents to ./.chomper.yaml inside a temp cwd, runs
// fn with the loaded config, and restores the original cwd.
func withYAML(t *testing.T, contents string, fn func(*Config)) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".chomper.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(cwd)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fn(cfg)
}

func withYAMLExpectError(t *testing.T, contents, wantSubstr string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".chomper.yaml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(cwd)
	_, err := Load()
	if err == nil {
		t.Fatal("expected Load error, got nil")
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q", err, wantSubstr)
	}
}
