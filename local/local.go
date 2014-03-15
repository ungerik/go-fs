package local

import (
	"io"
	"os"
	"path"
	"time"

	"github.com/ungerik/go-fs"
)

var FileSystem fileSystem

func init() {
	fs.All = append(fs.All, FileSystem)
	fs.Default = FileSystem
}

///////////////////////////////////////////////////////////////////////////////
// fileSystem

type fileSystem struct{}

func (fileSystem) Prefix() string {
	return "file://"
}

func (fileSystem) Info(url string) fs.File {
	panic("")
}

func (fileSystem) Create(url string) (fs.File, error) {
	panic("")
}

func (fileSystem) CreateDir(url string) (fs.File, error) {
	panic("")
}

///////////////////////////////////////////////////////////////////////////////
// File

type File struct {
	path string
}

func (file *File) URL() string {
	return "filt://" + file.path
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

func (file *File) ListDir(done <-chan struct{}) (<-chan fs.File, <-chan error) {
	errs := make(chan error, 1)

	if !file.IsDir() {
		errs <- fs.ErrIsNotDirectory{file}
		return nil, errs
	}
	osFile, err := os.Open(file.path)
	if err != nil {
		errs <- err
		return nil, errs
	}

	files := make(chan fs.File, 64)

	go func() {
		defer close(files)
		defer osFile.Close()

		for names, err := osFile.Readdirnames(64); ; {
			if err != nil {
				if err != io.EOF {
					errs <- err
				}
				return
			}

			for i := range names {
				select {
				case files <- &File{path.Join(file.path, names[i])}:
				case <-done:
					return
				}
			}
		}
	}()

	return files, errs
}

func (file *File) ListDirMatch(pattern string, done <-chan struct{}) (<-chan fs.File, <-chan error) {
	files, errs := file.ListDir(done)
	return fs.Match(pattern, done, files, errs)
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
	panic("not implemented")
}

func (file *File) OpenWriter() (io.WriteCloser, error) {
	panic("not implemented")
}

func (file *File) OpenReadWriter() (io.ReadWriteCloser, error) {
	panic("not implemented")
}
