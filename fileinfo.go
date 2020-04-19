package fs

import (
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

// FileInfo returns an os.FileInfo
func (i FileInfo) FileInfo() os.FileInfo { return osFileInfo{i} }

// osFileInfo implements os.FileInfo for a given FileInfo
type osFileInfo struct{ i FileInfo }

func (f osFileInfo) Name() string       { return f.i.Name }
func (f osFileInfo) Size() int64        { return f.i.Size }
func (f osFileInfo) Mode() os.FileMode  { return f.i.Permissions.FileMode(f.i.IsDir) }
func (f osFileInfo) ModTime() time.Time { return f.i.ModTime }
func (f osFileInfo) IsDir() bool        { return f.i.IsDir }
func (f osFileInfo) Sys() interface{}   { return nil }
