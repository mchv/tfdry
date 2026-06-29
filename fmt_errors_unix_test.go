// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runFmtFile must reject symlinks upfront with exit 2. The earlier
// symlink check at runFmt (main.go:397) catches symlinked arguments
// to `fmt`; runFmtFile's own Lstat check (main.go:510-513) is a
// defensive belt-and-braces guard for callers that bypass runFmt.
//
// We can only observe the outer check from the CLI surface, which
// is what callers see. The inner check is exercised by code-path
// audit and stays uncovered via tests by design.
//
// Without these checks, `-check` would follow the symlink at
// os.ReadFile and exit 3 if the target was dirty, while a write pass
// would later destroy the symlink on Windows (where O_NOFOLLOW is a
// no-op). Reject upfront so the failure mode is identical across
// read/write/platforms.
//
// Unix-only: Windows symlink creation needs elevated privileges by
// default; the equivalent path on Windows is the post-open IsRegular()
// check in checker/nofollow_windows.go, which the TODO.md "Proper
// Windows symlink protection" entry tracks separately.
func TestRun_FmtFile_Symlink_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a real .tf file the symlink will point at.
	realPath := filepath.Join(dir, "real.tf")
	if err := os.WriteFile(realPath, []byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create the symlink. Skip rather than fail if symlink creation
	// is unavailable (e.g. running inside a container without
	// CAP_SYS_ADMIN); the test is meaningful only when symlinks work.
	link := filepath.Join(dir, "link.tf")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	code, _, stderr := runCLI("fmt", link)
	if code != 2 {
		t.Errorf("symlink fmt target should exit 2, got %d (stderr=%q)", code, stderr)
	}
	// runFmt catches symlinked args first; the message uses
	// "refusing to operate on symlinked path".
	if !strings.Contains(stderr, "symlinked path") {
		t.Errorf("stderr should mention symlinked-path rejection, got %q", stderr)
	}
}
