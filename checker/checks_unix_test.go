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
//   - Symlink read tests rely on POSIX link creation without elevated
//     privileges. Read-only loading follows links to regular files, while
//     FixFormat retains its no-follow write guard.
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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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

// Directory scans follow symlinks to regular native-HCL files for read-only
// loading, matching Terraform/OpenTofu module semantics.
func TestParseDir_SymlinkedTerraformLoaded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "locals.tf")
	if err := os.WriteFile(target, []byte(`locals { shared = "ok" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "locals.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte(`output "x" { value = local.shared }`), 0o644); err != nil {
		t.Fatal(err)
	}

	files, parseViolations, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parseViolations) != 0 {
		t.Fatalf("unexpected parse violations: %+v", parseViolations)
	}
	if len(files) != 2 {
		t.Fatalf("parsed files = %d, want 2", len(files))
	}
	vs := mustRun(context.Background(), files, nil, dir)
	if hasCode(vs, "E003") {
		t.Fatalf("symlinked local was not loaded: %v", codes(vs))
	}
}

// A symlinked .tofu file that resolves to a regular file remains authoritative
// over a same-basename .tf peer, matching OpenTofu extension precedence.
func TestParseDir_OpenTofuSymlinkShadowsTerraform(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte(`output "x" { value = local.missing }`), 0o644); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "main.tofu")
	if err := os.WriteFile(target, []byte(`output "x" { value = "tofu" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "main.tofu")); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	files, parseViolations, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parseViolations) != 0 {
		t.Fatalf("unexpected parse violations: %+v", parseViolations)
	}
	if len(files) != 1 || files[0].Name != "main.tofu" {
		t.Fatalf("parsed files = %+v, want authoritative main.tofu", files)
	}
	vs := mustRun(context.Background(), files, nil, dir)
	if hasCode(vs, "E003") {
		t.Fatalf("shadowed main.tf was loaded: %v", codes(vs))
	}
}

func TestParseDir_BrokenSymlinkEmitsE000(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing.tf"), filepath.Join(dir, "main.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	files, vs, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("parsed files = %d, want 0", len(files))
	}
	if !hasCode(vs, "E000") {
		t.Fatalf("broken symlink must emit E000, got %v", codes(vs))
	}
}

func TestParseDirForFormat_SymlinkedFileCheckedReadOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "main.tf")
	dirty := []byte("locals {x=1}\n")
	if err := os.WriteFile(target, dirty, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "main.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	files, parseViolations, err := checker.ParseDirForFormat(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parseViolations) != 0 {
		t.Fatalf("unexpected parse violations: %+v", parseViolations)
	}
	formatViolations, err := checker.CheckFormat(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(formatViolations, "E008") {
		t.Fatalf("read-only format check missed dirty symlink target: %v", codes(formatViolations))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dirty) {
		t.Fatalf("read-only format check modified target: %q", got)
	}
}

func TestFixFormat_SymlinkedFileRefusesWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "main.tf")
	dirty := []byte("locals {x=1}\n")
	if err := os.WriteFile(target, dirty, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "main.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	files, parseViolations, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parseViolations) != 0 {
		t.Fatalf("unexpected parse violations: %+v", parseViolations)
	}
	_, vs, err := checker.FixFormat(context.Background(), files, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(vs, "E000") || !hasCode(vs, "E008") {
		t.Fatalf("dirty symlink write must emit E000 and E008, got %v", codes(vs))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dirty) {
		t.Fatalf("symlink target was modified: %q", got)
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

func TestParseDir_SymlinkToFIFOEmitsE000WithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	fifo := filepath.Join(targetDir, "target.tf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "main.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	done := make(chan []checker.Violation, 1)
	go func() {
		_, vs, _ := checker.ParseDir(context.Background(), dir)
		done <- vs
	}()
	select {
	case vs := <-done:
		if !hasCode(vs, "E000") {
			t.Fatalf("FIFO symlink must emit E000, got %v", codes(vs))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ParseDir blocked opening a symlink to a FIFO")
	}
}

func TestParseDir_OpenTofuNamedPipeShadowsTerraformAndEmitsE000(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte(`output "x" { value = "terraform" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "main.tofu"), 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	files, vs, err := checker.ParseDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("fallback Terraform file was loaded: %+v", files)
	}
	if !hasCode(vs, "E000") {
		t.Fatalf("non-regular authoritative OpenTofu file must emit E000, got %v", codes(vs))
	}
}

func TestFormatFile_FIFORefusesWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "main.tf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- checker.FormatFile(context.Background(), fifo, []byte("locals { x = 1 }\n"))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FormatFile(FIFO) = nil, want error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("FormatFile blocked opening FIFO")
	}
}
