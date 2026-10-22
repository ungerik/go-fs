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

// localLinkCount returns the number of hard links of the file from info.
func localLinkCount(info os.FileInfo) (links uint64, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
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
