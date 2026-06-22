package checker

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// CheckFormat returns E008 violations for any ParsedFile whose source differs
// from its hclwrite-formatted form. Files that failed parsing (Src == nil) are skipped.
func CheckFormat(files []ParsedFile) []Violation {
	var violations []Violation
	for _, f := range files {
		if f.Src == nil {
			continue
		}
		if !bytes.Equal(f.Src, hclwrite.Format(f.Src)) {
			violations = append(violations, Violation{
				Code:     "E008",
				Severity: "error",
				File:     f.Name,
				Message:  "file is not formatted (run tfdry --fix or terraform fmt)",
			})
		}
	}
	return violations
}

// FormatFile writes the hclwrite-formatted version of src to path atomically,
// preserving the original file's permissions. Use [WriteFormatted] instead
// when the caller already has the formatted bytes to avoid running
// hclwrite.Format twice.
func FormatFile(path string, src []byte) error {
	formatted := hclwrite.Format(src)
	_, err := writeFormatted(path, formatted)
	return err
}

// WriteFormatted atomically writes pre-formatted bytes to path, preserving
// the original file's permissions. The caller is responsible for having
// already produced `formatted` via hclwrite.Format. Useful when a previous
// step in the pipeline (e.g. dirtiness detection in `tfdry fmt`) has
// already computed the formatted form, so we don't recompute it here.
//
// Atomicity vs metadata preservation: see [writeFormatted] godoc for the
// tradeoff (mode bits preserved; ownership / ACLs / xattrs reset on rename).
func WriteFormatted(path string, formatted []byte) error {
	_, err := writeFormatted(path, formatted)
	return err
}

// FixFormat rewrites all unformatted files in dir atomically.
// Returns the set of filenames successfully fixed and any E000/E008
// violations. Each file is formatted exactly once. When a write fails,
// both E000 (the write error itself) and E008 (the file is still
// unformatted) are appended so callers that suppressed E008 in the
// initial Run pass for performance (see main.go --fix path / G21+G22)
// still surface the actionable formatting violation to the user.
func FixFormat(files []ParsedFile, dir string) (fixed map[string]bool, violations []Violation) {
	fixed = make(map[string]bool)
	for _, f := range files {
		if f.Src == nil {
			continue
		}
		formatted := hclwrite.Format(f.Src) // computed once
		if bytes.Equal(f.Src, formatted) {
			continue
		}
		path := filepath.Join(dir, f.Name)
		if ok, err := writeFormatted(path, formatted); err != nil {
			violations = append(violations,
				Violation{
					Code:     "E000",
					Severity: "error",
					File:     f.Name,
					Message:  fmt.Sprintf("cannot write formatted file: %v", err),
				},
				Violation{
					Code:     "E008",
					Severity: "error",
					File:     f.Name,
					Message:  "file is not formatted (run tfdry --fix or terraform fmt)",
				},
			)
		} else if ok {
			fixed[f.Name] = true
		}
	}
	return fixed, violations
}

// writeFormatted atomically writes pre-formatted bytes to path.
// Uses O_NOFOLLOW open to atomically reject symlinks and obtain real
// permissions in one syscall (mirrors the pattern in hcl.go:parseOne).
// Returns (true, nil) on success, (false, err) on error.
//
// Atomicity tradeoff (C37): the implementation uses CreateTemp + Rename
// rather than an in-place truncating write. This guarantees the file is
// never observed in a half-written state — a power loss or SIGKILL mid-
// write leaves either the original content or the formatted content,
// never a truncated-and-empty .tf file that would break `terraform plan`.
//
// The cost is that Rename replaces the inode. Mode bits are preserved
// (we chmod the temp to match the original), but the following
// metadata is NOT preserved across the rename:
//   - File ownership (uid/gid) — temp is owned by the running user
//   - POSIX ACLs (where the filesystem supports them)
//   - Extended attributes (xattrs)
//   - Some filesystem-specific flags (e.g. immutable, append-only)
//
// In practice this matches what `terraform fmt`, `gofmt`, and `git`
// already do for the same reason: source files in version control are
// the dominant use case, where atomic safety beats metadata preservation.
// Users who rely on group-shared trees or extended ACLs should be aware
// that running `tfdry fmt` may strip those attributes on rewrite.
func writeFormatted(path string, formatted []byte) (bool, error) {
	// Cross-platform symlink rejection (G14): on Windows oNoFollow == 0 means
	// O_NOFOLLOW is a no-op and OpenFile silently follows symlinks. Without
	// this Lstat precheck, the subsequent os.Rename would destroy the symlink
	// and replace it with a regular file. Lstat introduces a small TOCTOU
	// window between the check and the open, but that's a fundamentally less
	// severe failure mode than silent symlink destruction. Unix is already
	// covered by O_NOFOLLOW below; the Lstat is defence in depth there.
	if li, err := os.Lstat(path); err == nil && li.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("not a regular file")
	}
	// Open with O_NOFOLLOW so a symlink at path is rejected atomically (ELOOP),
	// closing the small race window between Lstat and the subsequent operations.
	// On Windows oNoFollow = 0; the Lstat precheck above is the actual symlink
	// rejection on that platform (see checker/nofollow_windows.go).
	f, err := os.OpenFile(path, os.O_RDONLY|oNoFollow, 0)
	if err != nil {
		if isSymlinkRejection(err) {
			return false, fmt.Errorf("not a regular file")
		}
		return false, err
	}
	fi, err := f.Stat()
	f.Close() // we only needed the open() check + perms; the rename works on path
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("not a regular file")
	}
	perm := fi.Mode().Perm()
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tfdry-fmt-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()

	// Single deferred cleanup keyed on a success flag, so the temp file is
	// reliably reaped on every error path without four duplicated
	// `tmp.Close(); os.Remove(...)` calls. Once Rename succeeds the temp
	// file no longer exists at tmpName, so we skip cleanup entirely.
	// tmp.Close() is safe to call twice (the second returns EBADF, ignored).
	renamed := false
	defer func() {
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(formatted); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return false, err
	}
	// C41: TOCTOU defense-in-depth. The initial Lstat/OpenFile checks
	// established that `path` was a regular file, but between those
	// checks and this rename, an attacker (or concurrent process) with
	// write access to the parent directory could swap `path` for a
	// symlink. POSIX rename(2) replaces the entry at `path` without
	// following the symlink, so the symlink's target file is NOT
	// modified — but the user-visible result is still a surprise (a
	// regular file appears where the symlink used to be, instead of
	// where the user thought their .tf file was). A final Lstat right
	// before Rename closes the window and fails the operation when the
	// race occurs.
	//
	// Production calls leave writeFormattedBeforeRename nil; tests set
	// it to inject the swap deterministically between this hook and the
	// Lstat below, so the race path is exercised without flaky timing.
	if writeFormattedBeforeRename != nil {
		writeFormattedBeforeRename(path)
	}
	if li, err := os.Lstat(path); err == nil && !li.Mode().IsRegular() {
		return false, fmt.Errorf("not a regular file (raced)")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	renamed = true
	return true, nil
}

// writeFormattedBeforeRename is a test hook (C41). Production callers
// must leave it nil; tests may set it to simulate a TOCTOU race by
// swapping the target path between the initial regular-file checks and
// the final pre-Rename Lstat. The nil check adds negligible overhead
// (one indirect call per write) compared to the I/O cost of the actual
// rewrite.
var writeFormattedBeforeRename func(path string)
