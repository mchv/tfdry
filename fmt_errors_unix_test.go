// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRun_Fmt_SymlinkArg_ExitTwo exercises write-mode symlink rejection at
// the runFmt entry point. Read-only `fmt -check` intentionally follows links
// to regular files, but write mode must reject before any rename can replace
// the link itself.
func TestRun_Fmt_SymlinkArg_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create a real .tf file the symlink will point at.
	realPath := filepath.Join(dir, "real.tf")
	if err := os.WriteFile(realPath, []byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create the symlink. Skip rather than fail if symlink creation
	// is unavailable — common blockers are filesystem support (FAT,
	// some overlay/bind-mount setups don't model symlinks),
	// container sandbox restrictions (seccomp filters that deny
	// the symlinkat syscall), or read-only filesystems. The test
	// is meaningful only when symlinks work; on a hostile
	// environment, skipping is the right signal.
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

// TestRun_Fmt_WriteError_ExitTwo covers runFmtFile's single-file
// WriteFormatted error path (main.go:566-572): the branch where
// WriteFormatted returns a non-cancellation error after a successful
// parse + format. The directory-mode equivalent
// (TestFixFormat_WriteError_ReturnsE000 in checker/format_internal_test.go)
// exercises the same defence at the inner-loop level; this test
// pins the single-file CLI surface.
//
// Strategy: write a malformed-but-parseable .tf file (so it triggers
// the rewrite path), then make the *parent directory* read-only
// AFTER the file is read but before the atomic-rename's temp-file
// creation. WriteFormatted creates `path.tmpXXXXXX` in the parent
// dir, so a read-only parent breaks the temp-file step.
//
// Unix-only: relies on chmod 0o500 (rx, no w) on a directory
// behaving as POSIX expects. Skips if running as root (root
// bypasses directory permissions).
func TestRun_Fmt_WriteError_ExitTwo(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; cannot block writes")
	}
	dir := t.TempDir()
	// Unformatted content — fmt will want to rewrite. Use deliberate
	// extra whitespace and odd indentation so gofumpt's normaliser
	// kicks in.
	path := filepath.Join(dir, "needs_format.tf")
	if err := os.WriteFile(path, []byte("locals{x=1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Lock the parent dir read-only AFTER writing the file. fmt will
	// read the file (still works — read permission is unaffected by
	// the dir's write bit), reformat in memory, then try to create
	// the temp file alongside the target — which needs the parent's
	// write bit. That step fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Restore writable permissions so t.TempDir's RemoveAll can clean up.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	code, _, stderr := runCLI("fmt", path)
	if code != 2 {
		t.Errorf("WriteFormatted error in single-file mode should exit 2, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "Error formatting") {
		t.Errorf("stderr should contain 'Error formatting' prefix, got %q", stderr)
	}
}

// TestRun_Fmt_SymlinkArgTrailingSlash_ExitTwo is the fmt-side
// counterpart of TestRun_LintRecursive_SymlinkDirRootTrailingSlash_Rejected.
// The same POSIX trailing-slash quirk that bypassed the recursive-
// lint symlink check also affected `tfdry fmt --recursive link/`
// (main.go:512-515). os.Lstat("link/") resolves the symlink to
// the target directory (Mode & ModeSymlink == 0) so the guard
// silently passes; filepath.WalkDir then sees the symlink on the
// cleaned path and doesn't recurse, producing an empty walk and
// exit 0. Fix mirrors the lint side: filepath.Clean before the
// Lstat check.
//
// Unix-only: same rationale as sibling symlink tests.
func TestRun_Fmt_SymlinkArgTrailingSlash_ExitTwo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "a.tf"),
		[]byte("locals { x = 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	code, _, stderr := runCLI("fmt", "--recursive", link+"/")
	if code != 2 {
		t.Errorf("fmt --recursive on symlink-with-trailing-slash root should exit 2, got %d; stderr=%q",
			code, stderr)
	}
	if !strings.Contains(stderr, "symlinked path") {
		t.Errorf("stderr should mention symlinked-path rejection, got %q", stderr)
	}
}

func TestRun_Fmt_FIFOPath_RefusesWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "main.tf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	for _, args := range [][]string{{"fmt", fifo}, {"fmt", "-check", fifo}} {
		args := args
		t.Run(strings.Join(args[:len(args)-1], "_"), func(t *testing.T) {
			done := make(chan int, 1)
			go func() {
				code, _, _ := runCLI(args...)
				done <- code
			}()
			select {
			case code := <-done:
				if code != 2 {
					t.Fatalf("runCLI(%v) = %d, want 2", args, code)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("runCLI(%v) blocked opening FIFO", args)
			}
		})
	}
}
