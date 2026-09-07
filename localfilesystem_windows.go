package fs

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/windows"
)

const localRoot = `C:\`

// isCrossDeviceError reports whether the error is
// an ERROR_NOT_SAME_DEVICE error (0x11) from os.Rename
// when source and destination are on different volumes.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.Errno(0x11))
}

var extraDirPermissions Permissions = 0

// localFileSystemID returns the volume serial number of the system drive
// formatted as hex, or the root directory if it can't be determined.
func localFileSystemID() string {
	root, err := windows.UTF16PtrFromString(localRoot)
	if err != nil {
		return localRoot
	}
	var serial uint32
	err = windows.GetVolumeInformation(root, nil, 0, &serial, nil, nil, nil, 0)
	if err != nil {
		return localRoot
	}
	return fmt.Sprintf("%08x", serial)
}

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
