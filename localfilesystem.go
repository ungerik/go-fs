package fs

const LocalPrefix = "file://"

type LocalFileSystem struct {
	DefaultCreatePermissions Permissions
}

func (LocalFileSystem) IsLocal() bool {
	return true
}

func (LocalFileSystem) IsReadOnly() bool {
	return false
}

func (LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (LocalFileSystem) File(uri string) File {
	return newLocalFile(uri)
}
