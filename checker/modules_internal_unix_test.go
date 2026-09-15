// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package checker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Tests in this file rely on POSIX-specific behaviour — `os.Chmod` with
// numeric modes that revoke access (e.g. 0o000) — which Windows does not
// model the same way. On Windows, `os.Chmod` only toggles the readonly
// bit based on the owner-write bit, leaving the file fully readable.
// The unix build constraint isolates these tests cleanly instead of
// relying on a runtime `t.Skip` (which still compiles the file and
// pulls in unused imports on Windows builds).

// An unreadable selected module file makes the aggregate schema incomplete,
// so callers must receive nil and skip E006/E007 rather than infer absence.
func TestParseModuleVarSchemas_UnreadableFile_InvalidatesSchema(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions; cannot exercise unreadable path")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.tf"),
		[]byte(`variable "good" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(dir, "bad.tf")
	if err := os.WriteFile(badPath, []byte(`variable "bad" { type = number }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(badPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(badPath, 0o644) })

	if got := parseModuleVarSchemas(dir, nil); got != nil {
		t.Fatalf("schema with unreadable file = %v, want nil", got)
	}
}

// Symlinked module schema files that resolve to regular files are loaded.
func TestParseModuleVarSchemas_SymlinkedTerraformLoaded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "variables.tf")
	if err := os.WriteFile(target,
		[]byte(`variable "from_tf" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "variables.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	got := parseModuleVarSchemas(dir, nil)
	if got["from_tf"].Kind != schemaString {
		t.Fatalf("symlinked variables.tf was not loaded: %v", got)
	}
}

// A symlinked .tofu schema file remains authoritative over a regular
// same-basename .tf peer when loading relative-module variables.
func TestParseModuleVarSchemas_OpenTofuSymlinkShadowsTerraform(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "variables.tf"),
		[]byte(`variable "from_tf" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "variables.tofu")
	if err := os.WriteFile(target,
		[]byte(`variable "from_tofu" { type = number }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "variables.tofu")); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	got := parseModuleVarSchemas(dir, nil)
	if got["from_tofu"].Kind != schemaNumber {
		t.Fatalf("symlinked variables.tofu was not loaded: %v", got)
	}
	if _, ok := got["from_tf"]; ok {
		t.Fatalf("shadowed variables.tf contributed a schema: %v", got)
	}
}

func TestParseModuleVarSchemas_SymlinkToFIFOInvalidatesWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	targetDir := t.TempDir()
	fifo := filepath.Join(targetDir, "variables.tf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "variables.tf")); err != nil {
		t.Skip("cannot create symlink:", err)
	}

	done := make(chan map[string]typeSchema, 1)
	go func() { done <- parseModuleVarSchemas(dir, nil) }()
	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("schema from FIFO symlink = %v, want nil", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("parseModuleVarSchemas blocked opening a symlink to a FIFO")
	}
}

func TestParseModuleVarSchemas_OpenTofuNamedPipeInvalidatesSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "variables.tf"),
		[]byte(`variable "fallback" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "variables.tofu"), 0o600); err != nil {
		t.Skip("cannot create FIFO:", err)
	}
	if got := parseModuleVarSchemas(dir, nil); got != nil {
		t.Fatalf("schema with authoritative non-regular variables.tofu = %v, want nil", got)
	}
}
