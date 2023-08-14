package fs

import "io/fs"

var (
	_ fs.DirEntry = StdDirEntry{File("")}
)

// StdDirEntry implements the io/fs.DirEntry interface
// from the standard library for a File.
type StdDirEntry struct {
	File File
}

// Name returns the name of the file (or subdirectory) described by the entry.
// This name is only the final element of the path (the base name), not the entire path.
// For example, Name would return "hello.go" not "/home/gopher/hello.go".
func (de StdDirEntry) Name() string {
	return de.File.Name()
}

// IsDir reports whether the entry describes a directory.
func (de StdDirEntry) IsDir() bool {
	return de.File.IsDir()
}

// Type returns the type bits for the entry.
// The type bits are a subset of the usual FileMode bits, those returned by the FileMode.Type method.
func (de StdDirEntry) Type() fs.FileMode {
	stat, err := de.File.Stat()
	if err != nil {
		return 0
	}
	return stat.Mode().Type()
}

// Info returns the FileInfo for the file or subdirectory described by the entry.
// The returned FileInfo may be from the time of the original directory read
// or from the time of the call to Info. If the file has been removed or renamed
// since the directory read, Info may return an error satisfying errors.Is(err, ErrNotExist).
// If the entry denotes a symbolic link, Info reports the information about the link itself,
// not the link's target.
func (de StdDirEntry) Info() (fs.FileInfo, error) {
	return de.File.Stat()
}
