package zipfs

import fs "github.com/ungerik/go-fs"

func init() {
	fs.Registry = append(fs.Registry, FileSystem)
}

const Prefix = "zip://"

var FileSystem ZipFileSystem

type ZipFileSystem struct {
}

func (ZipFileSystem) IsReadOnly() bool {
	return false
}

func (ZipFileSystem) Prefix() string {
	return Prefix
}

func (ZipFileSystem) Name() string {
	return "ZIP file system"
}

func (ZipFileSystem) File(uri ...string) fs.File {
	return nil
}
