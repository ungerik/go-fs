package fs

import (
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/satori/go.uuid"
)

// SubFileSystemPrefix is the URI prefix used to identify SubFileSystem files
const SubFileSystemPrefix = "sub://"

var subFileSystems map[string]*SubFileSystem

type SubFileSystem struct {
	prefix   string
	Parent   FileSystem
	BasePath string
}

func NewSubFileSystem(parent FileSystem, basePath string) *SubFileSystem {
	fs := &SubFileSystem{
		prefix:   SubFileSystemPrefix + uuid.NewV4().String(),
		Parent:   parent,
		BasePath: basePath,
	}
	subFileSystems[fs.prefix] = fs
	Registry = append(Registry, fs)
	return fs
}

func (fs *SubFileSystem) Destroy() {
	delete(subFileSystems, fs.prefix)
	DeregisterFileSystem(fs)
}

func (fs *SubFileSystem) IsReadOnly() bool {
	return fs.Parent.IsReadOnly()
}

func (fs *SubFileSystem) Prefix() string {
	return fs.prefix
}

func (fs *SubFileSystem) Name() string {
	return "Sub file system of " + fs.Parent.Name()
}

///////////////////////////////////////////////////
// TODO Replace implementation with real SubFileSystem from here on:
///////////////////////////////////////////////////

func (fs *SubFileSystem) File(uri ...string) File {
	if len(uri) == 0 {
		panic("SubFileSystem uri must not be empty")
	}

	return File(filepath.Clean(filepath.Join(uri...)))
}

func (fs *SubFileSystem) URN(filePath string) string {
	return filepath.ToSlash(filePath)
}

func (fs *SubFileSystem) URL(filePath string) string {
	return LocalPrefix + fs.URN(filePath)
}

func (fs *SubFileSystem) CleanPath(uri ...string) string {
	return fs.prefix + fs.Parent.CleanPath(uri...)
}

func (fs *SubFileSystem) SplitPath(filePath string) []string {
	return fs.Parent.SplitPath(filePath)
}

func (fs *SubFileSystem) Seperator() string {
	return fs.Parent.Seperator()
}

func (fs *SubFileSystem) FileName(filePath string) string {
	return filepath.Base(filePath)
}

func (fs *SubFileSystem) Ext(filePath string) string {
	return strings.ToLower(filepath.Ext(filePath))
}

func (fs *SubFileSystem) Dir(filePath string) string {
	return filepath.Dir(filePath)
}

func (fs *SubFileSystem) Exists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

func (fs *SubFileSystem) IsDir(filePath string) bool {
	info, err := os.Stat(filePath)
	return err == nil && info.IsDir()
}

func (fs *SubFileSystem) Size(filePath string) int64 {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (fs *SubFileSystem) ModTime(filePath string) time.Time {
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (fs *SubFileSystem) ListDir(filePath string, callback func(File) error, patterns ...string) error {
	if !fs.IsDir(filePath) {
		return ErrIsNotDirectory{File(filePath)}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	for eof := false; !eof; {
		names, err := f.Readdirnames(64)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return err
			}
		}

		for _, name := range names {
			match, err := matchAnyPattern(name, patterns)
			if match {
				err = callback(fs.File(filePath, name))
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (fs *SubFileSystem) ListDirMax(filePath string, n int, patterns ...string) (files []File, err error) {
	if !fs.IsDir(filePath) {
		return nil, ErrIsNotDirectory{File(filePath)}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var numFilesToDo int
	if n > 0 {
		files = make([]File, 0, n)
		numFilesToDo = n
	} else {
		numFilesToDo = 64
	}

	for eof := false; !eof && numFilesToDo > 0; {
		names, err := f.Readdirnames(numFilesToDo)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return nil, err
			}
		}

		for _, name := range names {
			match, err := matchAnyPattern(name, patterns)
			if match {
				files = append(files, fs.File(filePath, name))
			}
			if err != nil {
				return nil, err
			}
		}

		if n > 0 {
			numFilesToDo = n - len(files)
		}
	}

	return files, nil
}

func (fs *SubFileSystem) Permissions(filePath string) Permissions {
	info, err := os.Stat(filePath)
	if err != nil {
		return NoPermissions
	}
	return Permissions(info.Mode().Perm())
}

func (fs *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return os.Chmod(filePath, os.FileMode(perm))
}

func (fs *SubFileSystem) User(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (fs *SubFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (fs *SubFileSystem) Touch(filePath string, perm ...Permissions) error {
	if fs.Exists(filePath) {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	} else {
		return fs.WriteAll(filePath, nil, perm...)
	}
}

func (fs *SubFileSystem) MakeDir(filePath string, perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreateDirPermissions)
	return os.MkdirAll(filePath, os.FileMode(p))
}

func (fs *SubFileSystem) ReadAll(filePath string) ([]byte, error) {
	return ioutil.ReadFile(filePath)
}

func (fs *SubFileSystem) WriteAll(filePath string, data []byte, perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return ioutil.WriteFile(filePath, data, os.FileMode(p))
}

func (fs *SubFileSystem) Append(filePath string, data []byte, perm ...Permissions) error {
	writer, err := fs.OpenAppendWriter(filePath, perm...)
	if err != nil {
		return err
	}
	defer writer.Close()
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (fs *SubFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	return os.OpenFile(filePath, os.O_RDONLY, 0400)
}

func (fs *SubFileSystem) OpenWriter(filePath string, perm ...Permissions) (WriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
}

func (fs *SubFileSystem) OpenAppendWriter(filePath string, perm ...Permissions) (io.WriteCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(p))
}

func (fs *SubFileSystem) OpenReadWriter(filePath string, perm ...Permissions) (ReadWriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, os.FileMode(p))
}

func (fs *SubFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, errors.New("not implemented")
	// events := make(chan WatchEvent, 1)
	// return events
}

func (fs *SubFileSystem) Truncate(filePath string, size int64) error {
	return os.Truncate(filePath, size)
}

func (fs *SubFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separatos: " + newName)
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	err := os.Rename(filePath, newPath)
	if err != nil {
		return err
	}
	filePath = newPath
	return nil
}

func (fs *SubFileSystem) Move(filePath string, destPath string) error {
	if fs.IsDir(destPath) {
		destPath = filepath.Join(destPath, fs.FileName(filePath))
	}
	err := os.Rename(filePath, destPath)
	if err != nil {
		return err
	}
	filePath = destPath
	return nil
}

func (fs *SubFileSystem) Remove(filePath string) error {
	return os.Remove(filePath)
}
