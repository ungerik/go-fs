package s3fs

import (
	iofs "io/fs"
	"time"
)

var _ iofs.FileInfo = new(fileInfo)

type fileInfo struct {
	name string
	size int64
	time time.Time
	dir  bool
}

func (i *fileInfo) Name() string { return i.name } // base name of the file
func (i *fileInfo) Size() int64  { return i.size } // length in bytes for regular files; system-dependent for others
func (i *fileInfo) Mode() iofs.FileMode { // file mode bits
	if i.dir {
		return 0755 | iofs.ModeDir
	}
	return 0600
}
func (i *fileInfo) ModTime() time.Time { return i.time } // modification time
func (i *fileInfo) IsDir() bool        { return i.dir }  // abbreviation for Mode().IsDir()
func (i *fileInfo) Sys() any           { return nil }    // underlying data source (can return nil)
