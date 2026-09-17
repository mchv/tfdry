// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

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
// On Unix-like systems, opening with O_NOFOLLOW atomically rejects a
// formatting target that became a symlink between the Lstat check and open.
// Read-only configuration loading intentionally follows regular-file links;
// this flag is used only by checker/format.go's write path.
const oNoFollow = syscall.O_NOFOLLOW

// isSymlinkRejection reports whether err returned from os.OpenFile is the
// kernel's "would have followed a symlink" signal. On Unix this is ELOOP
// or EMLINK depending on filesystem type.
func isSymlinkRejection(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// oReadNonblock prevents a raced or symlinked FIFO from blocking before the
// post-open regular-file check. It has no effect on regular files.
const oReadNonblock = syscall.O_NONBLOCK
