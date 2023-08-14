package fs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

var (
	_ fs.FS         = StdFS{File("")}
	_ fs.SubFS      = StdFS{File("")}
	_ fs.StatFS     = StdFS{File("")}
	_ fs.GlobFS     = StdFS{File("")}
	_ fs.ReadDirFS  = StdFS{File("")}
	_ fs.ReadFileFS = StdFS{File("")}
)

// StdFS implements the io/fs.FS interface
// of the standard library for a File.
//
// StdFS implements the following interfaces:
//   - io/fs.FS
//   - io/fs.SubFS
//   - io/fs.StatFS
//   - io/fs.GlobFS
//   - io/fs.ReadDirFS
//   - io/fs.ReadFileFS
type StdFS struct {
	File File
}

// Stat returns a io/fs.FileInfo describing the file.
//
// This method implements the io/fs.StatFS interface.
func (f StdFS) Stat(name string) (fs.FileInfo, error) {
	return f.File.Join(name).Stat()
}

// Sub returns an io/fs.FS corresponding to the subtree rooted at dir.
//
// This method implements the io/fs.SubFS interface.
func (f StdFS) Sub(dir string) (fs.FS, error) {
	return f.File.Join(dir).StdFS(), nil
}

// Open opens the named file.
//
// This method implements the io/fs.FS interface.
func (f StdFS) Open(name string) (fs.File, error) {
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
func (f StdFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := checkStdFSName(name); err != nil {
		return nil, err
	}
	var entries []fs.DirEntry
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

// Glob returns the names of all files matching pattern,
// providing an implementation of the top-level
// Glob function.
//
// This method implements the io/fs.GlobFS interface.
func (f StdFS) Glob(pattern string) (names []string, err error) {
	// if pattern == `u[u][i-i][\d][\d-\d]i[r]/*e*` {
	// 	fmt.Println(pattern)
	// }
	if strings.Contains(pattern, "//") || strings.Contains(pattern, "[]") {
		return nil, fmt.Errorf("invalid glob pattern: %#v", pattern)
	}
	parentPattern, childPattern, cut := strings.Cut(pattern, "/")
	err = f.File.ListDir(
		func(file File) error {
			names = append(names, file.Name())
			return nil
		},
		parentPattern,
	)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if cut {
		parentNames := names
		names = nil // Don't include parents in final result
		for _, parent := range parentNames {
			children, err := f.File.Join(parent).StdFS().Glob(childPattern)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				names = append(names, path.Join(parent, child))
			}
		}
	}
	return names, nil
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
