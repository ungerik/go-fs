//go:build js || wasip1

package fs

import (
	"errors"
	"os"
	"syscall"
)

const localRoot = `/`

// isCrossDeviceError reports whether the error is
// a cross-device link error (EXDEV) from os.Rename.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

var extraDirPermissions Permissions = AllExecute

// localHiddenAttribute returns false,
// there is no hidden attribute, only the dot rule.
func localHiddenAttribute(string, os.FileInfo) bool {
	return false
}

// localFileSystemID returns the constant "local",
// because WebAssembly has no file system id.
func localFileSystemID() string {
	return "local"
}
