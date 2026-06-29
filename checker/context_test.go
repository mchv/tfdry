// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"bytes"
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

// ── Bucket B: mid-loop cancellation + per-function entry coverage ────────────
//
// The pre-cancel tests above cover the ENTRY ctx checks (the first
// instruction of each function). The tests below cover the INSIDE-LOOP
// ctx checks (per-iteration responsiveness contract) plus the public
// entry points the original sweep didn't pin (FormatFile,
// WriteFormatted) and the f.Src == nil skip branches (parse-failed
// files must be skipped silently, not crashed on).

// nthErrCtx is a context whose Err() returns nil for the first n-1
// calls and context.Canceled on the n-th call onwards. Used to drive
// mid-loop ctx checks deterministically: pre-cancelling would
// short-circuit at the function's entry check before the loop is
// reached, so a static cancel can't exercise the per-iteration path.
// With n=2: entry check sees nil, the first loop-body check sees
// Canceled.
//
// This wrapping is exclusively a test-only construct — production code
// must rely on the real context.Cancel / Deadline semantics. The
// alternative (running cancel() from a goroutine to race against the
// loop) is non-deterministic and would flake.
type nthErrCtx struct {
	context.Context
	n     int
	calls int
}

func (c *nthErrCtx) Err() error {
	c.calls++
	if c.calls >= c.n {
		return context.Canceled
	}
	return nil
}

func TestFormatFile_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// path / src are not dereferenced before the entry ctx check.
	err := checker.FormatFile(ctx, "/dev/null/should-not-be-touched", []byte("locals {}\n"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("FormatFile with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

func TestWriteFormatted_RespectsContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := checker.WriteFormatted(ctx, "/dev/null/should-not-be-touched", []byte("ok"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("WriteFormatted with cancelled ctx: err=%v, want context.Canceled", err)
	}
}

// TestWriteFormatted_HappyPath exercises the body of WriteFormatted
// (the atomic rewrite) — distinct from FormatFile, which formats from
// raw source. Callers that already hold the formatted bytes (e.g. the
// runFmt path in main.go) use WriteFormatted to skip the redundant
// hclwrite.Format pass.
func TestWriteFormatted_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.tf")
	if err := os.WriteFile(path, []byte("locals{a=1}"), 0o644); err != nil {
		t.Fatal(err)
	}
	formatted := []byte("locals {\n  a = 1\n}\n")
	if err := checker.WriteFormatted(context.Background(), path, formatted); err != nil {
		t.Fatalf("WriteFormatted: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, formatted) {
		t.Errorf("file not rewritten: got %q, want %q", got, formatted)
	}
}

// TestCheckFormat_MidLoopCancel exercises the per-iteration ctx check
// inside CheckFormat. Uses nthErrCtx: entry-check passes (call 1
// returns nil), the inside-loop check on the first iteration fires
// (call 2 returns context.Canceled).
func TestCheckFormat_MidLoopCancel(t *testing.T) {
	t.Parallel()
	ctx := &nthErrCtx{Context: context.Background(), n: 2}
	files := []checker.ParsedFile{
		{Name: "a.tf", Src: []byte("locals { x = 1 }")},
		{Name: "b.tf", Src: []byte("locals { y = 2 }")},
	}
	_, err := checker.CheckFormat(ctx, files)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CheckFormat mid-loop cancel: err=%v, want context.Canceled", err)
	}
}

// TestCheckFormat_NilSrc_Skipped pins the contract that a ParsedFile
// whose Src is nil (parse failure earlier in the pipeline) is skipped
// silently — no E008 emission, no panic on the bytes.Equal call.
func TestCheckFormat_NilSrc_Skipped(t *testing.T) {
	t.Parallel()
	files := []checker.ParsedFile{
		{Name: "broken.tf", Src: nil},
	}
	vs, err := checker.CheckFormat(context.Background(), files)
	if err != nil {
		t.Fatalf("CheckFormat with nil-Src file: unexpected err=%v", err)
	}
	if len(vs) != 0 {
		t.Errorf("nil-Src ParsedFile should produce no E008; got %v", vs)
	}
}

// TestFixFormat_MidLoopCancel — same idea as CheckFormat above, but
// also pins the partial-results contract (fixed map non-nil even when
// the loop bails early on cancellation).
func TestFixFormat_MidLoopCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := &nthErrCtx{Context: context.Background(), n: 2}
	files := []checker.ParsedFile{
		{Name: "a.tf", Src: []byte("locals { x = 1 }")},
	}
	fixed, _, err := checker.FixFormat(ctx, files, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("FixFormat mid-loop cancel: err=%v, want context.Canceled", err)
	}
	if fixed == nil {
		t.Errorf("fixed map must be non-nil on mid-loop cancel for partial-results contract consistency")
	}
}

// TestFixFormat_NilSrc_Skipped — parse-failed files must not crash the
// fix pass on the bytes.Equal / hclwrite.Format calls below the skip.
func TestFixFormat_NilSrc_Skipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := []checker.ParsedFile{
		{Name: "broken.tf", Src: nil},
	}
	fixed, vs, err := checker.FixFormat(context.Background(), files, dir)
	if err != nil {
		t.Fatalf("FixFormat with nil-Src file: unexpected err=%v", err)
	}
	if len(fixed) != 0 {
		t.Errorf("nil-Src file should not be fixed; got %v", fixed)
	}
	if len(vs) != 0 {
		t.Errorf("nil-Src file should produce no violations; got %v", vs)
	}
}

// TestRun_MidLoopCancel exercises the per-file ctx check inside Run's
// main file-iteration loop (checker/checks.go:115). Same nthErrCtx
// pattern as CheckFormat/FixFormat above.
func TestRun_MidLoopCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, _, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &nthErrCtx{Context: context.Background(), n: 2}
	_, err = checker.Run(ctx, files, nil, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run mid-loop cancel: err=%v, want context.Canceled", err)
	}
}

// TestParseDir_SequentialMidLoopCancel exercises the per-entry ctx
// check in ParseDir's sequential branch (<= 4 .tf files). Two files
// are sufficient: nthErrCtx with n=3 passes the entry check (call 1),
// passes the first loop iteration (call 2 from the loop's ctx.Err),
// fails the second loop iteration (call 3).
func TestParseDir_SequentialMidLoopCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.tf", "b.tf"} {
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte("locals { x = 1 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &nthErrCtx{Context: context.Background(), n: 3}
	_, _, err := checker.ParseDir(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParseDir sequential mid-loop cancel: err=%v, want context.Canceled", err)
	}
}

// TestParseDir_ConcurrentPath_RespectsContext exercises the concurrent
// branch of ParseDir (> 4 .tf files, errgroup-based parallel parse).
// Pre-cancels the ctx so errgroup workers see the cancellation
// immediately and propagate via g.Wait().
func TestParseDir_ConcurrentPath_RespectsContext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 10 files comfortably above the sequential/concurrent threshold (4).
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("f%d.tf", i)
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte("locals { x = 1 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Pre-cancel: the entry check at ParseDir's top fires before
	// reaching the concurrent dispatch. To actually hit the
	// errgroup paths we wrap ctx so the entry check passes (n=2)
	// and the dispatcher / worker see the cancellation on a
	// subsequent Err() call.
	wrapped := &nthErrCtx{Context: ctx, n: 2}
	_, _, err := checker.ParseDir(wrapped, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ParseDir concurrent path: err=%v, want context.Canceled", err)
	}
}
