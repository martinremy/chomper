package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/martinremy/chomper/internal/judge"
)

// SupervisorConfig configures one supervised worker invocation.
type SupervisorConfig struct {
	Harness          Harness
	JudgeModel       string // empty -> harness default (Haiku for claude)
	SilenceThreshold time.Duration
	SilenceMax       time.Duration // cap on exponential backoff
	MaxQuestions     int
	BufferLines      int // how many recent events to retain for the judge

	// Out is where formatted events + judge log lines are written.
	// Defaults to os.Stdout for events and os.Stderr for judge logs.
	Out    io.Writer
	JudgeW io.Writer

	IssueCtx judge.IssueContext
}

// Supervisor runs the worker harness in stream-json I/O and supervises
// it by classifying silence + answering questions via the judge.
//
// Lifecycle:
//   1. Spawn worker via Harness.RunWorkerStream (stdin stays open).
//   2. Reader goroutine ships parsed Events on a buffered channel.
//   3. Main loop selects on (event, silence-timer, ctx).
//   4. On silence -> classify -> {keep waiting (backoff) | inject answer | abort}.
//   5. On terminal event -> Cmd.Wait + return.
//
// Every code path that exits the loop kills the worker on the way out
// when an error has occurred, so abandoned subprocesses don't leak.
type Supervisor struct {
	cfg SupervisorConfig
}

// NewSupervisor returns a Supervisor with defaults filled in.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	if cfg.SilenceThreshold <= 0 {
		cfg.SilenceThreshold = 30 * time.Second
	}
	if cfg.SilenceMax <= 0 {
		cfg.SilenceMax = 5 * time.Minute
	}
	if cfg.MaxQuestions <= 0 {
		cfg.MaxQuestions = 5
	}
	if cfg.BufferLines <= 0 {
		cfg.BufferLines = 200
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.JudgeW == nil {
		cfg.JudgeW = os.Stderr
	}
	return &Supervisor{cfg: cfg}
}

// Run spawns the worker, supervises it, returns its exit error (or a
// supervision-induced error like max-questions exceeded).
func (s *Supervisor) Run(ctx context.Context, repoDir, prompt string) error {
	proc, err := s.cfg.Harness.RunWorkerStream(ctx, repoDir, prompt)
	if err != nil {
		return err
	}

	s.logJudge("supervising %s; silence threshold %.0fs, max questions %d",
		s.cfg.Harness.Name(), s.cfg.SilenceThreshold.Seconds(), s.cfg.MaxQuestions)

	events := make(chan Event, 100)
	go readEvents(proc.Stdout, events)

	buf := newRingBuffer(s.cfg.BufferLines)
	var lastToolUseID string
	threshold := s.cfg.SilenceThreshold
	answered := 0

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return proc.Cmd.Wait()
			}

			if rendered := FormatEvent(ev); rendered != "" {
				fmt.Fprintln(s.cfg.Out, rendered)
			}
			buf.push(ev)
			if id := ev.ToolUseID(); id != "" {
				lastToolUseID = id
			}
			if ev.IsTerminal() {
				return proc.Cmd.Wait()
			}
			threshold = s.cfg.SilenceThreshold

		case <-time.After(threshold):
			elapsed := threshold.Seconds()
			s.logJudge("silence detected (%.0fs); classifying...", elapsed)
			v := judge.Classify(ctx, s.cfg.Harness, s.cfg.JudgeModel,
				s.cfg.IssueCtx, buf.tail(6000), elapsed)

			switch v.State {
			case "working":
				threshold = nextBackoff(threshold, s.cfg.SilenceMax)
				s.logJudge("verdict=working (%s); next check in %.0fs", v.Reason, threshold.Seconds())

			case "errored":
				s.logJudge("verdict=errored (%s); aborting", v.Reason)
				kill(proc.Cmd)
				return fmt.Errorf("worker errored: %s", v.Reason)

			case "waiting":
				if answered >= s.cfg.MaxQuestions {
					s.logJudge("reached max questions (%d); aborting issue", s.cfg.MaxQuestions)
					kill(proc.Cmd)
					return fmt.Errorf("exceeded max questions (%d)", s.cfg.MaxQuestions)
				}
				s.logJudge("verdict=waiting; question: %s", truncate(v.Question, 120))
				answer, err := judge.Answer(ctx, s.cfg.Harness, s.cfg.JudgeModel,
					s.cfg.IssueCtx, buf.tail(4000), v.Question, v.Options)
				if err != nil {
					s.logJudge("answerer failed; aborting rather than guessing: %s", err)
					kill(proc.Cmd)
					return fmt.Errorf("answerer judge: %w", err)
				}
				event := SynthesizeToolResult(answer, lastToolUseID)
				if _, err := proc.Stdin.Write(event); err != nil {
					s.logJudge("failed to write tool_result to worker stdin: %s", err)
					kill(proc.Cmd)
					return fmt.Errorf("inject answer: %w", err)
				}
				answered++
				threshold = s.cfg.SilenceThreshold
				s.logJudge("answered question #%d: %s", answered, truncate(answer, 80))

			default:
				s.logJudge("unknown classifier state %q; treating as working", v.State)
				threshold = nextBackoff(threshold, s.cfg.SilenceMax)
			}

		case <-ctx.Done():
			kill(proc.Cmd)
			return ctx.Err()
		}
	}
}

// logJudge writes a [judge]-prefixed line to the judge writer (stderr
// by default). Bold cyan via ANSI when the writer is a TTY-backed
// os.Stderr; plain otherwise.
func (s *Supervisor) logJudge(format string, args ...any) {
	prefix := "[judge]"
	if s.cfg.JudgeW == os.Stderr && useColor {
		prefix = "\033[1;36m[judge]\033[0m"
	}
	fmt.Fprintf(s.cfg.JudgeW, prefix+" "+format+"\n", args...)
}

// readEvents scans line-delimited stream-json from r and ships parsed
// events to ch. On non-JSON lines, ships an Event{Raw: line} so the
// caller can still forward them. Closes ch on EOF or scan error.
func readEvents(r io.ReadCloser, ch chan<- Event) {
	defer close(ch)
	defer r.Close()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			ev = Event{Raw: scanner.Text()}
		}
		ch <- ev
	}
}

// nextBackoff multiplies threshold by 1.5, capped at max.
func nextBackoff(current, max time.Duration) time.Duration {
	next := time.Duration(float64(current) * 1.5)
	if next > max {
		return max
	}
	return next
}

// kill terminates the worker subprocess. Errors are intentionally
// ignored — we only call this from failure paths, where the cleanup
// is best-effort.
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}
