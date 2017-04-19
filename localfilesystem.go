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

func (local *LocalFileSystem) IsReadOnly() bool {
	return false
}

func (local *LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (local *LocalFileSystem) Name() string {
	return "local file system"
}

func (local *LocalFileSystem) File(uri ...string) File {
	return File(filepath.Clean(filepath.Join(uri...)))
}

func (local *LocalFileSystem) URL(cleanPath string) string {
	return LocalPrefix + filepath.ToSlash(cleanPath)
}

func (local *LocalFileSystem) CleanPath(uri ...string) string {
	return filepath.Clean(strings.TrimPrefix(filepath.Join(uri...), LocalPrefix))
}

func (local *LocalFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, local.Prefix())
	filePath = strings.TrimPrefix(filePath, local.Seperator())
	return strings.Split(filePath, local.Seperator())
}

func (local *LocalFileSystem) Seperator() string {
	return string(filepath.Separator)
}

func (local *LocalFileSystem) FileName(filePath string) string {
	return filepath.Base(filePath)
}

func (local *LocalFileSystem) Ext(filePath string) string {
	return filepath.Ext(filePath)
}

func (local *LocalFileSystem) Dir(filePath string) string {
	return filepath.Dir(filePath)
}

func (local *LocalFileSystem) Exists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

func (local *LocalFileSystem) IsDir(filePath string) bool {
	info, err := os.Stat(filePath)
	return err == nil && info.IsDir()
}

func (local *LocalFileSystem) Size(filePath string) int64 {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (local *LocalFileSystem) ModTime(filePath string) time.Time {
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (local *LocalFileSystem) ListDir(dirPath string, callback func(File) error, patterns []string) error {
	if !local.IsDir(dirPath) {
		return NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath)
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
			match, err := MatchAnyPatternLocal(name, patterns)
			if match {
				err = callback(local.File(dirPath, name))
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (local *LocalFileSystem) ListDirMax(dirPath string, n int, patterns []string) (files []File, err error) {
	if !local.IsDir(dirPath) {
		return nil, NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath)
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
			match, err := MatchAnyPatternLocal(name, patterns)
			if match {
				files = append(files, local.File(dirPath, name))
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

func (local *LocalFileSystem) Permissions(filePath string) Permissions {
	info, err := os.Stat(filePath)
	if err != nil {
		return NoPermissions
	}
	return Permissions(info.Mode().Perm())
}

func (local *LocalFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return os.Chmod(filePath, os.FileMode(perm))
}

func (local *LocalFileSystem) User(filePath string) string {
	panic("not implemented")
}

func (local *LocalFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (local *LocalFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (local *LocalFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (local *LocalFileSystem) Touch(filePath string, perm []Permissions) error {
	if local.Exists(filePath) {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	} else {
		return local.WriteAll(filePath, nil, perm)
	}
}

func (local *LocalFileSystem) MakeDir(filePath string, perm []Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreateDirPermissions)
	return os.MkdirAll(filePath, os.FileMode(p))
}

func (local *LocalFileSystem) ReadAll(filePath string) ([]byte, error) {
	return ioutil.ReadFile(filePath)
}

func (local *LocalFileSystem) WriteAll(filePath string, data []byte, perm []Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return ioutil.WriteFile(filePath, data, os.FileMode(p))
}

func (local *LocalFileSystem) Append(filePath string, data []byte, perm []Permissions) error {
	writer, err := local.OpenAppendWriter(filePath, perm)
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

func (local *LocalFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	return os.OpenFile(filePath, os.O_RDONLY, 0400)
}

func (local *LocalFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
}

func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(p))
}

func (local *LocalFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, os.FileMode(p))
}

func (local *LocalFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, errors.New("not implemented")
	// events := make(chan WatchEvent, 1)
	// return events
}

func (local *LocalFileSystem) Truncate(filePath string, size int64) error {
	return os.Truncate(filePath, size)
}

func (local *LocalFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	err := os.Rename(filePath, newPath)
	if err != nil {
		return err
	}
	filePath = newPath
	return nil
}

func (local *LocalFileSystem) Move(filePath string, destPath string) error {
	if local.IsDir(destPath) {
		destPath = filepath.Join(destPath, local.FileName(filePath))
	}
	err := os.Rename(filePath, destPath)
	if err != nil {
		return err
	}
	filePath = destPath
	return nil
}

func (local *LocalFileSystem) Remove(filePath string) error {
	return os.Remove(filePath)
}
