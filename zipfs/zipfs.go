package zipfs

import "github.com/ungerik/go-fs"

const Prefix = "zip://"

type ZipFileSystem struct {
}

func FromFile(zipFile fs.File) (*ZipFileSystem, error) {
	return nil, nil
}

func FromPath(zipPath string) (*ZipFileSystem, error) {
	return FromFile(fs.File(zipPath))
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


func (ZipFileSystem) File(uriParts ...string) fs.File {
	return ""
}
