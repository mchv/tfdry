// This test uses os.Chmod(dir, 0o000) to force ParseDir's os.ReadDir to
// fail with EACCES so it synthesises an E000 violation. Windows file
// permissions don't model POSIX read bits the same way (a 0o000 chmod
// is effectively a no-op for the user-side ACL evaluation), so the
// chmod can't drive ParseDir into the E000 path on Windows. Rather
// than handle that via a runtime.GOOS skip, the file is excluded from
// Windows compilation entirely — the intent is visible from the
// filename and the unused `runtime` import drops out of the sibling
// e000_exit_code_test.go.

//go:build !windows

package main

import (
	"os"
	"testing"
)

// TestRun_E000_DirectoryUnreadable_ExitTwo pins the contract that an E000
// violation (tool/infrastructure failure) routes to exit code 2, not 1.
// E000 is emitted by ParseDir when it can't read the directory itself —
// this is a "tool couldn't run" condition that's semantically different
// from "lint found issues in user code" (which is exit 1). The README and
// main.go's run() godoc both document exit 2 for "I/O failures (unreadable
// directories, write failures during --fix or fmt)".
//
// Before this fix, main.go's `if report.Summary.Errors > 0 { return 1 }`
// caught E000 alongside E001/E004/E007 etc., flattening the distinction
// and exiting 1 for what's really a tool error. Surfaced by Copilot C73
// during PR A2 review.
//
// Test technique: chmod the directory to 0o000 so os.ReadDir fails inside
// ParseDir, which then synthesises E000. Skip if running as root (chmod
// has no effect for the superuser).
func TestRun_E000_DirectoryUnreadable_ExitTwo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 doesn't restrict the superuser")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skip("cannot chmod:", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	code, _, stderr := runCLI(dir)
	if code != 2 {
		t.Errorf("E000 (unreadable dir) → exit code = %d, want 2 (tool error); stderr=%q",
			code, stderr)
	}
}
