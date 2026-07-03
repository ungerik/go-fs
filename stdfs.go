package fs

import (
	"errors"
	"fmt"
	iofs "io/fs"
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
	return f.File.Join(name).OpenReader()
}

// ReadFile reads the named file and returns its contents.
//
// This method implements the io/fs.ReadFileFS interface.
func (f StdFS) ReadFile(name string) ([]byte, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	return f.File.Join(name).ReadAll()
}

// ReadDir reads the named directory
// and returns a list of directory entries sorted by filename.
//
// This method implements the io/fs.ReadDirFS interface.
func (f StdFS) ReadDir(name string) ([]iofs.DirEntry, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	var entries []iofs.DirEntry
	err := f.File.Join(name).ListDir(func(file File) error {
		entries = append(entries, file.StdDirEntry())
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func checkStdFSName(name string) error {
	if name == "" {
		return errors.New("empty filename")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "/.") || strings.Contains(name, "//") {
		return fmt.Errorf("invalid filename: %s", name)
	}
	return nil
}
