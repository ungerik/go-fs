package s3fs

import (
	"os"
	"time"
)

var _ os.FileInfo = new(fileInfo)

type fileInfo struct {
	name string
	size int64
	time time.Time
}

func (i *fileInfo) Name() string       { return i.name } // base name of the file
func (i *fileInfo) Size() int64        { return i.size } // length in bytes for regular files; system-dependent for others
func (i *fileInfo) Mode() os.FileMode  { return 0600 }   // file mode bits
func (i *fileInfo) ModTime() time.Time { return i.time } // modification time
func (i *fileInfo) IsDir() bool        { return false }  // abbreviation for Mode().IsDir()
func (i *fileInfo) Sys() any           { return nil }    // underlying data source (can return nil)
