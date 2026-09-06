//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fs

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

const localRoot = `/`

// isCrossDeviceError reports whether the error is
// a cross-device link error (EXDEV) from os.Rename.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

var extraDirPermissions Permissions = AllExecute

func hasLocalFileAttributeHidden(string) (bool, error) {
	return false, nil
}

// localFileSystemID returns the statfs f_fsid of the root directory
// formatted as hex, or "/" if it can't be determined.
func localFileSystemID() string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(localRoot, &stat); err != nil {
		return localRoot
	}
	// The field name of the fsid array differs between platforms,
	// so format the struct and strip the decoration: "{[a b]}" -> "a-b"
	id := strings.Trim(fmt.Sprintf("%x", stat.Fsid), "{}[]")
	return strings.ReplaceAll(id, " ", "-")
}

func (local *LocalFileSystem) User(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	filePath = expandTilde(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", NewErrUnsupported(local, "User")
	}
	u, err := user.LookupId(fmt.Sprint(stat.Uid))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func (local *LocalFileSystem) SetUser(filePath string, username string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)

	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	return os.Chown(filePath, uid, -1)
}

func (local *LocalFileSystem) Group(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	filePath = expandTilde(filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", NewErrUnsupported(local, "Group")
	}
	g, err := user.LookupGroupId(fmt.Sprint(stat.Gid))
	if err != nil {
		return "", err
	}
	return g.Name, nil
}

func (local *LocalFileSystem) SetGroup(filePath string, group string) error {
	filePath = expandTilde(filePath)

	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)

	g, err := user.LookupGroup(group)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return err
	}
	return os.Chown(filePath, -1, gid)
}
