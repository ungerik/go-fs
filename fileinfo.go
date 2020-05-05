package fs

import (
	"os"
)

// FileInfo is returned by FileSystem.Stat()
type FileInfo interface {
	os.FileInfo
	// Name() string       // base name of the file
	// Size() int64        // length in bytes for regular files; system-dependent for others
	// Mode() FileMode     // file mode bits
	// ModTime() time.Time // modification time
	// IsDir() bool        // abbreviation for Mode().IsDir()
	// Sys() interface{}   // underlying data source (can return nil)

	File() File
	Exists() bool
	IsRegular() bool
	IsHidden() bool
	Permissions() Permissions

	CachedContentHash() string // For performance reasons, it will only be returned if the FileSystem implementation already has it cached
}
