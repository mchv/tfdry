// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package checker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// FormatFile when the parent dir is read-only: CreateTemp fails (EACCES).
// Lives in a unix-only file because chmod 0o555 only restricts writes on
// POSIX systems; Windows ignores the mode (the readonly bit alone doesn't
// gate directory-create-temp-file). Previously this test sat in
// format_test.go with a runtime.GOOS=="windows" t.Skip, but the project
// convention is to gate via //go:build unix so platform constraints are
// visible from filenames and there are no unused imports on the
// non-applicable build.
//
// Skipped at runtime on root (chmod restrictions don't apply).
func TestFormatFile_ReadOnlyParentDir_ReturnsError(t *testing.T) {
	t.Parallel()
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

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = checker.FormatFile(context.Background(), path, src)
	if err == nil {
		t.Fatal("expected error when parent dir is read-only, got nil")
	}
}
