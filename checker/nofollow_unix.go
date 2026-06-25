//go:build unix

// This file provides the Unix-side oNoFollow / isSymlinkRejection
// implementations. The `unix` build tag (Linux, macOS, BSD, illumos,
// AIX) matches the actual syscall surface: O_NOFOLLOW, ELOOP, and
// EMLINK are POSIX-derived and not available on Plan 9 or JS/wasm.
// nofollow_windows.go provides the no-op Windows fallback;
// non-Unix/non-Windows platforms intentionally fail to compile
// (tfdry targets darwin, linux, and windows only — see TODO.md
// "Distribution"). The Plan 9 / JS failures will be a clear
// "undefined: oNoFollow" rather than the confusing syscall errors
// the previous `!windows` tag produced.

package checker

import (
	"errors"
	"syscall"
)

// oNoFollow is the OS-level "do not follow symlinks" open flag.
//
// On Unix-like systems, opening with O_NOFOLLOW atomically rejects symlinks
// at the kernel level (returns ELOOP/EMLINK), eliminating the TOCTOU window
// between Lstat and the subsequent open or rename operations.
//
// Used in checker/hcl.go (parseOne), checker/format.go (writeFormatted),
// and checker/modules.go (parseModuleVarSchemas).
const oNoFollow = syscall.O_NOFOLLOW

// isSymlinkRejection reports whether err returned from os.OpenFile is the
// kernel's "would have followed a symlink" signal. On Unix this is ELOOP
// or EMLINK depending on filesystem type.
func isSymlinkRejection(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}
