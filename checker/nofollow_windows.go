// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package checker

// oNoFollow on Windows is 0 (no-op).
//
// Windows does not honour POSIX O_NOFOLLOW. Read-only configuration loading
// intentionally follows symlinks to regular files on every platform. Formatting
// writes reject links with Lstat, but without CreateFile and
// FILE_FLAG_OPEN_REPARSE_POINT the protection against a concurrent link swap
// remains best-effort on Windows.
const oNoFollow = 0

// isSymlinkRejection on Windows always returns false: without O_NOFOLLOW,
// OpenFile cannot provide the Unix atomic rejection signal. Formatting writes
// rely on surrounding Lstat checks instead.
func isSymlinkRejection(err error) bool {
	return false
}

// Windows has no os.OpenFile flag equivalent needed here. A pre-open Stat and
// post-open file check reject non-regular targets; concurrent swaps remain a
// best-effort limitation on this platform.
const oReadNonblock = 0
