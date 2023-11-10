//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris
// +build darwin dragonfly freebsd linux netbsd openbsd solaris

package fs

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

const localRoot = `/`

var extraDirPermissions Permissions = AllExecute

func hasLocalFileAttributeHidden(path string) (bool, error) {
	return false, nil
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
