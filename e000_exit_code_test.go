package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRun_E000_FileExceedsSize_ExitTwo: ParseDir emits E000 for files
// larger than the max-file-size limit (parseOne path). This is also a
// tool-side concern — the file exists but tfdry can't process it safely
// — so it routes to exit 2.
func TestRun_E000_FileExceedsSize_ExitTwo(t *testing.T) {
	dir := t.TempDir()
	// Sparse file: 10 MB + 1 byte (the production threshold in
	// checker/hcl.go is 10 MB, strict >). Truncate is O(1) — we
	// only care about the file size for the limit check, not the
	// content. Same technique as TestParseDir_FileTooLarge_EmitsE000.
	path := filepath.Join(dir, "huge.tf")
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
	huge, err := os.Create(filepath.Join(dir, "huge.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := huge.Truncate(10*1024*1024 + 1); err != nil {
		_ = huge.Close()
		t.Fatal(err)
	}
	if err := huge.Close(); err != nil {
		t.Fatal(err)
	}

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
