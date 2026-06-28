// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

// These tests exercise the checker's behaviour around POSIX permission
// bits and Unix symlinks:
//
//   - TestE000_AlwaysEmitted_WhenDirUnreadable and
//     TestParseDir_UnreadableFile_EmitsE000 use os.Chmod(0o000) to
//     force EACCES from os.ReadDir / os.Open. Windows file ACLs don't
//     honour POSIX permission bits the same way (a 0o000 chmod is
//     effectively a no-op there), so the chmod can't drive the E000
//     path on Windows.
//   - TestParseDir_SymlinkSkipped relies on POSIX symlink semantics —
//     creating a symlink without elevated privileges (which doesn't
//     work on default Windows) plus the kernel-level O_NOFOLLOW
//     rejection in checker/nofollow_unix.go. The Windows variant uses
//     a post-open IsRegular() check that, today, doesn't trigger on
//     all symlink types — tracked separately in TODO.md as "Proper
//     Windows symlink protection".
//   - TestFixFormat_WriteError_ReturnsE000 uses os.Chmod(0o555) to
//     make a directory read-only so the rewrite path fails with
//     EROFS / EACCES. Same Windows permission-model issue.
//   - TestFormatFile_PreservesPermissions asserts that FormatFile
//     preserves the original 0o600 mode on the rewritten file.
//     Windows preserves mode at the simulated-bit level and exact
//     permission preservation is platform-specific.
//
// Use `unix` (not `!windows`) so the file is excluded from
// compilation on every non-Unix target rather than just Windows —
// matches the pattern established in sigint_test.go,
// e000_exit_code_unix_test.go, and checker/nofollow_unix.go. Plan 9
// and js/wasm also lack the POSIX permission model these tests rely
// on.

//go:build unix

package checker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// E000 (ParseDir infrastructure errors) must be emitted even when --checks
// does not include E001.
func TestE000_AlwaysEmitted_WhenDirUnreadable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 doesn't restrict the superuser")
	}
	dir := t.TempDir()
	// Make dir unreadable so ReadDir fails.
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skip("cannot chmod (running as root?)")
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, vs, _ := checker.ParseDir(context.Background(), dir)
	if !hasCode(vs, "E000") {
		t.Fatalf("expected E000 for unreadable dir, got %v", codes(vs))
	}
}

// ParseDir: unreadable file emits E000 with the underlying OS error in the
// message (so users can distinguish permission-denied from other failures).
func TestParseDir_UnreadableFile_EmitsE000(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o000 doesn't restrict the superuser")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.tf")
	if err := os.WriteFile(path, []byte(`locals { x = "y" }`), 0o000); err != nil {
		t.Fatal(err)
	}
	_, vs, _ := checker.ParseDir(context.Background(), dir)
	var e000 *checker.Violation
	for i := range vs {
		if vs[i].Code == "E000" {
			e000 = &vs[i]
			break
		}
	}
	if e000 == nil {
		t.Fatalf("expected E000 for unreadable file, got %v", codes(vs))
	}
	// Message must include the OS-level error so diagnostics can distinguish
	// EACCES vs ENOENT vs other failures.
	if !strings.Contains(e000.Message, "permission denied") &&
		!strings.Contains(e000.Message, "EACCES") {
		t.Errorf("E000 message should contain the underlying error, got %q", e000.Message)
	}
}

// ParseDir skips symlinks silently.
func TestParseDir_SymlinkSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a real file and a symlink to it.
	realPath := filepath.Join(dir, "real.tf")
	if err := os.WriteFile(realPath, []byte(`locals { x = "y" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.tf")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	files, vs, _ := checker.ParseDir(context.Background(), dir)
	// No E000 for the symlink — it should be silently skipped.
	for _, v := range vs {
		if v.Code == "E000" {
			t.Fatalf("unexpected E000 for symlink: %v", v.Message)
		}
	}
	// Only the real file should be parsed.
	if len(files) != 1 {
		t.Fatalf("expected 1 parsed file, got %d", len(files))
	}
}

// --fix must keep E008 in output when the file could not be
// written. Once the --fix path skips E008 in the initial Run pass for
// performance, FixFormat itself becomes the only emitter of E008 for
// unfixable files — without it, the user would see E000 (write error) but
// not E008 (file is still unformatted), losing the actionable signal.
func TestFixFormat_WriteError_ReturnsE000(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 0o555 doesn't prevent the superuser from writing")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "readonly")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("locals {\na=\"foo\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skip("cannot chmod:", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	files, _, _ := checker.ParseDir(context.Background(), dir)
	_, vs, _ := checker.FixFormat(context.Background(), files, dir)
	if !hasCode(vs, "E000") {
		t.Errorf("expected E000 when write fails, got %v", codes(vs))
	}
	if !hasCode(vs, "E008") {
		t.Errorf("expected E008 alongside E000 (file is still unformatted), got %v", codes(vs))
	}
}

// FormatFile must preserve original file permissions.
func TestFormatFile_PreservesPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte("locals {\na=\"foo\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.FormatFile(context.Background(), path, src); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("FormatFile changed permissions: got %o, want 0600", fi.Mode().Perm())
	}
}
