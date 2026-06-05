package checker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

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
// preserving the original file's permissions.
func FormatFile(path string, src []byte) error {
	formatted := hclwrite.Format(src)
	_, err := writeFormatted(path, formatted)
	return err
}

// FixFormat rewrites all unformatted files in dir atomically.
// Returns the set of filenames successfully fixed and any E000 write-error violations.
// Each file is formatted exactly once.
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
			violations = append(violations, Violation{
				Code:     "E000",
				Severity: "error",
				File:     f.Name,
				Message:  fmt.Sprintf("cannot write formatted file: %v", err),
			})
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
func writeFormatted(path string, formatted []byte) (bool, error) {
	// Open with O_NOFOLLOW so a symlink at path is rejected atomically (ELOOP),
	// closing the small race window between Lstat and the subsequent operations.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
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
	if _, err := tmp.Write(formatted); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return false, err
	}
	return true, nil
}
