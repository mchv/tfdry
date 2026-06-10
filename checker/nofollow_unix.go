//go:build !windows

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
