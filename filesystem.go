package fs

type FileSystem interface {
	IsReadOnly() bool
	Prefix() string
	Name() string
	File(uri ...string) File
}
