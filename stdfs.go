package fs

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"runtime"
	"sort"
	"strings"
)

var (
	_ iofs.FS         = StdFS{File("")}
	_ iofs.SubFS      = StdFS{File("")}
	_ iofs.StatFS     = StdFS{File("")}
	_ iofs.ReadDirFS  = StdFS{File("")}
	_ iofs.ReadFileFS = StdFS{File("")}
)

// StdFS implements the io/fs.FS interface
// of the standard library for a File.
//
// StdFS implements the following interfaces:
//   - io/fs.FS
//   - io/fs.SubFS
//   - io/fs.StatFS
//   - io/fs.ReadDirFS
//   - io/fs.ReadFileFS
type StdFS struct {
	File File
}

// Stat returns a io/fs.FileInfo describing the file.
//
// This method implements the io/fs.StatFS interface.
func (f StdFS) Stat(name string) (iofs.FileInfo, error) {
	return f.File.Join(name).Stat()
}

// Sub returns an io/fs.FS corresponding to the subtree rooted at dir.
//
// This method implements the io/fs.SubFS interface.
func (f StdFS) Sub(dir string) (iofs.FS, error) {
	return f.File.Join(dir).StdFS(), nil
}

// Open opens the named file.
//
// This method implements the io/fs.FS interface.
func (f StdFS) Open(name string) (iofs.File, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	file := f.File.Join(name)
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		// Directories are opened as io/fs.ReadDirFile,
		// which not every file system's OpenReader supports
		return &stdDirFile{file: file, info: info}, nil
	}
	return file.OpenReader()
}

// stdDirFile is the io/fs.ReadDirFile of a directory opened by StdFS.
type stdDirFile struct {
	file    File
	info    iofs.FileInfo
	entries []iofs.DirEntry // loaded by the first ReadDir
	loaded  bool
	offset  int
}

func (d *stdDirFile) Stat() (iofs.FileInfo, error) {
	return d.info, nil
}

func (d *stdDirFile) Read([]byte) (int, error) {
	return 0, &iofs.PathError{Op: "read", Path: d.file.Path(), Err: NewErrIsDirectory(d.file)}
}

func (d *stdDirFile) Close() error {
	return nil
}

// ReadDir reads the directory entries sorted by name like io/fs.ReadDir,
// n entries at a time for n > 0 and all remaining entries otherwise.
func (d *stdDirFile) ReadDir(n int) ([]iofs.DirEntry, error) {
	if !d.loaded {
		entries, err := stdReadDir(d.file)
		if err != nil {
			return nil, err
		}
		d.entries = entries
		d.loaded = true
	}
	remaining := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return remaining, nil
	}
	if len(remaining) == 0 {
		return nil, io.EOF
	}
	if len(remaining) > n {
		remaining = remaining[:n]
	}
	d.offset += len(remaining)
	return remaining, nil
}

// ReadFile reads the named file and returns its contents.
//
// This method implements the io/fs.ReadFileFS interface.
func (f StdFS) ReadFile(name string) ([]byte, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	return f.File.Join(name).ReadAll(context.Background())
}

// ReadDir reads the named directory
// and returns a list of directory entries sorted by filename.
//
// This method implements the io/fs.ReadDirFS interface.
func (f StdFS) ReadDir(name string) ([]iofs.DirEntry, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	return stdReadDir(f.File.Join(name))
}

// stdReadDir lists the entries of dir sorted by name like io/fs.ReadDir.
func stdReadDir(dir File) ([]iofs.DirEntry, error) {
	var entries []iofs.DirEntry
	err := dir.ListDir(context.Background(), func(file File) error {
		entries = append(entries, file.StdDirEntry())
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// checkStdFSName validates name like the io/fs package does,
// so files like "dir/.gitignore" are accepted.
// Like os.DirFS it rejects backslashes and colons on Windows,
// because io/fs names are always slash separated and must not
// be reinterpreted by the underlying file system.
func checkStdFSName(name string) error {
	if name == "" {
		return errors.New("empty filename")
	}
	if !iofs.ValidPath(name) || runtime.GOOS == "windows" && strings.ContainsAny(name, `\:`) {
		return &iofs.PathError{Op: "open", Path: name, Err: iofs.ErrInvalid}
	}
	return nil
}
