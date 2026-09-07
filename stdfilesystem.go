package fs

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"path"
	"strings"
	"sync/atomic"

	"github.com/ungerik/go-fs/fsimpl"
)

// StdFileSystemPrefix is the URI prefix of StdFileSystem, followed by the id.
const StdFileSystemPrefix = "stdfs://"

var (
	_ FileSystem                 = new(StdFileSystem)
	_ ReadAllFileSystem          = new(StdFileSystem)
	_ ListDirRecursiveFileSystem = new(StdFileSystem)
)

// StdFileSystem adapts an io/fs.FS of the standard library
// as a read-only FileSystem, so that an embed.FS, an os.DirFS,
// a zip.Reader or a testing/fstest.MapFS can be used with the File API.
// It is the counterpart of StdFS, which exposes a File as io/fs.FS.
//
// The rooted file system paths map to the slash separated,
// unrooted io/fs names: "/a/b" is the name "a/b", "/" is ".".
//
// StdFileSystem only implements the primitives; every other operation
// uses the generic emulations of the fs package. The io/fs.ReadFileFS
// and io/fs.ReadDirFS methods are used when the wrapped file system
// implements them.
type StdFileSystem struct {
	fsimpl.PathHelper

	fsys   iofs.FS
	id     string
	name   string
	closed atomic.Bool
}

// NewStdFileSystem returns a read-only FileSystem for fsys with the
// URI prefix "stdfs://" + id. A random id is used if id is empty.
// The file system is not registered, use Register to make
// File values with its prefix work.
func NewStdFileSystem(fsys iofs.FS, id string) *StdFileSystem {
	if id == "" {
		id = fsimpl.RandomString()
	}
	return NewStdFileSystemWithPrefix(fsys, StdFileSystemPrefix+id, "io/fs file system")
}

// NewStdFileSystemWithPrefix returns a read-only FileSystem for fsys
// with a custom URI prefix and name, for file systems that are built
// on an io/fs.FS but have a scheme of their own like zipfs.
// The id is the prefix without the scheme.
func NewStdFileSystemWithPrefix(fsys iofs.FS, prefix, name string) *StdFileSystem {
	_, id, _ := strings.Cut(prefix, "://")
	return &StdFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: prefix, Rooted: true},
		fsys:       fsys,
		id:         id,
		name:       name,
	}
}

// NewStdFileSystemAndRegister returns a registered read-only
// FileSystem for fsys, see NewStdFileSystem.
func NewStdFileSystemAndRegister(fsys iofs.FS, id string) *StdFileSystem {
	stdFS := NewStdFileSystem(fsys, id)
	Register(stdFS)
	return stdFS
}

// name returns the io/fs name of a file system path.
func stdName(filePath string) string {
	name := strings.Trim(path.Clean(filePath), "/")
	if name == "" || name == "." {
		return "."
	}
	return name
}

func (s *StdFileSystem) checkClosed() error {
	if s.closed.Load() {
		return ErrFileSystemClosed
	}
	return nil
}

// fileInfo returns the FileInfo of an io/fs.FileInfo. Files without
// permission bits (like in a testing/fstest.MapFS) are reported as
// readable by everyone, since the file system is readable.
func (s *StdFileSystem) fileInfo(file File, info iofs.FileInfo, hidden bool) *FileInfo {
	fileInfo := NewFileInfo(file, info, hidden)
	if fileInfo.Permissions == 0 {
		fileInfo.Permissions = AllRead
	}
	return fileInfo
}

// wrapErr maps io/fs errors for filePath to the fs error types.
func (s *StdFileSystem) wrapErr(filePath string, err error) error {
	if errors.Is(err, iofs.ErrNotExist) {
		return NewErrDoesNotExist(s.file(filePath))
	}
	return err
}

func (s *StdFileSystem) file(filePath string) File {
	return File(s.JoinCleanURI(filePath))
}

