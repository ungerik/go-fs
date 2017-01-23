package fs

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func newLocalFile(uri string) *LocalFile {
	path := filepath.Clean(strings.TrimPrefix(uri, LocalPrefix))
	return &LocalFile{path: path}
}

type LocalFile struct {
	path string
}

func (*LocalFile) FileSystem() FileSystem {
	return Local
}

func (file *LocalFile) String() string {
	return fmt.Sprintf("%s (%s)", file.Path(), file.FileSystem().Name())
}

func (file *LocalFile) URN() string {
	return filepath.ToSlash(file.path)
}

func (file *LocalFile) URL() string {
	return LocalPrefix + file.URN()
}

func (file *LocalFile) Path() string {
	return file.path
}

func (file *LocalFile) Dir() string {
	return filepath.Dir(file.path)
}

func (file *LocalFile) Name() string {
	return filepath.Base(file.path)
}

func (file *LocalFile) Ext() string {
	return strings.ToLower(filepath.Ext(file.path))
}

func (file *LocalFile) Exists() bool {
	_, err := os.Stat(file.path)
	return err == nil
}

func (file *LocalFile) IsDir() bool {
	info, err := os.Stat(file.path)
	return err == nil && info.IsDir()
}

func (file *LocalFile) Size() int64 {
	info, err := os.Stat(file.path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (file *LocalFile) ModTime() time.Time {
	info, err := os.Stat(file.path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func matchPatterns(name string, patterns []string) (bool, error) {
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

func (file *LocalFile) ListDir(callback func(File) error, patterns ...string) error {
	if !file.IsDir() {
		return ErrIsNotDirectory{file}
	}

	f, err := os.Open(file.path)
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
			match, err := matchPatterns(name, patterns)
			if match {
				file := newLocalFile(filepath.Join(file.path, name))
				err = callback(file)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (file *LocalFile) ListDirMax(n int, patterns ...string) (files []File, err error) {
	if !file.IsDir() {
		return nil, ErrIsNotDirectory{file}
	}

	f, err := os.Open(file.path)
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
			match, err := matchPatterns(name, patterns)
			if match {
				file := newLocalFile(filepath.Join(file.path, name))
				files = append(files, file)
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

func (file *LocalFile) Permissions() Permissions {
	info, err := os.Stat(file.path)
	if err != nil {
		return 0
	}
	return Permissions(info.Mode().Perm())
}

func (file *LocalFile) SetPermissions(perm Permissions) error {
	return os.Chmod(file.path, os.FileMode(perm))
}

func (file *LocalFile) User() string {
	panic("not implemented")
}

func (file *LocalFile) SetUser(user string) error {
	panic("not implemented")
}

func (file *LocalFile) Group() string {
	panic("not implemented")
}

func (file *LocalFile) SetGroup(user string) error {
	panic("not implemented")
}

func (file *LocalFile) Touch(perm ...Permissions) error {
	if file.Exists() {
		now := time.Now()
		return os.Chtimes(file.path, now, now)
	} else {
		return file.WriteAll(nil, perm...)
	}
}

func (file *LocalFile) MakeDir(perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.MkdirAll(file.path, os.FileMode(p))
}

func (file *LocalFile) ReadAll() ([]byte, error) {
	return ioutil.ReadFile(file.path)
}

func (file *LocalFile) WriteAll(data []byte, perm ...Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return ioutil.WriteFile(file.path, data, os.FileMode(p))
}

func (file *LocalFile) Append(data []byte, perm ...Permissions) error {
	writer, err := file.OpenAppendWriter(perm...)
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

func (file *LocalFile) OpenReader() (ReadSeekCloser, error) {
	return os.OpenFile(file.path, os.O_RDONLY, 0400)
}

func (file *LocalFile) OpenWriter(perm ...Permissions) (WriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
}

func (file *LocalFile) OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(p))
}

func (file *LocalFile) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return os.OpenFile(file.path, os.O_RDWR|os.O_CREATE, os.FileMode(p))
}

func (file *LocalFile) Watch() (<-chan WatchEvent, error) {
	return nil, errors.New("not implemented")
	// events := make(chan WatchEvent, 1)
	// return events
}

func (file *LocalFile) Truncate(size int64) error {
	return os.Truncate(file.path, size)
}

func (file *LocalFile) Rename(newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separatos: " + newName)
	}
	newPath := filepath.Join(file.Dir(), newName)
	err := os.Rename(file.path, newPath)
	if err != nil {
		return err
	}
	file.path = newPath
	return nil
}

func (file *LocalFile) Move(destination File) error {
	destPath := destination.Path()
	if destination.IsDir() {
		destPath = filepath.Join(destPath, file.Name())
	}
	err := os.Rename(file.path, destPath)
	if err != nil {
		return err
	}
	file.path = destPath
	return nil
}

func (file *LocalFile) Remove() error {
	return os.Remove(file.path)
}
