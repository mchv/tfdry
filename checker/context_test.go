package checker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// These tests pin the cancellation contract for the public checker API:
// every long-running entry point accepts ctx as its first argument and
// returns ctx.Err() (wrapped) if cancellation fires mid-flight.
//
// The cancellation contract isn't only about responsiveness — without
// ctx, callers like main.run() can't propagate SIGINT into a running
// checker pass, and future watch-mode / LSP / huge-monorepo use cases
// have no way to bail early. Shipping these signatures pre-v0.1.0
// avoids a breaking-change later (TODO.md v0.1.0 scope: PR A2).
//
// The tests cancel BEFORE invoking the function (immediate cancel)
// rather than racing the cancel against the work — that produces
// flake-free, deterministic test runs. A concurrent-cancel test would
// add scheduler timing dependence for no extra coverage of the
// invariant we care about (that ctx is checked at all).

// makeManyTfFiles creates n .tf files in dir so the walker has enough
// work to hit a cancellation checkpoint even on fast hardware.
func makeManyTfFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%04d.tf", i))
		if err := os.WriteFile(path, []byte("locals {\n  x = 1\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// mustRun is a test helper that calls checker.Run and panics on error.
// Used in test sites that spread Run's output into an append() literal,
// where capturing the new error return inline would require restructuring
// the test. Cancellation tests don't go through this helper — they want
// to OBSERVE the error rather than panic on it.
func mustRun(ctx context.Context, files []checker.ParsedFile, checks checker.CheckSet, dir string) []checker.Violation {
	vs, err := checker.Run(ctx, files, checks, dir)
	if err != nil {
		panic(fmt.Sprintf("checker.Run: %v", err))
	}
	return vs
}

func TestParseDir_RespectsContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeManyTfFiles(t, dir, 50)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the call — ParseDir must observe this

	_, _, err := checker.ParseDir(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParseDir with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestRun_RespectsContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeManyTfFiles(t, dir, 50)
	// First parse with a live ctx so we have files to run checks on.
	files, _, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = checker.Run(ctx, files, nil, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestCheckFormat_RespectsContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	makeManyTfFiles(t, dir, 50)
	files, _, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = checker.CheckFormat(ctx, files)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CheckFormat with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestFixFormat_RespectsContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create unformatted files so FixFormat has actual work to do.
	for i := 0; i < 50; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%04d.tf", i))
		// Deliberately mis-indented; tfdry will reformat.
		if err := os.WriteFile(path, []byte("locals{\nx=1\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, _, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = checker.FixFormat(ctx, files, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("FixFormat with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

// TestParseDir_ContextBackground_NoRegression confirms the happy path
// (context.Background passed) still produces the same parse output as
// before the ctx sweep. Without this guard, a cancellation-checkpoint
// regression that always returned ctx.Err() would slip past.
func TestParseDir_ContextBackground_NoRegression(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("locals {\n  x = 1\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	files, vs, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ParseDir with Background ctx: unexpected err=%v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 parsed file, got %d", len(files))
	}
	for _, v := range vs {
		if strings.HasPrefix(v.Code, "E001") {
			t.Errorf("unexpected parse violation: %+v", v)
		}
	}
}
