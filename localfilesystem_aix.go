package fs

import "golang.org/x/sys/unix"

// rootFileSystemID returns the f_fsid of the root directory from statfs,
// the syscall package has no statfs on AIX.
func rootFileSystemID() (any, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(localRoot, &stat); err != nil {
		return nil, err
	}
	return stat.Fsid, nil
}
