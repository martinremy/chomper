// chomper — autonomous loop that walks open GitHub issues through to
// merged PRs using an AI coding harness (Claude Code or Codex CLI).
//
// First Go slice: --dry-run path only. Loads config, lists issues,
// applies filters, prints matches. The full per-issue pipeline
// (worktree, harness, poll, merge) is being ported from the bash
// prototype tagged v0.1-bash.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/martinremy/chomper/internal/chomper"
	"github.com/martinremy/chomper/internal/config"
	"github.com/martinremy/chomper/internal/gh"
	"github.com/martinremy/chomper/internal/harness"
)

const usageText = `Usage: chomper [subcommand] [options]

Sequentially work open issues in the current GitHub repo to merged PRs.

Subcommands:
  doctor                     audit local environment (not yet ported)

Options:
  --harness <claude|codex>   override harness from config
  --label <label>            filter by label (repeatable; OR logic)
  --title <string>           filter by title substring (case-insensitive)
  --dry-run                  print matching issues and exit
  --help, -h                 show this message
`

// stringSlice is a flag.Value that accumulates repeated --label values.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	rc := run(os.Args[1:])
	os.Exit(rc)
}

// run is the testable entry point. Returns an exit code rather than
// calling os.Exit directly so tests/main can compose cleanly.
func run(args []string) int {
	// `doctor` is a subcommand, not a flag — handle before flag parsing.
	if len(args) > 0 && args[0] == "doctor" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return chomper.RunDoctor(ctx)
	}

	var (
		dryRun   bool
		harnessName string
		title    string
		labels   stringSlice
		showHelp bool
	)
	fs := flag.NewFlagSet("chomper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageText) }
	fs.BoolVar(&dryRun, "dry-run", false, "")
	fs.StringVar(&harnessName, "harness", "", "")
	fs.StringVar(&title, "title", "", "")
	fs.Var(&labels, "label", "")
	fs.BoolVar(&showHelp, "help", false, "")
	fs.BoolVar(&showHelp, "h", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(usageText)
			return 0
		}
		return 2
	}
	if showHelp {
		fmt.Print(usageText)
		return 0
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	cfg.Apply(config.Overrides{
		Harness:    harnessName,
		Labels:     labels,
		TitleMatch: title,
	})
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client := gh.New()
	if err := client.RequireEnv(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	repo, err := client.CurrentRepo(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not determine current GitHub repo: %s\n", err)
		return 1
	}

	issues, err := client.OpenIssues(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}

	matches := gh.Filter(issues, cfg.Filter.Labels, cfg.Filter.TitleMatch)

	// Header — mirrors the bash prototype's output verbatim so users
	// can directly compare behavior across the two implementations.
	fmt.Printf("repo: %s\n", repo)
	fmt.Printf("harness: %s   trunk: %s   merge: %s   ci-timeout: %dm\n",
		cfg.Harness, cfg.TrunkBranch, cfg.MergeStrategy, cfg.CITimeoutMinutes)
	if len(cfg.Filter.Labels) > 0 {
		fmt.Printf("label filter: %s\n", strings.Join(cfg.Filter.Labels, " "))
	}
	if cfg.Filter.TitleMatch != "" {
		fmt.Printf("title filter: %s\n", cfg.Filter.TitleMatch)
	}
	fmt.Printf("matching open issues: %d\n", len(matches))

	if len(matches) == 0 {
		fmt.Println("nothing to do.")
		return 0
	}

	for _, iss := range matches {
		fmt.Printf("  #%d  %s\n", iss.Number, iss.Title)
	}

	if dryRun {
		fmt.Println("(dry run; no issues will be worked.)")
		return 0
	}

	// Pick the harness. Verify the CLI is actually on PATH before
	// committing to the loop; otherwise the first ProcessIssue dies
	// halfway through.
	h, err := harness.New(cfg.Harness)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		return 1
	}
	if _, err := exec.LookPath(h.Name()); err != nil {
		fmt.Fprintf(os.Stderr, "error: harness %q not found on PATH\n", h.Name())
		return 1
	}

	deps := &chomper.Deps{
		Cfg:     cfg,
		Repo:    repo,
		GH:      client,
		Harness: h,
	}

	for _, iss := range matches {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "interrupted; stopping issue loop")
			break
		}
		if err := chomper.ProcessIssue(ctx, deps, iss); err != nil {
			fmt.Fprintf(os.Stderr, "error: process issue #%d: %s\n", iss.Number, err)
			return 1
		}
	}
	return 0
}
