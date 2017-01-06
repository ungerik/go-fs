package zipfs

import fs "github.com/ungerik/go-fs"

func init() {
	fs.Registry = append(fs.Registry, FileSystem)
}

const Prefix = "zip://"

var FileSystem ZipFileSystem

type ZipFileSystem struct {
}

func (ZipFileSystem) IsLocal() bool {
	return true
}

func (ZipFileSystem) IsReadOnly() bool {
	return false
}

func (ZipFileSystem) Prefix() string {
	return Prefix
}

func (ZipFileSystem) File(uri string) fs.File {
	return nil
}
