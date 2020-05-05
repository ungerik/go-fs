package fs

import (
	"fmt"
	"os"
	"time"
)

type localFileInfo struct {
	os.FileInfo

	path              string
	cachedContentHash string // For performance reasons, it will only be returned if the FileSystem implementation already has it cached
}

func newLocalFileInfo(filePath string, fileInfo os.FileInfo) *localFileInfo {
	return &localFileInfo{
		FileInfo:          fileInfo,
		path:              expandTilde(filePath),
		cachedContentHash: "",
	}
}

func (i *localFileInfo) File() File                { return File(i.path) }
func (i *localFileInfo) Exists() bool              { return true }
func (i *localFileInfo) IsRegular() bool           { return i.FileInfo.Mode().IsRegular() }
func (i *localFileInfo) Permissions() Permissions  { return Permissions(i.FileInfo.Mode().Perm()) }
func (i *localFileInfo) CachedContentHash() string { return i.cachedContentHash }

func (i *localFileInfo) IsHidden() bool {
	hidden, err := hasFileAttributeHidden(i.path)
	if err != nil {
		// Should not happen, this is why we are logging the error
		fmt.Fprintf(os.Stderr, "hasFileAttributeHidden(%s): %+v\n", i.path, err)
		return false
	}
	name := i.Name()
	return hidden || len(name) > 0 && name[0] == '.'
}

func NewNonExistingFileInfo(file File) FileInfo {
	return nonExistingFileInfo{file}
}

type nonExistingFileInfo struct {
	file File
}

func (i nonExistingFileInfo) Name() string            { return i.file.Name() }
func (nonExistingFileInfo) Size() int64               { return 0 }
func (nonExistingFileInfo) Mode() os.FileMode         { return 0 }
func (nonExistingFileInfo) ModTime() time.Time        { return time.Time{} }
func (nonExistingFileInfo) IsDir() bool               { return false }
func (nonExistingFileInfo) Sys() interface{}          { return nil }
func (i nonExistingFileInfo) File() File              { return i.file }
func (nonExistingFileInfo) Exists() bool              { return false }
func (nonExistingFileInfo) IsRegular() bool           { return false }
func (nonExistingFileInfo) Permissions() Permissions  { return NoPermissions }
func (nonExistingFileInfo) CachedContentHash() string { return "" }
func (nonExistingFileInfo) IsHidden() bool            { return false }
