// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package checker

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests in this file rely on POSIX-specific behaviour — `os.Chmod` with
// numeric modes that revoke access (e.g. 0o000) — which Windows does not
// model the same way. On Windows, `os.Chmod` only toggles the readonly
// bit based on the owner-write bit, leaving the file fully readable.
// The unix build constraint isolates these tests cleanly instead of
// relying on a runtime `t.Skip` (which still compiles the file and
// pulls in unused imports on Windows builds).

// TestParseModuleVarSchemas_UnreadableFile_SkippedSilently — see
// parseModuleVarSchemas's per-entry continue-on-OpenFile-error branch
// (modules.go:107). A file that's syntactically valid but cannot be
// opened (mode 0o000) must be skipped silently, and a well-formed
// neighbour in the same directory must still be parsed.
func TestParseModuleVarSchemas_UnreadableFile_SkippedSilently(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode permissions; cannot exercise unreadable path")
	}
	dir := t.TempDir()
	// One good file with a valid schema, one unreadable file. The
	// good file must still be parsed even though the bad file is
	// silently skipped.
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
	t.Cleanup(func() { _ = os.Chmod(badPath, 0o644) }) // let t.TempDir clean up

	got := parseModuleVarSchemas(dir, nil)
	if _, ok := got["good"]; !ok {
		t.Errorf("good.tf must still be parsed: got %v", got)
	}
	if _, ok := got["bad"]; ok {
		t.Errorf("bad.tf (unreadable) must NOT appear in schemas: got %v", got)
	}
}
