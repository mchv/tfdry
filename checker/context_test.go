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

// The four pre-cancel tests below intentionally do NO disk setup.
// Each function checks ctx.Err() as its very first instruction and
// returns immediately, so any files we created would be unused. The
// minimal form expresses the test's intent more directly ("this
// function honours pre-call cancellation") and is faster to run on
// CI without sacrificing coverage: TestFixFormat_EntryCancelReturnsNonNilMap
// (below) and the SIGINT subprocess test in main_test/sigint_test.go
// already exercise the mid-walk / mid-pass cancellation paths.

func TestParseDir_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the call — ParseDir must observe this

	// Use an empty string as the dir. ParseDir's entry-level ctx.Err()
	// check fires before any I/O (including os.ReadDir on the path),
	// so passing a bogus dir is intentional: if a future refactor moved
	// the ctx check after the ReadDir, this test would surface an
	// ENOENT error instead of context.Canceled — exactly the regression
	// the test is meant to catch.
	_, _, err := checker.ParseDir(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParseDir with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestRun_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// nil files / nil checks: Run's entry ctx.Err() check fires before
	// dereferencing either. If a refactor moves the check below the
	// for-range, the empty slice means there's nothing to range over
	// and we'd silently get a clean nil/nil return, which the assertion
	// below would catch as "err = <nil>, want context.Canceled".
	_, err := checker.Run(ctx, nil, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestCheckFormat_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := checker.CheckFormat(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CheckFormat with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestFixFormat_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := checker.FixFormat(ctx, nil, "")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("FixFormat with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

// TestFixFormat_EntryCancelReturnsNonNilMap pins the partial-results
// contract specifically at the entry-cancel boundary. Before this test,
// FixFormat returned (nil, nil, ctx.Err()) when ctx was already cancelled
// at the entry check, but (non-nil-map, violations, ctx.Err()) when
// cancellation fired mid-loop. The inconsistency forced callers to
// nil-check the map even when they only wanted to range/inspect partial
// results (range over nil map is a no-op, but direct map assignment
// would panic — and the "partial results" promise implies the map can
// be extended by the caller for accumulation patterns).
//
// The fix is to initialize the map before the entry check so every
// FixFormat exit path returns a non-nil map. The violations slice
// stays as a Go-idiomatic nil for "no results" (slice operations are
// safe on nil).
func TestFixFormat_EntryCancelReturnsNonNilMap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Pre-cancel before any work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fixed, violations, err := checker.FixFormat(ctx, nil, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if fixed == nil {
		t.Errorf("fixed map = nil at entry-cancel; partial-results contract requires non-nil map for consistency with mid-loop cancel path")
	}
	if len(fixed) != 0 {
		t.Errorf("fixed map = %v, want empty (no work was done)", fixed)
	}
	// violations being nil is acceptable — slice operations are safe
	// on nil and Go convention treats nil/empty slices interchangeably.
	_ = violations
}

// TestParseDir_ContextBackground_NoRegression confirms the happy path
// (context.Background passed) still produces the same parse output as
// before the ctx sweep. Without this guard, a cancellation-checkpoint
// regression that always returned ctx.Err() would slip past.
func TestParseDir_ContextBackground_NoRegression(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("locals {\n  x = 1\n}\n"), 0o644); err != nil {
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
