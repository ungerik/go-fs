package fs

import (
	"errors"
	iofs "io/fs"
	"strings"
	"time"
)

// FileInfo is a snapshot of a file's stat information.
// In comparison to io/fs.FileInfo it's not an interface
// but a struct with public fields.
//
// IsSymlink reports if the path itself is a symbolic link.
// The FileInfo of Stat follows symbolic links,
// so for a link all other fields describe the target.
// The FileInfo values of ListDir and Lstat describe the directory entry:
// for a link IsDir and IsRegular describe the target as far as the
// file system knows it cheaply (both false if unknown),
// and the other fields may describe the link itself.
// ListDirRecursive describes its entries like ListDir, but it never
// follows a link and IsDir is false also for a link to a directory.
type FileInfo struct {
	File        File
	Name        string
	Exists      bool
	IsDir       bool
	IsRegular   bool
	IsSymlink   bool // The path itself is a symbolic link, see the FileInfo documentation
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
// The Permissions are the result of PermissionsFromFileMode,
// so they include the setuid, setgid and sticky bits of the mode.
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
		Permissions: PermissionsFromFileMode(mode),
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
//
// Like the result of os.Stat it describes the file
// a symbolic link points to: the mode never has io/fs.ModeSymlink set,
// it has io/fs.ModeDir for a directory and io/fs.ModeIrregular
// for a file that is neither a directory nor a regular file.
// So Mode().IsDir() equals IsDir and Mode().IsRegular() equals IsRegular.
//
// A [StdDirEntry] describes a symbolic link itself.
func (i *FileInfo) StdFileInfo() iofs.FileInfo { return fileInfo{i} }

// fileInfo implements io/fs.FileInfo for a given FileInfo
type fileInfo struct{ i *FileInfo }

func (f fileInfo) Name() string       { return f.i.Name }
func (f fileInfo) Size() int64        { return f.i.Size }
func (f fileInfo) ModTime() time.Time { return f.i.Modified }
func (f fileInfo) IsDir() bool        { return f.i.IsDir }
func (f fileInfo) Sys() any           { return f.i.Sys }

func (f fileInfo) Mode() iofs.FileMode {
	m := f.i.Permissions.FileMode(f.i.IsDir)
	if !f.i.IsDir && !f.i.IsRegular {
		m |= iofs.ModeIrregular
	}
	return m
}

// linkFileInfo implements io/fs.FileInfo like the result of os.Lstat
// for the FileInfo of a symbolic link: the mode has io/fs.ModeSymlink
// set and no other type bits, and IsDir is false.
type linkFileInfo struct{ fileInfo }

func (linkFileInfo) IsDir() bool { return false }

func (f linkFileInfo) Mode() iofs.FileMode {
	return f.i.Permissions.FileMode(false) | iofs.ModeSymlink
}
