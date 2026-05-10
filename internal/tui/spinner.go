// Package tui provides terminal UI primitives — currently just a
// phase-labeled spinner for showing progress during long polls.
//
// The spinner runs in a goroutine, redraws once per ~100ms via \r,
// and only activates when stderr is a TTY. The With helper takes a
// label and a function, runs the function while the spinner draws,
// and guarantees teardown via defer.
package tui

import (
	"context"
	"fmt"
	"os"
	"time"
)

var frames = []rune{'|', '/', '-', '\\'}

// With wraps fn in a phase-labeled spinner. The spinner clears its
// line and stops cleanly when fn returns, even on error or panic
// (via defer). When stderr is not a TTY the spinner is a no-op and
// fn runs unchanged.
//
// fn's error is propagated; the spinner itself never returns an error.
func With(label string, fn func() error) error {
	stop := start(label)
	defer stop()
	return fn()
}

// stopFn clears the spinner line and waits for the goroutine to exit.
type stopFn func()

func start(label string) stopFn {
	if !isTTY(os.Stderr) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go run(ctx, label, done)
	return func() {
		cancel()
		<-done                            // ensure the goroutine has finished its last draw
		fmt.Fprint(os.Stderr, "\r\033[K") // final clear
	}
}

func run(ctx context.Context, label string, done chan struct{}) {
	defer close(done)
	start := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(start)
			mm := int(elapsed.Minutes())
			ss := int(elapsed.Seconds()) % 60
			fmt.Fprintf(os.Stderr, "\r\033[K\033[2m%c %s [%02d:%02d]\033[0m",
				frames[i%len(frames)], label, mm, ss)
			i++
		}
	}
}

// isTTY returns true if f is a terminal (character device). Stdlib-only
// implementation — works for macOS/Linux, which is chomper's only
// supported target.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
