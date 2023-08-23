package fs

import (
	"io/fs"
	"os"
	"time"
)

// FileInfo is a snapshot of a file's stat information.
// In comparison to io/fs.FileInfo it's not an interface
// but a struct with public fields.
type FileInfo struct {
	File        File
	Name        string
	Exists      bool
	IsDir       bool
	IsRegular   bool
	IsHidden    bool
	Size        int64
	Modified    time.Time
	Permissions Permissions
}

// NewFileInfo returns a FileInfo using the
// data from an os.FileInfo as snapshot
// of an existing file.
// Use NewNonExistingFileInfo to get
// a FileInfo for non existing file.
func NewFileInfo(file File, info fs.FileInfo, hidden bool) FileInfo {
	mode := info.Mode()
	return FileInfo{
		File:        file,
		Name:        info.Name(),
		Exists:      true,
		IsDir:       mode.IsDir(),
		IsRegular:   mode.IsRegular(),
		IsHidden:    hidden,
		Size:        info.Size(),
		Modified:    info.ModTime(),
		Permissions: Permissions(mode.Perm()),
	}
}

// NewNonExistingFileInfo returns a FileInfo
// for a potentially non existing file.
// FileInfo.Exists will be false, but the
// file may exist at any point of time.
// IsHidden will be true if the name starts with a dot.
func NewNonExistingFileInfo(file File) FileInfo {
	name := file.Name()
	return FileInfo{
		File:     file,
		Name:     name,
		Exists:   false,
		IsHidden: len(name) > 0 && name[0] == '.',
	}
}

// StdFileInfo returns an io/fs.FileInfo wrapper
// for the data stored in the FileInfo struct.
func (i *FileInfo) StdFileInfo() fs.FileInfo { return fileInfo{i} }

// fileInfo implements os.FileInfo and fs.FileInfo for a given FileInfo
type fileInfo struct{ i *FileInfo }

func (f fileInfo) Name() string       { return f.i.Name }
func (f fileInfo) Size() int64        { return f.i.Size }
func (f fileInfo) Mode() os.FileMode  { return f.i.Permissions.FileMode(f.i.IsDir) }
func (f fileInfo) ModTime() time.Time { return f.i.Modified }
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
