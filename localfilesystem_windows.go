package fs

import "syscall"

const localRoot = `C:\`

var extraDirPermissions Permissions = 0

func hasFileAttributeHidden(filePath string) (bool, error) {
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
