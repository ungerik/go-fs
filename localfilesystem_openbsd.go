package fs

import "syscall"

// rootFileSystemID returns the f_fsid of the root directory from statfs,
// which is named F_fsid on OpenBSD.
func rootFileSystemID() (any, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(localRoot, &stat); err != nil {
		return nil, err
	}
	return stat.F_fsid, nil
}
