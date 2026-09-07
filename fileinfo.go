package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"strings"
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
	IsSymlink   bool // The path itself is a symbolic link, the other fields describe the link target
	IsHidden    bool
	Size        int64
	Modified    time.Time
	Permissions Permissions
	Sys         any // Underlying data source (can return nil)
}

// Validate returns an error if the FileInfo is invalid.
func (i *FileInfo) Validate() error {
	if i == nil {
		return errors.New("<nil> FileInfo")
	}
	if i.File == "" || i.Name == "" {
		return ErrEmptyPath
	}
	return nil
}

// NewFileInfo returns a FileInfo using the
// data from an io/fs.FileInfo as snapshot
// of an existing file.
// Use NewNonExistingFileInfo to get
// a FileInfo for non existing file.
func NewFileInfo(file File, info iofs.FileInfo, hidden bool) *FileInfo {
	mode := info.Mode()
	return &FileInfo{
		File:        file,
		Name:        info.Name(),
		Exists:      true,
		IsDir:       mode.IsDir(),
		IsRegular:   mode.IsRegular(),
		IsSymlink:   mode&iofs.ModeSymlink != 0,
		IsHidden:    hidden,
		Size:        info.Size(),
		Modified:    info.ModTime(),
		Permissions: Permissions(mode.Perm()),
		Sys:         info.Sys(),
	}
}

// NewNonExistingFileInfo returns a FileInfo
// for a potentially non existing file.
// FileInfo.Exists will be false, but the
// file may exist at any point of time.
// IsHidden will be true if the name starts with a dot.
func NewNonExistingFileInfo(file File) *FileInfo {
	name := file.Name()
	return &FileInfo{
		File:     file,
		Name:     name,
		Exists:   false,
		IsHidden: strings.HasPrefix(name, "."),
	}
}

// StdFileInfo returns an io/fs.FileInfo wrapper
// for the data stored in the FileInfo struct.
func (i *FileInfo) StdFileInfo() iofs.FileInfo { return fileInfo{i} }

// fileInfo implements os.FileInfo and fs.FileInfo for a given FileInfo
type fileInfo struct{ i *FileInfo }

func (f fileInfo) Name() string       { return f.i.Name }
func (f fileInfo) Size() int64        { return f.i.Size }
func (f fileInfo) ModTime() time.Time { return f.i.Modified }
func (f fileInfo) IsDir() bool        { return f.i.IsDir }
func (f fileInfo) Sys() any           { return f.i.Sys }

func (f fileInfo) Mode() os.FileMode {
	m := f.i.Permissions.FileMode(f.i.IsDir)
	if f.i.IsSymlink {
		m |= os.ModeSymlink
	}
	return m
}
