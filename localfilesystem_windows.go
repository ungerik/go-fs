package fs

import "syscall"

func hasFileAttributeHidden(path string) (bool, error) {
	p, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return false, e
	}
	attrs, e := syscall.GetFileAttributes(p)
	if e != nil {
		return false, e
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0, nil
}
