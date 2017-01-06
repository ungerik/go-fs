package httpfs

import "github.com/ungerik/go-fs"

func init() {
	fs.Registry = append(fs.Registry, FileSystem, FileSystemTLS)
}

const (
	Prefix    = "http://"
	PrefixTLS = "https://"
)

var (
	FileSystem    = HTTPFileSystem{Prefix}
	FileSystemTLS = HTTPFileSystem{PrefixTLS}
)

type HTTPFileSystem struct {
	prefix string
}

func (HTTPFileSystem) IsLocal() bool {
	return false
}

func (HTTPFileSystem) IsReadOnly() bool {
	return true
}

func (f HTTPFileSystem) Prefix() string {
	return f.prefix
}

func (HTTPFileSystem) File(uri string) fs.File {
	return nil
}
