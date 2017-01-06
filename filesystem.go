package fs

type FileSystem interface {
	IsLocal() bool
	IsReadOnly() bool
	Prefix() string
	File(uri string) File
}
