package fs

import (
	"io/fs"
	"os"
	"time"
)

// FileInfo is a snapshot of a file's stat information.
// In comparison to os.FileInfo it's not an interface
// but a struct with public fields.
type FileInfo struct {
	Name        string
	Exists      bool
	IsDir       bool
	IsRegular   bool
	IsHidden    bool
	Size        int64
	ModTime     time.Time
	Permissions Permissions

	// ContentHash is otional.
	// For performance reasons, it will only be filled
	// if the FileSystem implementation already has it cached.
	ContentHash string
}

// NewFileInfo returns a FileInfo using the
// data from an os.FileInfo as snapshot
// of an existing file.
// Use NewNonExistingFileInfo to get
// a FileInfo for non existing file.
func NewFileInfo(i os.FileInfo, hidden bool) FileInfo {
	name := i.Name()
	mode := i.Mode()
	return FileInfo{
		Name:        name,
		Exists:      true,
		IsDir:       mode.IsDir(),
		IsRegular:   mode.IsRegular(),
		IsHidden:    hidden,
		Size:        i.Size(),
		ModTime:     i.ModTime(),
		Permissions: Permissions(mode.Perm()),
	}
}

// NewNonExistingFileInfo returns a FileInfo
// for a non existing file with the given name.
// IsHidden will be true if the name starts with a dot.
func NewNonExistingFileInfo(name string) FileInfo {
	return FileInfo{
		Name:     name,
		Exists:   false,
		IsHidden: len(name) > 0 && name[0] == '.',
	}
}

// OSFileInfo returns an os.FileInfo wrapper
// for the data stored in the FileInfo struct.
func (i *FileInfo) OSFileInfo() os.FileInfo { return fileInfo{i} }

// FSFileInfo returns an io/fs.FileInfo wrapper
// for the data stored in the FileInfo struct.
func (i *FileInfo) FSFileInfo() fs.FileInfo { return fileInfo{i} }

// fileInfo implements os.FileInfo and fs.FileInfo for a given FileInfo
type fileInfo struct{ i *FileInfo }

func (f fileInfo) Name() string       { return f.i.Name }
func (f fileInfo) Size() int64        { return f.i.Size }
func (f fileInfo) Mode() os.FileMode  { return f.i.Permissions.FileMode(f.i.IsDir) }
func (f fileInfo) ModTime() time.Time { return f.i.ModTime }
func (f fileInfo) IsDir() bool        { return f.i.IsDir }
func (f fileInfo) Sys() interface{}   { return nil }

// type NameSizeProvider interface {
// 	Name() string
// 	Size() int64
// }

// // FSFileInfoFromNameSizeProvider wraps a NameSizeProvider as a non-directory fs.FileInfo
// // that returns 0666 as mode and the current time as modified time.
// func FSFileInfoFromNameSizeProvider(ns NameSizeProvider) fs.FileInfo {
// 	return nameSizeInfo{ns}
// }

// type nameSizeInfo struct {
// 	NameSizeProvider
// }

// func (nameSizeInfo) Mode() os.FileMode  { return 0666 }
// func (nameSizeInfo) ModTime() time.Time { return time.Now() }
// func (nameSizeInfo) IsDir() bool        { return false }
// func (nameSizeInfo) Sys() interface{}   { return nil }
