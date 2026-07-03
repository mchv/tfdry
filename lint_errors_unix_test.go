// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_LintRecursive_SymlinkDirRoot_Rejected covers the counterpart
// to TestRun_Fmt_SymlinkArg_ExitTwo for the lint path: --recursive
// with a symlink-to-directory root would silently walk zero
// directories (filepath.WalkDir is Lstat-based and does not descend
// through a symlinked root), producing an empty report and exit 0.
// Reject explicitly to match runFmt's symlink discipline and
// eliminate the silent-no-op class of bug.
//
// Unix-only: same rationale as TestRun_Fmt_SymlinkArg_ExitTwo —
// Windows symlink creation needs elevated privileges by default,
// and the equivalent post-open protection path is documented in
// checker/nofollow_windows.go.
func TestRun_LintRecursive_SymlinkDirRoot_Rejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Create a real dir with a .tf file that would produce a
	// violation if scanned — so if the symlink rejection is broken,
	// we'd see the violation surface in stdout.
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "main.tf"),
		[]byte(`output "x" { value = local.missing }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	code, _, stderr := runCLI("--recursive", link)
	if code != 2 {
		t.Errorf("--recursive on symlink root should exit 2, got %d; stderr=%q",
			code, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr should mention symlink rejection, got %q", stderr)
	}
}

// TestRun_LintRecursive_UnreadableSubdir_ExitTwo covers the
// collectDirs walk-error branch. When filepath.WalkDir enters a
// subdirectory that cannot be readdir'd (e.g. chmod 0), it invokes
// the walkFn a second time with walkErr set; collectDirs returns
// that error and the lint dispatch routes it via handleFatalErr to
// exit 2 with a clear tool-error message. Without covering this
// branch, a filesystem-permission edge case could silently swallow
// diagnostics if the propagation chain were ever broken.
//
// Unix-only: chmod 0 on a subdirectory doesn't produce equivalent
// EACCES behaviour under Windows' ACL model; the run() surface is
// exercised on other platforms via the FileRoot / SymlinkDirRoot
// tests.
func TestRun_LintRecursive_UnreadableSubdir_ExitTwo(t *testing.T) {
	// No t.Parallel — chmod races with other tests are unlikely, but
	// serialising is cheap insurance.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.tf"),
		[]byte(`locals { x = "ok" }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a subdir and remove read+exec permission — WalkDir will
	// see it as a directory (Lstat mode intact) but the internal
	// os.ReadDir fails with EACCES.
	unreadable := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	// Add a .tf inside so if the walk didn't hit the perm error, we'd
	// visit the subdir and process it.
	if err := os.WriteFile(filepath.Join(unreadable, "hidden.tf"),
		[]byte(`locals { y = "would-be-seen" }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Skip("cannot chmod 0 on subdir:", err)
	}
	// Restore perms so t.TempDir()'s cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	code, _, stderr := runCLI("--recursive", root)
	if code != 2 {
		t.Errorf("--recursive over unreadable subdir should exit 2, got %d; stderr=%q",
			code, stderr)
	}
	if stderr == "" {
		t.Errorf("expected explanatory stderr, got empty")
	}
}
