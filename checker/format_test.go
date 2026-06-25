package checker_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// E008: unformatted file must be flagged.
func TestE008_UnformattedFile(t *testing.T) {
	dir := t.TempDir()
	// Deliberately bad formatting: wrong indentation, unaligned =
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`locals {
  a="foo"
  bb  =  "bar"
}
`), 0o644)
	files, pv, _ := checker.ParseDir(context.Background(), dir)
	vs := append(pv, mustRun(context.Background(), files, nil, dir)...)
	if !hasCode(vs, "E008") {
		t.Fatalf("expected E008 for unformatted file, got %v", codes(vs))
	}
}

// E008: already-formatted file must not be flagged.
func TestE008_FormattedFile_NoViolation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`locals {
  a  = "foo"
  bb = "bar"
}
`), 0o644)
	files, pv, _ := checker.ParseDir(context.Background(), dir)
	vs := append(pv, mustRun(context.Background(), files, nil, dir)...)
	if hasCode(vs, "E008") {
		t.Fatalf("unexpected E008 for already-formatted file: %v", codes(vs))
	}
}

// E008: file with syntax error (E001) must be skipped — no E008.
func TestE008_SyntaxError_Skipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.tf"), []byte(`locals { bad syntax !!!`), 0o644)
	_, pv, _ := checker.ParseDir(context.Background(), dir)
	if hasCode(pv, "E008") {
		t.Fatalf("E008 must not fire on files that failed E001: %v", codes(pv))
	}
}

// FormatFile: writes canonical bytes to disk.
func TestFormatFile_WritesFormattedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	unformatted := []byte(`locals {
  a="foo"
}
`)
	os.WriteFile(path, unformatted, 0o644)

	src, _ := os.ReadFile(path)
	if err := checker.FormatFile(context.Background(), path, src); err != nil {
		t.Fatalf("FormatFile error: %v", err)
	}

	got, _ := os.ReadFile(path)
	// After formatting, a = "foo" should be properly spaced.
	if string(got) == string(unformatted) {
		t.Fatal("FormatFile did not change the file content")
	}
	// Running again must be idempotent.
	if err := checker.FormatFile(context.Background(), path, got); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(path)
	if string(got) != string(got2) {
		t.Fatal("FormatFile is not idempotent")
	}
}

// ── writeFormatted error paths (T13) ─────────────────────────────────────────

// FormatFile on a path that doesn't exist: open fails with ENOENT.
func TestFormatFile_MissingFile_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.tf")
	err := checker.FormatFile(context.Background(), missing, []byte(`locals { x = "y" }`))
	if err == nil {
		t.Fatal("expected error when path does not exist, got nil")
	}
}

// FormatFile on a directory path: must reject with "not a regular file".
func TestFormatFile_DirectoryPath_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := checker.FormatFile(context.Background(), subdir, []byte(`locals { x = "y" }`))
	if err == nil {
		t.Fatal("expected error when path is a directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected 'not a regular file' error, got %v", err)
	}
}

// FormatFile when the parent dir is read-only: CreateTemp fails (EACCES).
// Skipped on root (chmod restrictions don't apply) and on Windows (unreliable).
func TestFormatFile_ReadOnlyParentDir_ReturnsError(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("read-only dir semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root — chmod 0555 doesn't restrict writes")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(`locals { x="y" }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Make the parent read-only AFTER the source file exists. writeFormatted
	// will open the source successfully but CreateTemp in the same dir fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	src, _ := os.ReadFile(path)
	err := checker.FormatFile(context.Background(), path, src)
	if err == nil {
		t.Fatal("expected error when parent dir is read-only, got nil")
	}
}
