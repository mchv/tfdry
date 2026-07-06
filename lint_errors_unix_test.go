// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"encoding/json"
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

// TestRun_LintRecursive_UnreadableSubdir_EmitsE000InReport locks in
// the contract that a walk hitting an unreadable subdirectory
// surfaces the failure as an E000 violation in the aggregated
// Report — matching what non-recursive lint does for
// `tfdry --json <chmod-0-dir>`. Previously, `collectDirs` returned
// WalkDir errors verbatim, which the lint dispatch treated as fatal
// and exited 2 with a stderr message and no JSON output. That
// broke the "single Report even on tool errors" contract for
// `--json --recursive` consumers.
//
// The fix (in `collectDirs`) treats per-directory readdir failures
// as non-fatal: the directory has already been added to the walk's
// dirs slice on the first walkFn call, so the lint loop's ParseDir
// re-hits the readdir failure and emits E000 with the directory
// path in v.File. That E000 aggregates into the Report alongside
// any other violations. Exit code stays 2 (via ToolErrors > 0).
//
// Unix-only: chmod 0 on a subdirectory doesn't produce equivalent
// EACCES behaviour under Windows' ACL model; the run() surface is
// exercised on other platforms via the FileRoot / SymlinkDirRoot
// tests.
func TestRun_LintRecursive_UnreadableSubdir_EmitsE000InReport(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0 doesn't restrict the superuser")
	}
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
	// Add a .tf inside so if the walk somehow bypassed the perm
	// error, we'd visit the subdir and process it.
	if err := os.WriteFile(filepath.Join(unreadable, "hidden.tf"),
		[]byte(`locals { y = "would-be-seen" }`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Skip("cannot chmod 0 on subdir:", err)
	}
	// Restore perms so t.TempDir()'s cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	code, stdout, stderr := runCLI("--json", "--recursive", root)
	if code != 2 {
		t.Fatalf("--recursive over unreadable subdir should exit 2, got %d; "+
			"stdout=%q stderr=%q", code, stdout, stderr)
	}
	// The Report should be emitted on stdout, containing an E000 for
	// the unreadable subdir. Stderr should be empty — errors flow
	// through the Report, matching the non-recursive contract.
	if stderr != "" {
		t.Errorf("stderr should be empty (errors flow via Report), got %q", stderr)
	}
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout must be valid JSON Report: %v\noutput: %s", err, stdout)
	}
	foundE000 := false
	for _, v := range got.Violations {
		code, _ := v["code"].(string)
		file, _ := v["file"].(string)
		if code == "E000" && strings.Contains(file, "unreadable") {
			foundE000 = true
		}
	}
	if !foundE000 {
		t.Errorf("expected E000 violation for the 'unreadable' subdir in the JSON "+
			"Report; got violations: %+v", got.Violations)
	}
}

// TestRun_LintRecursive_SymlinkDirRootTrailingSlash_Rejected covers the
// POSIX trailing-slash quirk that bypasses the symlink check when the
// user supplies the symlink with a `/` suffix. On POSIX, os.Lstat
// on a path with a trailing slash resolves the symlink (returns info
// about the target directory), so `Mode() & ModeSymlink` is 0 and
// the symlink-rejection guard would silently pass. The subsequent
// filepath.WalkDir then calls Lstat internally on the *cleaned* path
// (no trailing slash), sees the symlink, and does not recurse into
// it — resulting in an empty walk and a silent exit-0 no-op. The fix
// is to filepath.Clean the CLI arg BEFORE the Lstat check so both
// call sites see the same shape.
//
// Companion to TestRun_LintRecursive_SymlinkDirRoot_Rejected (no
// trailing slash); this one specifically pins the trailing-slash
// variant so the bypass can't regress.
//
// Unix-only: same rationale as the sibling test — Windows symlink
// creation needs elevated privileges by default.
func TestRun_LintRecursive_SymlinkDirRootTrailingSlash_Rejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
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

	code, _, stderr := runCLI("--recursive", link+"/")
	if code != 2 {
		t.Errorf("--recursive on symlink-with-trailing-slash root should exit 2, got %d; stderr=%q",
			code, stderr)
	}
	if !strings.Contains(stderr, "symlink") {
		t.Errorf("stderr should mention symlink rejection, got %q", stderr)
	}
}

// TestRun_LintNonRecursive_DirLevelE000_PreservesDirectoryPath guards
// the non-recursive contract for directory-level E000 violations:
// when ParseDir can't read the target directory itself, the emitted
// violation's `file` field must be the directory path (as it was in
// v0.1.1), not the "." sentinel that `displayPath` returns for the
// `vFile == dir` case.
//
// Regression window: after the recursive-lint dispatch refactor
// (issue #21), the per-directory `displayPath` transformation was
// applied unconditionally. In non-recursive mode with dirs =
// [rootClean] and a dir-level E000 where v.File == rootClean, the
// transformation compressed the path to "." — silently changing the
// JSON schema and human-output for the exact tool-error case that
// downstream consumers (CI, dashboards) rely on for correct
// attribution. The fix is to gate the displayPath loop on the
// `recursive` flag so non-recursive lint preserves its v0.1.1
// output byte-for-byte.
//
// Unix-only: chmod 0 is the deterministic way to trigger the
// dir-level E000 branch inside ParseDir (os.ReadDir fails).
func TestRun_LintNonRecursive_DirLevelE000_PreservesDirectoryPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0 doesn't restrict the superuser")
	}
	// No t.Parallel — chmod modifies a real directory.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skip("cannot chmod:", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	code, stdout, _ := runCLI("--json", dir)
	if code != 2 {
		t.Fatalf("expected exit 2 (E000), got %d; stdout=%q", code, stdout)
	}
	var got struct {
		Violations []map[string]any `json:"violations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if len(got.Violations) == 0 {
		t.Fatalf("expected at least one E000 violation, got none; output: %s", stdout)
	}
	foundE000 := false
	for _, v := range got.Violations {
		code, _ := v["code"].(string)
		file, _ := v["file"].(string)
		if code != "E000" {
			continue
		}
		foundE000 = true
		if file == "." {
			t.Errorf("dir-level E000 file field was compressed to %q by displayPath; "+
				"expected the directory path %q (v0.1.1 contract)", file, dir)
		}
		// The path should be either the raw dir arg or its cleaned
		// form — both are acceptable per v0.1.1 which passed the arg
		// directly to ParseDir which cleans internally.
		if file != dir && file != filepath.Clean(dir) {
			t.Errorf("dir-level E000 file = %q; expected %q or its Clean form", file, dir)
		}
	}
	if !foundE000 {
		t.Errorf("no E000 violation in output; got: %+v", got.Violations)
	}
}
