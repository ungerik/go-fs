package fs

import (
	"errors"
	"syscall"
)

const localRoot = `C:\`

// isCrossDeviceError reports whether the error is
// an ERROR_NOT_SAME_DEVICE error (0x11) from os.Rename
// when source and destination are on different volumes.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.Errno(0x11))
}

var extraDirPermissions Permissions = 0

func hasLocalFileAttributeHidden(filePath string) (bool, error) {
	p, e := syscall.UTF16PtrFromString(filePath)
	if e != nil {
		return false, e
	}
	attrs, e := syscall.GetFileAttributes(p)
	if e != nil {
		return false, e
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0, nil
}
