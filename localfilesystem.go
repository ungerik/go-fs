package fs

const LocalPrefix = "file://"

type LocalFileSystem struct {
	DefaultCreatePermissions Permissions
}

func (LocalFileSystem) IsReadOnly() bool {
	return false
}

func (LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (LocalFileSystem) Name() string {
	return "Local file system"
}

func (LocalFileSystem) File(uri ...string) File {
	return newLocalFile(uri)
}
