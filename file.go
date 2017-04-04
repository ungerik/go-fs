package fs

import (
	"fmt"
	"io"
	"time"
)

type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

type WriteSeekCloser interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

type ReadWriteSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

type File string

func (file File) FileSystem() FileSystem {
	return getFileSystem(string(file))
}

func (file File) String() string {
	return fmt.Sprintf("%s (%s)", file.Path(), file.FileSystem().Name())
}

func (file File) URN() string {
	return file.FileSystem().URN(file.Path())
}

func (file File) URL() string {
	return file.FileSystem().URL(file.Path())
}

func (file File) Path() string {
	return file.FileSystem().CleanPath(string(file))
}

func (file File) Name() string {
	return file.FileSystem().FileName(file.Path())
}

func (file File) Ext() string {
	return file.FileSystem().Ext(file.Path())
}

func (file File) RemoveExt() File {
	return file[:len(file)-len(file.Ext())]
}

func (file File) ReplaceExt(newExt string) File {
	return file.RemoveExt() + File(newExt)
}

func (file File) Dir() File {
	return File(file.FileSystem().Dir(file.Path()))
}

func (file File) Relative(pathParts ...string) File {
	return file.FileSystem().File(append([]string{file.Path()}, pathParts...)...)
}

func (file File) Exists() bool {
	return file.FileSystem().Exists(file.Path())
}

func (file File) IsDir() bool {
	return file.FileSystem().IsDir(file.Path())
}

func (file File) Size() int64 {
	return file.FileSystem().Size(file.Path())
}

func (file File) ModTime() time.Time {
	return file.FileSystem().ModTime(file.Path())
}

func (file File) ListDir(callback func(File) error, patterns ...string) error {
	return file.FileSystem().ListDir(file.Path(), callback, patterns...)
}

func (file File) ListDirMax(n int, patterns ...string) (files []File, err error) {
	return file.FileSystem().ListDirMax(file.Path(), n, patterns...)
}

func (file File) Permissions() Permissions {
	return file.FileSystem().Permissions(file.Path())
}

func (file File) SetPermissions(perm Permissions) error {
	return file.FileSystem().SetPermissions(file.Path(), perm)
}

func (file File) User() string {
	return file.FileSystem().User(file.Path())
}

func (file File) SetUser(user string) error {
	return file.FileSystem().SetUser(file.Path(), user)
}

func (file File) Group() string {
	return file.FileSystem().Group(file.Path())
}

func (file File) SetGroup(group string) error {
	return file.FileSystem().SetGroup(file.Path(), group)
}

func (file File) Touch(perm ...Permissions) error {
	return file.FileSystem().Touch(file.Path(), perm...)
}

func (file File) MakeDir(perm ...Permissions) error {
	return file.FileSystem().MakeDir(file.Path(), perm...)
}

func (file File) ReadAll() ([]byte, error) {
	return file.FileSystem().ReadAll(file.Path())
}

// WriteTo implements the io.WriterTo interface
func (file File) WriteTo(writer io.Writer) (n int64, err error) {
	reader, err := file.OpenReader()
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	return io.Copy(writer, reader)
}

// ReadFrom implements the io.ReaderFrom interface,
// the file is writter with the existing permissions if it exists,
// or with the default write permissions if it does not exist yet.
func (file File) ReadFrom(reader io.Reader) (n int64, err error) {
	var writer io.WriteCloser
	existingPerm := file.Permissions()
	if existingPerm != NoPermissions {
		writer, err = file.OpenWriter(existingPerm)
	} else {
		writer, err = file.OpenWriter()
	}
	if err != nil {
		return 0, err
	}
	defer writer.Close()
	return io.Copy(writer, reader)
}

func (file File) WriteAll(data []byte, perm ...Permissions) error {
	return file.FileSystem().WriteAll(file.Path(), data, perm...)
}

func (file File) Append(data []byte, perm ...Permissions) error {
	return file.FileSystem().Append(file.Path(), data, perm...)
}

func (file File) OpenReader() (ReadSeekCloser, error) {
	return file.FileSystem().OpenReader(file.Path())
}

func (file File) OpenWriter(perm ...Permissions) (WriteSeekCloser, error) {
	return file.FileSystem().OpenWriter(file.Path(), perm...)
}

func (file File) OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error) {
	return file.FileSystem().OpenAppendWriter(file.Path(), perm...)
}

func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	return file.FileSystem().OpenReadWriter(file.Path(), perm...)
}

func (file File) Watch() (<-chan WatchEvent, error) {
	return file.FileSystem().Watch(file.Path())
}

func (file File) Truncate(size int64) error {
	return file.FileSystem().Truncate(file.Path(), size)
}

func (file File) Rename(newName string) error {
	return file.FileSystem().Rename(file.Path(), newName)
}

func (file File) Move(destination File) error {
	return file.FileSystem().Move(file.Path(), destination.Path())
}

func (file File) Remove() error {
	return file.FileSystem().Remove(file.Path())
}
