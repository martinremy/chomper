// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package config loads, validates, and merges chomper's settings.
//
// Precedence (highest first):
//  1. CLI flags (via Apply)
//  2. .chomper.yaml in cwd
//  3. Built-in defaults
//
// Unknown YAML keys are an error — strict by design, to surface typos
// loudly. This is the same posture the bash prototype's parse_config.py
// adopted after the silent-fallback bug.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the resolved chomper configuration for one invocation.
type Config struct {
	Harness                string  `yaml:"harness"`
	TrunkBranch            string  `yaml:"trunk_branch"`
	MergeStrategy          string  `yaml:"merge_strategy"`
	CITimeoutMinutes       int     `yaml:"ci_timeout_minutes"`
	Filter                 Filter  `yaml:"filter"`
	AutoAnswer             bool    `yaml:"auto_answer"`
	AutoAnswerModel        string  `yaml:"auto_answer_model"`
	AutoAnswerMaxQuestions int     `yaml:"auto_answer_max_questions"`
	AutoAnswerSilenceS     int     `yaml:"auto_answer_silence_threshold_s"`
	WaitForReviews         Reviews `yaml:"wait_for_reviews"`
	FixCI                  FixCI   `yaml:"fix_ci"`
}

// Filter applies to issue selection.
type Filter struct {
	Labels     []string `yaml:"labels"`
	TitleMatch string   `yaml:"title_match"`
}

// Reviews configures the wait-for-reviews loop.
type Reviews struct {
	Reviewers            []string `yaml:"reviewers"`
	TimeoutMinutes       int      `yaml:"timeout_minutes"`
	MaxIterations        int      `yaml:"max_iterations"`
	JudgeAdjudicates     bool     `yaml:"judge_adjudicates"`
	ApproveStateRequired bool     `yaml:"approve_state_required"`
}

// FixCI configures the auto-fix-CI loop. When CI fails red on a chomper
// PR, the loop re-invokes the harness with failed-check context and
// re-polls CI, up to MaxIterations times. Bounded blast radius is the
// whole point of the cap — without it, a stubborn failure could ratchet
// harness invocations indefinitely.
type FixCI struct {
	Enabled       bool `yaml:"enabled"`
	MaxIterations int  `yaml:"max_iterations"`
}

// Defaults returns the built-in default config. Built once per invocation
// and overlaid with YAML + CLI in Load + Apply.
func Defaults() *Config {
	return &Config{
		Harness:          "claude",
		TrunkBranch:      "main",
		MergeStrategy:    "squash",
		CITimeoutMinutes: 30,
		Filter: Filter{
			Labels: []string{"chomper"},
		},
		AutoAnswerMaxQuestions: 5,
		AutoAnswerSilenceS:     30,
		WaitForReviews: Reviews{
			TimeoutMinutes:   15,
			MaxIterations:    10,
			JudgeAdjudicates: true,
		},
		FixCI: FixCI{
			Enabled:       true,
			MaxIterations: 3,
		},
	}
}

// Load merges .chomper.yaml (if present in cwd) onto Defaults. Returns
// the resolved config or an error pointing at the offending key/line.
func Load() (*Config, error) {
	cfg := Defaults()

	const path = ".chomper.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // unknown YAML keys -> error (catches typos)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Overrides are the CLI-supplied values that win over YAML + defaults.
// Empty/zero fields are no-ops; they do NOT clobber the prior layer.
type Overrides struct {
	Harness    string
	Labels     []string
	TitleMatch string
}

// Apply overlays CLI args onto the config.
//
// Label semantics match the bash prototype: when --label flags are
// supplied, they fully REPLACE the YAML/default list rather than
// appending. This keeps "chomper --label bug" behaving the same as
// "chomper --label bug --label other" — both clear the default
// `chomper` label.
func (c *Config) Apply(o Overrides) *Config {
	if o.Harness != "" {
		c.Harness = o.Harness
	}
	if len(o.Labels) > 0 {
		c.Filter.Labels = o.Labels
	}
	if o.TitleMatch != "" {
		c.Filter.TitleMatch = o.TitleMatch
	}
	return c
}

// Validate checks the resolved config against the allowed value sets.
// Returns the first error encountered, so users fix one thing at a time.
func (c *Config) Validate() error {
	switch c.Harness {
	case "claude", "codex":
	default:
		return fmt.Errorf("invalid harness: %q (use claude or codex)", c.Harness)
	}
	switch c.MergeStrategy {
	case "squash", "merge", "rebase":
	default:
		return fmt.Errorf("invalid merge_strategy: %q (use squash|merge|rebase)", c.MergeStrategy)
	}
	if c.CITimeoutMinutes <= 0 {
		return fmt.Errorf("invalid ci_timeout_minutes: %d (must be a positive integer)", c.CITimeoutMinutes)
	}
	if c.AutoAnswerMaxQuestions <= 0 {
		return fmt.Errorf("invalid auto_answer_max_questions: %d", c.AutoAnswerMaxQuestions)
	}
	if c.AutoAnswerSilenceS <= 0 {
		return fmt.Errorf("invalid auto_answer_silence_threshold_s: %d", c.AutoAnswerSilenceS)
	}
	r := c.WaitForReviews
	if r.TimeoutMinutes <= 0 {
		return fmt.Errorf("invalid wait_for_reviews.timeout_minutes: %d", r.TimeoutMinutes)
	}
	if r.MaxIterations <= 0 {
		return fmt.Errorf("invalid wait_for_reviews.max_iterations: %d", r.MaxIterations)
	}
	// fix_ci.max_iterations is only meaningful when the loop is enabled;
	// don't reject a disabled-with-zero state (it's inert and that's fine).
	if c.FixCI.Enabled && c.FixCI.MaxIterations <= 0 {
		return fmt.Errorf("invalid fix_ci.max_iterations: %d (must be a positive integer when fix_ci.enabled is true)", c.FixCI.MaxIterations)
	}
	return nil
}
