//go:build unix

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

var (
	_ UserFileSystem  = new(LocalFileSystem)
	_ GroupFileSystem = new(LocalFileSystem)
)

// isCrossDeviceError reports whether the error is
// a cross-device link error (EXDEV) from os.Rename.
func isCrossDeviceError(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// localLinkCount returns the number of hard links of the file from info.
func localLinkCount(info os.FileInfo) (links uint64, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(stat.Nlink), true
}

var extraDirPermissions Permissions = AllExecute

// localHiddenAttribute returns false,
// Unix has no hidden attribute, only the dot rule.
func localHiddenAttribute(string, os.FileInfo) bool {
	return false
}

// localFileSystemID returns the file system id of the root directory
// formatted as hex, or "/" if it can't be determined.
func localFileSystemID() string {
	fsid, err := rootFileSystemID()
	if err != nil {
		return localRoot
	}
	// The type of the id differs between platforms,
	// so format it and strip the decoration: "{[a b]}" -> "a-b"
	id := strings.Trim(fmt.Sprintf("%x", fsid), "{}[]")
	return strings.ReplaceAll(id, " ", "-")
}

// User returns the name of the user owning the file.
// Only available on Unix systems.
func (local *LocalFileSystem) User(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", wrapOSErr(filePath, err)
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

// SetUser changes the user owning the file.
// Only available on Unix systems.
func (local *LocalFileSystem) SetUser(filePath string, username string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	return wrapOSErr(filePath, os.Chown(filePath, uid, -1))
}

// Group returns the name of the group owning the file.
// Only available on Unix systems.
func (local *LocalFileSystem) Group(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", wrapOSErr(filePath, err)
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

// SetGroup changes the group owning the file.
// Only available on Unix systems.
func (local *LocalFileSystem) SetGroup(filePath string, group string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return err
	}
	return wrapOSErr(filePath, os.Chown(filePath, -1, gid))
}
