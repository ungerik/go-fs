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

func (file *LocalFile) ListDir(callback func(File) error, patterns ...string) error {
	if !file.IsDir() {
		return ErrIsNotDirectory{file}
	}

	osFile, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer osFile.Close()

	for {
		names, err := osFile.Readdirnames(64)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		for _, name := range names {
			if len(patterns) > 0 {
				anyMatch := false
				for _, pattern := range patterns {
					match, err := filepath.Match(pattern, name)
					if err != nil {
						return err
					}
					if match {
						anyMatch = true
						break
					}
				}
				if !anyMatch {
					continue
				}
			}

			err = callback(newLocalFile(filepath.Join(file.path, name)))
			if err != nil {
				return err
			}
		}
	}
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

func (file *LocalFile) Remove() error {
	return os.Remove(file.path)
}