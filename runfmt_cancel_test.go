package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunFmt_PreCancel_BailsBeforeIO pins the contract that runFmt
// must check ctx.Err() at its very first instruction, before any
// filesystem I/O. Without this guard, a pre-cancelled context still
// pays for os.Lstat (or os.Stat) on the supplied path plus a
// potentially deep collectFmtDirs walk in -recursive mode, even
// though the result is going to be discarded immediately.
//
// The test deliberately uses a path that doesn't exist on disk. Two
// possible outcomes:
//
//   - Without the entry-level check: runFmt calls os.Lstat first,
//     gets ENOENT, prints "tfdry fmt: <path>: ..." to stderr, returns
//     exit code 2.
//   - With the entry-level check: handleFatalErr(ctx.Err(), ...) fires
//     at the top of runFmt, prints "tfdry: interrupted", returns exit
//     code 130
//     without ever touching the filesystem.
//
// The exit-code assertion is the discriminator: 130 means the ctx check
// fired first; anything else means we wasted I/O on a doomed run. The
// stderr-message assertion guards against an accidental future change
// that returns 130 from some other code path.
//
// runFmtFile has its own equivalent entry check (added in PR A2 round 1)
// and is covered indirectly via the SIGINT subprocess test, but the
// same principle applies — a single missing guard at the top costs us
// the os.ReadFile cost when ctx is already cancelled.
func TestRunFmt_PreCancel_BailsBeforeIO(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel BEFORE the call

	var stdout, stderr bytes.Buffer
	// /nonexistent/path is chosen so that any I/O attempt produces a
	// distinct exit code (2) and distinct stderr text from the
	// interrupted path (130 + "tfdry: interrupted"). A real path would
	// muddy the discriminator.
	code := runFmt(ctx, &stdout, &stderr, "/nonexistent/path/should/not/exist", false, true)

	if code != 130 {
		t.Errorf("code = %d, want 130 — runFmt did filesystem I/O before checking ctx.Err(); stderr=%q",
			code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tfdry: interrupted") {
		t.Errorf("stderr = %q, want to contain %q", stderr.String(), "tfdry: interrupted")
	}
}

// TestRunFmtFile_PreCancel_BailsBeforeIO is the file-mode counterpart
// of TestRunFmt_PreCancel_BailsBeforeIO. runFmtFile's entry check was
// added in PR A2 round 1 so this test passes today, but it guards
// against regression — moving the check below any I/O call would
// reintroduce the wasted-work bug.
func TestRunFmtFile_PreCancel_BailsBeforeIO(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := runFmtFile(ctx, &stdout, &stderr, "/nonexistent/path/should/not/exist", false)

	if code != 130 {
		t.Errorf("code = %d, want 130; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "tfdry: interrupted") {
		t.Errorf("stderr = %q, want to contain %q", stderr.String(), "tfdry: interrupted")
	}
}
