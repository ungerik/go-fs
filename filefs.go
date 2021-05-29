package fs

import "io/fs"

var (
	_ fs.FS         = FileFS{File("")}
	_ fs.SubFS      = FileFS{File("")}
	_ fs.StatFS     = FileFS{File("")}
	_ fs.GlobFS     = FileFS{File("")}
	_ fs.ReadDirFS  = FileFS{File("")}
	_ fs.ReadFileFS = FileFS{File("")}
)

// FileFS implements the FS interfaces of the os/fs package for a File.
//
// FileFS implements the following interfaces:
//   os/fs.FS
//   os/fs.SubFS
//   os/fs.StatFS
//   os/fs.GlobFS
//   os/fs.ReadDirFS
//   os/fs.ReadFileFS
type FileFS struct {
	File File
}

// Stat returns a FileInfo describing the file.
// This method implements the io/fs.StatFS interface.
func (f FileFS) Stat(name string) (fs.FileInfo, error) {
	return f.File.Join(name).Stat()
}

// Sub returns an FS corresponding to the subtree rooted at dir.
// This method implements the io/fs.SubFS interface.
func (f FileFS) Sub(dir string) (fs.FS, error) {
	return f.File.Join(dir).AsFS(), nil
}

// Open opens the named file.
// This method implements the io/fs.FS interface.
func (f FileFS) Open(name string) (fs.File, error) {
	panic("TODO")
}

// ReadFile reads the named file and returns its contents.
// This method implements the io/fs.ReadFileFS interface.
func (f FileFS) ReadFile(name string) ([]byte, error) {
	return f.File.Join(name).ReadAll()
}

// ReadDir reads the named directory
// and returns a list of directory entries sorted by filename.
// This method implements the io/fs.ReadDirFS interface.
func (f FileFS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	err = f.File.Join(name).ListDir(func(file File) error {
		entries = append(entries, file.AsDirEntry())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Glob returns the names of all files matching pattern,
// providing an implementation of the top-level
// Glob function.
// This method implements the io/fs.GlobFS interface.
func (f FileFS) Glob(pattern string) (names []string, err error) {
	err = f.File.ListDir(
		func(file File) error {
			names = append(names, file.Name())
			return nil
		},
		pattern,
	)
	if err != nil {
		return nil, err
	}
	return names, nil
}

type FileDirEntry struct {
	File File
}

// Name returns the name of the file (or subdirectory) described by the entry.
// This name is only the final element of the path (the base name), not the entire path.
// For example, Name would return "hello.go" not "/home/gopher/hello.go".
func (e FileDirEntry) Name() string {
	return e.File.Name()
}

// IsDir reports whether the entry describes a directory.
func (e FileDirEntry) IsDir() bool {
	return e.File.IsDir()
}

// Type returns the type bits for the entry.
// The type bits are a subset of the usual FileMode bits, those returned by the FileMode.Type method.
func (e FileDirEntry) Type() fs.FileMode {
	stat, err := e.File.Stat()
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
func (e FileDirEntry) Info() (fs.FileInfo, error) {
	return e.File.Stat()
}
