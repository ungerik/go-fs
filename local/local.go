package local

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ungerik/go-fs"
)

const Prefix = "file://"

var FS FileSystem

func init() {
	fs.Registry = append(fs.Registry, FS)
	fs.Default = FS
}

///////////////////////////////////////////////////////////////////////////////
// FileSystem

type FileSystem struct{}

func (FileSystem) Prefix() string {
	return Prefix
}

func (FileSystem) Get(url string) fs.File {
	return &File{strings.TrimPrefix(url, Prefix)}
}

func (FileSystem) Create(url string) (fs.File, error) {
	file := FS.Get(url)
	osFile, err := os.OpenFile(file.Path(), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	err = osFile.Close()
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (FileSystem) CreateDir(url string) (fs.File, error) {
	file := FS.Get(url)
	err := os.MkdirAll(file.Path(), 0700)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (FileSystem) Touch(url string) error {
	if file := FS.Get(url); file.Exists() {
		now := time.Now()
		return os.Chtimes(file.Path(), now, now)
	} else {
		_, err := FS.Create(url)
		return err
	}
}

///////////////////////////////////////////////////////////////////////////////
// File

type File struct {
	path string
}

func (file *File) URL() string {
	return Prefix + file.path
}

func (file *File) Path() string {
	return file.path
}

func (file *File) Name() string {
	return path.Base(file.path)
}

func (file *File) Ext() string {
	return path.Ext(file.path)
}

func (file *File) Exists() bool {
	_, err := os.Stat(file.path)
	return err == nil
}

func (file *File) IsDir() bool {
	info, err := os.Stat(file.path)
	return err == nil && info.IsDir()
}

func (file *File) Size() int64 {
	info, err := os.Stat(file.path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (file *File) ModTime() time.Time {
	info, err := os.Stat(file.path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (file *File) ListDir(callback func(fs.File) error, patterns ...string) error {
	if !file.IsDir() {
		return fs.ErrIsNotDirectory{file}
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
					match, err := filepaths.Match(pattern, name)
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

			err = callback(&File{path.Join(file.path, name)})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (file *File) Readable() (user, group, all bool) {
	info, err := os.Stat(file.path)
	if err != nil {
		return false, false, false
	}
	perm := info.Mode().Perm()
	return perm&0400 != 0, perm&0040 != 0, perm&0004 != 0
}

func (file *File) SetReadable(user, group, all bool) error {
	info, err := os.Stat(file.path)
	if err != nil {
		return err
	}
	perm := info.Mode().Perm()
	if user {
		perm |= 0400
	}
	if group {
		perm |= 0040
	}
	if all {
		perm |= 0004
	}
	return os.Chmod(file.path, perm)
}

func (file *File) Writable() (user, group, all bool) {
	info, err := os.Stat(file.path)
	if err != nil {
		return false, false, false
	}
	perm := info.Mode().Perm()
	return perm&0200 != 0, perm&0020 != 0, perm&0002 != 0
}

func (file *File) SetWritable(user, group, all bool) error {
	info, err := os.Stat(file.path)
	if err != nil {
		return err
	}
	perm := info.Mode().Perm()
	if user {
		perm |= 0200
	}
	if group {
		perm |= 0020
	}
	if all {
		perm |= 0002
	}
	return os.Chmod(file.path, perm)
}

func (file *File) Executable() (user, group, all bool) {
	info, err := os.Stat(file.path)
	if err != nil {
		return false, false, false
	}
	perm := info.Mode().Perm()
	return perm&0100 != 0, perm&0010 != 0, perm&0001 != 0
}

func (file *File) SetExecutable(user, group, all bool) error {
	info, err := os.Stat(file.path)
	if err != nil {
		return err
	}
	perm := info.Mode().Perm()
	if user {
		perm |= 0100
	}
	if group {
		perm |= 0010
	}
	if all {
		perm |= 0001
	}
	return os.Chmod(file.path, perm)
}

func (file *File) User() string {
	panic("not implemented")
}

func (file *File) SetUser(user string) error {
	panic("not implemented")
}

func (file *File) Group() string {
	panic("not implemented")
}

func (file *File) SetGroup(user string) error {
	panic("not implemented")
}

func (file *File) OpenReader() (io.ReadCloser, error) {
	return os.OpenFile(file.Path(), os.O_RDONLY, 0600)
}

func (file *File) OpenWriter() (io.WriteCloser, error) {
	return os.OpenFile(file.Path(), os.O_WRONLY|os.O_CREATE, 0600)
}

func (file *File) OpenAppendWriter() (io.WriteCloser, error) {
	return os.OpenFile(file.Path(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
}

func (file *File) OpenReadWriter() (io.ReadWriteCloser, error) {
	return os.OpenFile(file.Path(), os.O_RDWR|os.O_CREATE, 0600)
}

func (file *File) Watch() <-chan fs.WatchEvent {
	events := make(chan fs.WatchEvent, 1)
	events <- fs.WatchEvent{Err: errors.New("not implemented")}
	return events
}
