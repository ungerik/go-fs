//go:build netbsd || solaris

package fs

import "golang.org/x/sys/unix"

// rootFileSystemID returns the f_fsid of the root directory from statvfs,
// NetBSD, Solaris and illumos have no statfs.
func rootFileSystemID() (any, error) {
	var stat unix.Statvfs_t
	if err := unix.Statvfs(localRoot, &stat); err != nil {
		return nil, err
	}
	return stat.Fsid, nil
}
