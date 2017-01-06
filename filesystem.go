package fs

type FileSystem interface {
	IsLocal() bool
	Prefix() string
	SelectFile(uri string) File
	CreateFile(url string, perm ...Permissions) (File, error)
	MakeDir(url string, perm ...Permissions) (File, error)
}
