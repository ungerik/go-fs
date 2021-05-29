// +build darwin dragonfly freebsd linux netbsd openbsd solaris

package fs

const localRoot = `/`

var extraDirPermissions Permissions = AllExecute

func hasLocalFileAttributeHidden(path string) (bool, error) {
	return false, nil
}
