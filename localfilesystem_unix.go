package fs

const localRoot = `/`

var extraDirPermissions Permissions = AllExecute

func hasFileAttributeHidden(path string) (bool, error) {
	return false, nil
}
