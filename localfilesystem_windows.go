package fs

import (
	"errors"
	"fmt"
	"os"
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

// localLinkCount returns false, because the Sys value
// of a FileInfo on Windows has no link count.
func localLinkCount(os.FileInfo) (links uint64, ok bool) {
	return 0, false
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

// localHiddenAttribute reports if the file has the FILE_ATTRIBUTE_HIDDEN
// attribute. It is read from info if not nil, else from the file at filePath.
// A file whose attributes can't be read is not hidden.
func localHiddenAttribute(filePath string, info os.FileInfo) bool {
	if info != nil {
		if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
			return data.FileAttributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
		}
	}
	p, err := syscall.UTF16PtrFromString(filePath)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}
