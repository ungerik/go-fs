package fs

import (
	"io/fs"
	"os"
	"time"
)

// FileInfo is returned by FileSystem.Stat()
type FileInfo struct {
	Name        string
	Exists      bool
	IsDir       bool
	IsRegular   bool
	IsHidden    bool
	Size        int64
	ModTime     time.Time
	Permissions Permissions
	ContentHash string //ContentHash is otional. For performance reasons, it will only be filled if the FileSystem implementation already has it cached
}

// OSFileInfo returns an os.FileInfo
func (i FileInfo) OSFileInfo() os.FileInfo { return fileInfo{i} }

// FSFileInfo returns an io/os.FileInfo
func (i FileInfo) FSFileInfo() fs.FileInfo { return fileInfo{i} }

// fileInfo implements os.FileInfo and fs.FileInfo for a given FileInfo
type fileInfo struct{ i FileInfo }

func (f fileInfo) Name() string       { return f.i.Name }
func (f fileInfo) Size() int64        { return f.i.Size }
func (f fileInfo) Mode() os.FileMode  { return f.i.Permissions.FileMode(f.i.IsDir) }
func (f fileInfo) ModTime() time.Time { return f.i.ModTime }
func (f fileInfo) IsDir() bool        { return f.i.IsDir }
func (f fileInfo) Sys() interface{}   { return nil }
