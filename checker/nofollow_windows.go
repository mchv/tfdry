//go:build windows

package checker

// oNoFollow on Windows is 0 (no-op).
//
// Windows does not honour POSIX O_NOFOLLOW. Genuine symlink protection on
// Windows requires CreateFile with FILE_FLAG_OPEN_REPARSE_POINT and is
// outside the scope of the current build. Without it, the symlink check
// here degrades to "best effort": symlinks pointing at regular files will
// be silently followed, but the subsequent fi.Mode().IsRegular() check
// still rejects symlinks pointing at directories or devices.
//
// See TODO.md "Distribution" / "Windows support" for the proper
// implementation, which must land alongside Windows CI coverage.
const oNoFollow = 0

// isSymlinkRejection on Windows always returns false: without O_NOFOLLOW,
// the open never produces a "would have followed a symlink" error. Symlink
// rejection on Windows currently relies on the post-open IsRegular check.
func isSymlinkRejection(err error) bool {
	return false
}
