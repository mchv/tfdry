// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the fmt subcommand's argument-parsing and per-file
// error paths that the happy-path TestRun_Fmt* tests don't exercise.
// Each test exits with code 2 (tool error per the exit-code contract
// in SECURITY.md / SKILL.md).

// Two subcommands on the same invocation must fail with a clear
// "unexpected subcommand" message rather than silently picking one.
// Covers the dispatcher branch in main.go:184-187.
func TestRun_MultipleSubcommands_ExitTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		// describe then fmt: parser sees `fmt` after `describe` is bound.
		{"describe + fmt", []string{"describe", "fmt"}},
		// version then describe: same dispatcher path.
		{"version + describe", []string{"version", "describe"}},
		// fmt then version: order doesn't matter; the dispatcher
		// only allows one subcommand per invocation.
		{"fmt + version", []string{"fmt", "version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(tc.args...)
			if code != 2 {
				t.Errorf("expected exit 2, got %d (stderr=%q)", code, stderr)
			}
			if !strings.Contains(stderr, "unexpected subcommand") {
				t.Errorf("stderr should mention 'unexpected subcommand', got %q", stderr)
			}
		})
	}
}

// runFmtFile called with a nonexistent path must exit 2 with the
// os.ReadFile error in stderr (main.go:515-518).
func TestRun_FmtFile_NotFound_ExitTwo(t *testing.T) {
	dir := t.TempDir()
	// Path under temp dir that doesn't exist — guaranteed unique by t.TempDir.
	bogus := filepath.Join(dir, "definitely-not-there.tf")
	code, _, stderr := runCLI("fmt", bogus)
	if code != 2 {
		t.Errorf("nonexistent fmt target should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "tfdry fmt:") {
		t.Errorf("stderr should be prefixed 'tfdry fmt:', got %q", stderr)
	}
}

// runFmtFile called on a malformed .tf file must surface a parse
// diagnostic and exit 2 rather than silently formatting invalid HCL
// (main.go:531-550). Without this guard, single-file mode would
// best-effort token-reshuffle broken syntax and exit 0 — directly
// contradicting directory mode's E001 / exit 2 contract.
func TestRun_FmtFile_ParseError_ExitTwo(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		// Unclosed brace: HCL parser will emit a diagnostic.
		"broken.tf": "locals {\n  x =",
	})
	path := filepath.Join(dir, "broken.tf")
	code, _, stderr := runCLI("fmt", path)
	if code != 2 {
		t.Errorf("malformed .tf file should exit 2 in fmt single-file mode, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "Error:") {
		t.Errorf("stderr should print parse diagnostic with 'Error:' prefix, got %q", stderr)
	}
	// Must reference the file path so the user knows which file failed.
	if !strings.Contains(stderr, "broken.tf") {
		t.Errorf("stderr should include the filename 'broken.tf', got %q", stderr)
	}
}

// runFmtFile against a malformed .tf that has a diagnostic WITHOUT a
// subject (no source position info attached) — exercises the
// "if line > 0" else-branch at main.go:548-550 that prints the
// "Error: <path>: <msg>" form rather than "Error: <path>:<line>: <msg>".
// Most HCL diagnostics include Subject, so this branch is rarely
// reached but exists for defensive completeness; we hit it by
// feeding HCL that triggers a synthetic / position-less diagnostic.
//
// HCL's parser produces position-less diagnostics for certain
// out-of-band conditions, but they're hard to engineer reliably
// across versions of the hcl library. Skip a synthetic test rather
// than baking a fragile version-dependent fixture; the printing
// branch is trivially correct by inspection (it's a single
// fmt.Fprintf with one fewer placeholder than the line-included
// branch). Left here as a note for future contributors who hit a
// real diagnostic-without-subject case.
