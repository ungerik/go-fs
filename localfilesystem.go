package fs

import (
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const LocalPrefix = "file://"

type LocalFileSystem struct {
	DefaultCreatePermissions    Permissions
	DefaultCreateDirPermissions Permissions
}

func (fs *LocalFileSystem) IsReadOnly() bool {
	return false
}

func (fs *LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (fs *LocalFileSystem) Name() string {
	return "local file system"
}

func (fs *LocalFileSystem) File(uri ...string) File {
	return File(filepath.Clean(filepath.Join(uri...)))
}

func (fs *LocalFileSystem) URN(filePath string) string {
	return filepath.ToSlash(filePath)
}

func (fs *LocalFileSystem) URL(filePath string) string {
	return LocalPrefix + fs.URN(filePath)
}

func (fs *LocalFileSystem) CleanPath(filePath string) string {
	return filepath.Clean(strings.TrimPrefix(filePath, LocalPrefix))
}

func (fs *LocalFileSystem) FileName(filePath string) string {
	return filepath.Base(filePath)
}

func (fs *LocalFileSystem) Ext(filePath string) string {
	return strings.ToLower(filepath.Ext(filePath))
}

func (fs *LocalFileSystem) Dir(filePath string) string {
	return filepath.Dir(filePath)
}

func (fs *LocalFileSystem) Exists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

func (fs *LocalFileSystem) IsDir(filePath string) bool {
	info, err := os.Stat(filePath)
	return err == nil && info.IsDir()
}

func (fs *LocalFileSystem) Size(filePath string) int64 {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (fs *LocalFileSystem) ModTime(filePath string) time.Time {
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func matchAnyPattern(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := filepath.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

func (fs *LocalFileSystem) ListDir(filePath string, callback func(File) error, patterns ...string) error {
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

func (fs *LocalFileSystem) ListDirMax(filePath string, n int, patterns ...string) (files []File, err error) {
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

func (fs *LocalFileSystem) Permissions(filePath string) Permissions {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	return Permissions(info.Mode().Perm())
}

func (fs *LocalFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return os.Chmod(filePath, os.FileMode(perm))
}

func (fs *LocalFileSystem) User(filePath string) string {
	panic("not implemented")
}

func (fs *LocalFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (fs *LocalFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (fs *LocalFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (fs *LocalFileSystem) Touch(filePath string, perm ...Permissions) error {
	if fs.Exists(filePath) {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	} else {
		return fs.WriteAll(filePath, nil, perm...)
	}
}

func (fs *LocalFileSystem) MakeDir(filePath string, perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreateDirPermissions)
	return os.MkdirAll(filePath, os.FileMode(p))
}

func (fs *LocalFileSystem) ReadAll(filePath string) ([]byte, error) {
	return ioutil.ReadFile(filePath)
}

func (fs *LocalFileSystem) WriteAll(filePath string, data []byte, perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return ioutil.WriteFile(filePath, data, os.FileMode(p))
}

func (fs *LocalFileSystem) Append(filePath string, data []byte, perm ...Permissions) error {
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

func (fs *LocalFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	return os.OpenFile(filePath, os.O_RDONLY, 0400)
}

func (fs *LocalFileSystem) OpenWriter(filePath string, perm ...Permissions) (WriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
}

func (fs *LocalFileSystem) OpenAppendWriter(filePath string, perm ...Permissions) (io.WriteCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(p))
}

func (fs *LocalFileSystem) OpenReadWriter(filePath string, perm ...Permissions) (ReadWriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, os.FileMode(p))
}

func (fs *LocalFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, errors.New("not implemented")
	// events := make(chan WatchEvent, 1)
	// return events
}

func (fs *LocalFileSystem) Truncate(filePath string, size int64) error {
	return os.Truncate(filePath, size)
}

func (fs *LocalFileSystem) Rename(filePath string, newName string) error {
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

func (fs *LocalFileSystem) Move(filePath string, destPath string) error {
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

func (fs *LocalFileSystem) Remove(filePath string) error {
	return os.Remove(filePath)
}