// ReadableWritable returns true for readable and false for writable,
// because an io/fs.FS is read-only.
func (s *StdFileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

// RootDir returns the root directory of the file system.
func (s *StdFileSystem) RootDir() File {
	return File(s.URIPrefix + "/")
}

// ID returns the id of the file system, which is part of its URI prefix.
func (s *StdFileSystem) ID() string {
	return s.id
}

// Name returns the name passed to NewStdFileSystemWithPrefix,
// or "io/fs file system".
func (s *StdFileSystem) Name() string {
	return s.name
}

// String returns a descriptive string of the file system
// including its name and prefix.
func (s *StdFileSystem) String() string {
	return s.Name() + " with prefix " + s.URIPrefix
}

// Stat returns the FileInfo of the file. Names beginning with a dot
// are reported as hidden.
func (s *StdFileSystem) Stat(filePath string) (*FileInfo, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	info, err := iofs.Stat(s.fsys, stdName(filePath))
	if err != nil {
		return nil, s.wrapErr(filePath, err)
	}
	fileInfo := s.fileInfo(s.file(filePath), info, s.IsHidden(filePath))
	if stdName(filePath) == "." {
		fileInfo.Name = "/"
	}
	return fileInfo, nil
}

// ListDir calls the callback for every file in the directory that
// matches any of the patterns, or for all files if no patterns are passed.
// The io/fs.ReadDirFS method of the wrapped file system is used if
// it implements it.
func (s *StdFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := iofs.ReadDir(s.fsys, stdName(dirPath))
	if err != nil {
		if info, statErr := s.Stat(dirPath); statErr == nil && !info.IsDir {
			return NewErrIsNotDirectory(info.File)
		}
		return s.wrapErr(dirPath, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		err = callback(s.fileInfo(s.file(s.CleanPath(dirPath, entry.Name())), info, s.IsHidden(entry.Name())))
		if err != nil {
			return err
		}
	}
	return nil
}

// ListDirRecursive walks the tree with io/fs.WalkDir and calls
// callback for every file (not directory) matching the patterns.
func (s *StdFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	root := stdName(dirPath)
	return iofs.WalkDir(s.fsys, root, func(name string, entry iofs.DirEntry, err error) error {
		if err != nil {
			if name == root {
				return s.wrapErr(dirPath, err)
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return callback(s.fileInfo(s.file("/"+name), info, s.IsHidden(entry.Name())))
	})
}

// OpenReader opens the file with the io/fs.FS.
// The returned reader implements io/fs.File.
func (s *StdFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	file, err := s.fsys.Open(stdName(filePath))
	if err != nil {
		return nil, s.wrapErr(filePath, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if info.IsDir() {
		return nil, errors.Join(NewErrIsDirectory(s.file(filePath)), file.Close())
	}
	// Stat of the opened file must report the same info as Stat
	// of the file system, including the readable permissions
	return &stdFile{File: file, info: s.fileInfo(s.file(filePath), info, s.IsHidden(filePath)).StdFileInfo()}, nil
}

// stdFile is an opened io/fs.File whose Stat reports
// the FileInfo of the StdFileSystem.
type stdFile struct {
	iofs.File
	info iofs.FileInfo
}

func (f *stdFile) Stat() (iofs.FileInfo, error) {
	return f.info, nil
}

// ReadAll uses io/fs.ReadFile, which uses the ReadFile method
// of the wrapped file system if it implements io/fs.ReadFileFS.
func (s *StdFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := iofs.ReadFile(s.fsys, stdName(filePath))
	if err != nil {
		if info, statErr := s.Stat(filePath); statErr == nil && info.IsDir {
			return nil, NewErrIsDirectory(info.File)
		}
		return nil, s.wrapErr(filePath, err)
	}
	return data, nil
}

// Close unregisters the file system if it was registered.
// Every method returns ErrFileSystemClosed afterwards.
func (s *StdFileSystem) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	Unregister(s)
	return nil
}
