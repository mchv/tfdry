// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// createOversizedFile creates a sparse file at dir/name that's 10 MB + 1
// byte — one byte over the production threshold in checker/hcl.go. Used
// across the three tests below that need to drive ParseDir into the
// "file too large" E000 path. Truncate is O(1) (sparse file allocation)
// so the helper is cheap to call in multiple tests, and the content is
// irrelevant — the size check fires before any byte is read.
//
// t.Fatal on any error: the test wouldn't make sense without the file.
func createOversizedFile(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(10*1024*1024 + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRun_E000_FileExceedsSize_ExitTwo: ParseDir emits E000 for files
// larger than the max-file-size limit (parseOne path). This is also a
// tool-side concern — the file exists but tfdry can't process it safely
// — so it routes to exit 2.
func TestRun_E000_FileExceedsSize_ExitTwo(t *testing.T) {
	dir := t.TempDir()
	createOversizedFile(t, dir, "huge.tf")

	code, _, stderr := runCLI(dir)
	if code != 2 {
		t.Errorf("E000 (file > size limit) → exit code = %d, want 2 (tool error); stderr=%q",
			code, stderr)
	}
}

// TestRun_E000_PrecedesOtherErrors_ExitTwo: when both E000 and other
// error-severity violations (E001, E004, etc.) are present, the E000
// "tool couldn't run cleanly" signal wins. Otherwise the user sees
// exit 1 ("found violations") and might dismiss the result as normal
// lint output, missing that some files weren't actually checked.
func TestRun_E000_PrecedesOtherErrors_ExitTwo(t *testing.T) {
	dir := t.TempDir()
	// One file with a parse error (E001 — invalid HCL syntax).
	if err := os.WriteFile(filepath.Join(dir, "bad.tf"),
		[]byte("this is not valid hcl !!!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One file too large to parse — generates E000.
	createOversizedFile(t, dir, "huge.tf")

	code, _, stderr := runCLI(dir)
	if code != 2 {
		t.Errorf("E000 mixed with E001 → exit code = %d, want 2 (E000 wins); stderr=%q",
			code, stderr)
	}
}

// TestRun_NoE000_OnlyLintErrors_ExitOne is the regression guard for the
// existing exit-1 behaviour. After the fix, runs with NO E000 violations
// must still exit 1 on lint errors — we're narrowing the exit-1 case to
// "lint found issues", not redefining it.
func TestRun_NoE000_OnlyLintErrors_ExitOne(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `output "x" {
  value = local.does_not_exist
}
`,
	})
	code, _, stderr := runCLI(dir)
	if code != 1 {
		t.Errorf("lint errors only → exit code = %d, want 1 (regression: exit 1 must still fire for non-E000 errors); stderr=%q",
			code, stderr)
	}
}

// TestRun_E000_JSONOutput_IncludesToolErrors asserts the user-facing
// JSON shape includes the new `summary.tool_errors` field that we
// document in README.md and SKILL.md. Without this test, a future
// refactor could silently drop the field (e.g. removing it from the
// Summary struct, changing the json tag) and the documented contract
// would diverge from the runtime output. The existing
// TestRun_JSONOutput_Shape test uses a partial-fields struct that
// ignores extra fields, so it wouldn't catch the omission.
//
// We trigger E000 via the same oversize-file technique as
// TestRun_E000_FileExceedsSize_ExitTwo, then verify:
//   - summary.tool_errors > 0  (the new field is present and counted)
//   - summary.errors > 0       (E000 is also tallied in the legacy count
//     for human-output / back-compat)
//   - exit code 2              (E000 routes to the tool-error exit)
func TestRun_E000_JSONOutput_IncludesToolErrors(t *testing.T) {
	dir := t.TempDir()
	createOversizedFile(t, dir, "huge.tf")

	code, stdout, stderr := runCLI("--json", dir)
	if code != 2 {
		t.Fatalf("expected exit 2 for E000, got %d; stderr=%q", code, stderr)
	}

	var got struct {
		Summary struct {
			Errors     int `json:"errors"`
			Warnings   int `json:"warnings"`
			ToolErrors int `json:"tool_errors"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if got.Summary.ToolErrors == 0 {
		t.Errorf("summary.tool_errors = 0, want > 0 (E000 should be counted as tool error); summary=%+v",
			got.Summary)
	}
	if got.Summary.Errors == 0 {
		t.Errorf("summary.errors = 0, want > 0 (E000 should also count as error for back-compat); summary=%+v",
			got.Summary)
	}
}

// TestRun_E000_JSONOutput_OmitsLineField asserts that file-level
// violations (E000, emitted from the I/O layer before HCL parsing
// resolves any line number) produce JSON entries with NO "line" key
// at all, per the `json:"line,omitempty"` tag on
// checker.Violation.Line. The README's JSON schema row documents
// this as "1-based line number; omitted when the violation is
// file-level (e.g. E000)"; without this test, a future struct
// refactor that drops omitempty would silently emit "line": 0 and
// diverge from the documented contract — and the existing
// TestRun_E000_JSONOutput_IncludesToolErrors only inspects summary
// fields, not the violation entries themselves.
func TestRun_E000_JSONOutput_OmitsLineField(t *testing.T) {
	dir := t.TempDir()
	createOversizedFile(t, dir, "huge.tf")

	code, stdout, stderr := runCLI("--json", dir)
	if code != 2 {
		t.Fatalf("expected exit 2 for E000, got %d; stderr=%q", code, stderr)
	}

	// Decode into a generic map so we can detect the *presence* of
	// keys, not just their values — a partial-fields struct would
	// happily accept "line": 0 and miss the omitempty regression.
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if len(got.Violations) == 0 {
		t.Fatalf("expected at least one violation, got none; output: %s", stdout)
	}
	for i, v := range got.Violations {
		if v["code"] != "E000" {
			continue
		}
		if _, hasLine := v["line"]; hasLine {
			t.Errorf("violation[%d] (E000) has unexpected \"line\" key (omitempty should have stripped it); violation=%+v", i, v)
		}
	}
}
