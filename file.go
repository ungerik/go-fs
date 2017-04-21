package fs

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ReadSeekCloser combines the interfaces
// io.Reader
// io.ReaderAt
// io.Seeker
// io.Closer
type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

// WriteSeekCloser combines the interfaces
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
type WriteSeekCloser interface {
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

// ReadWriteSeekCloser combines the interfaces
// io.Reader
// io.ReaderAt
// io.Writer
// io.WriterAt
// io.Seeker
// io.Closer
type ReadWriteSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer
}

// File is a local file system path or URI
// describing the location of a file.
type File string

// FileSystem returns the FileSystem of the File.
// Defaults to Local if not a complete URI.
func (file File) FileSystem() FileSystem {
	return getFileSystem(string(file))
}

func (file File) String() string {
	return fmt.Sprintf("%s (%s)", file.Path(), file.FileSystem().Name())
}

// URL of the file
func (file File) URL() string {
	return file.FileSystem().URL(file.Path())
}

// Path returns the cleaned path of the file.
// It may differ from the string value of File
// because it will be cleaned depending on the FileSystem
func (file File) Path() string {
	return file.FileSystem().CleanPath(string(file))
}

// Name returns the name part of the file path,
// which is usually the string after the last path Separator.
func (file File) Name() string {
	return file.FileSystem().FileName(file.Path())
}

// Ext returns the extension of file name including the extension separator.
// Example: File("image.png").Ext() == ".png"
func (file File) Ext() string {
	return file.FileSystem().Ext(file.Path())
}

// ExtLower returns the lower case extension of file name including the extension separator.
// Example: File("Image.PNG").ExtLower() == ".png"
func (file File) ExtLower() string {
	return strings.ToLower(file.Ext())
}

// RemoveExt returns a File with a path where the extension is removed.
func (file File) RemoveExt() File {
	return file[:len(file)-len(file.Ext())]
}

// ReplaceExt returns a File with a path where the file name extension is replaced with newExt.
func (file File) ReplaceExt(newExt string) File {
	if len(newExt) == 0 || newExt[0] != '.' {
		newExt = "." + newExt
	}
	return file.RemoveExt() + File(newExt)
}

// Dir returns the parent directory of the File.
func (file File) Dir() File {
	return File(file.FileSystem().Dir(file.Path()))
}

// Relative returns a File with a path relative to this file.
// Every part of pathParts is a subsequent directory of file
// concaternated with a path Separator.
func (file File) Relative(pathParts ...string) File {
	if file != "" {
		pathParts = append([]string{file.Path()}, pathParts...)
	}
	return file.FileSystem().File(pathParts...)
}

func (file File) Stat(filePath string) FileInfo {
	return file.FileSystem().Stat(file.Path())
}

// Exists returns a file or directory with the path of File exists.
func (file File) Exists() bool {
	return file.FileSystem().Stat(file.Path()).Exists
}

// IsDir returns a directory with the path of File exists.
func (file File) IsDir() bool {
	return file.FileSystem().Stat(file.Path()).IsDir
}

// Size returns the size of the file or 0 if it does not exist or is a directory.
func (file File) Size() int64 {
	return file.FileSystem().Stat(file.Path()).Size
}

func (file File) ModTime() time.Time {
	return file.FileSystem().Stat(file.Path()).ModTime
}

func (file File) ListDir(callback func(File) error, patterns ...string) error {
	return file.FileSystem().ListDir(file.Path(), callback, patterns)
}

func (file File) ListDirRecursive(callback func(File) error, patterns ...string) error {
	return file.ListDir(func(f File) (err error) {
		if f.IsDir() {
			err := f.ListDirRecursive(callback)
			// Don't mind files that have been deleted while iterating
			if IsErrDoesNotExist(err) {
				return nil
			}
			return err
		}
		return callback(f)
	}, patterns...)
}

func (file File) ListDirMax(max int, patterns ...string) (files []File, err error) {
	return file.FileSystem().ListDirMax(file.Path(), max, patterns)
}

func (file File) ListDirRecursiveMax(max int, patterns ...string) (files []File, err error) {
	return ListDirMaxImpl(file.Path(), max, patterns, func(dirPath string, callback func(File) error, patterns []string) error {
		return file.ListDirRecursive(callback, patterns...)
	})
}

func (file File) Permissions() Permissions {
	return file.FileSystem().Stat(file.Path()).Permissions
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
	return file.FileSystem().Touch(file.Path(), perm)
}

func (file File) MakeDir(perm ...Permissions) error {
	return file.FileSystem().MakeDir(file.Path(), perm)
}

// MakeAllDirs creates all directories up to this one
func (file File) MakeAllDirs(perm ...Permissions) (err error) {
	parts := file.FileSystem().SplitPath(file.Path())
	var dir File
	for _, part := range parts {
		dir = dir.Relative(part)
		if !dir.Exists() {
			err = dir.MakeDir(perm...)
			if err != nil {
				return err
			}
		} else if !dir.IsDir() {
			return errors.New("MakeAllDirs: file instead of directory in path: " + file.Path())
		}
	}
	return nil
}

func (file File) ReadAll() ([]byte, error) {
	return file.FileSystem().ReadAll(file.Path())
}

func (file File) ReadAllString() (string, error) {
	data, err := file.ReadAll()
	if data == nil || err != nil {
		return "", err
	}
	return string(data), nil
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
	return file.FileSystem().WriteAll(file.Path(), data, perm)
}

func (file File) WriteAllString(data string, perm ...Permissions) error {
	return file.WriteAll([]byte(data), perm...)
}

func (file File) Append(data []byte, perm ...Permissions) error {
	return file.FileSystem().Append(file.Path(), data, perm)
}

func (file File) OpenReader() (ReadSeekCloser, error) {
	return file.FileSystem().OpenReader(file.Path())
}

func (file File) OpenWriter(perm ...Permissions) (WriteSeekCloser, error) {
	return file.FileSystem().OpenWriter(file.Path(), perm)
}

func (file File) OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error) {
	return file.FileSystem().OpenAppendWriter(file.Path(), perm)
}

func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	return file.FileSystem().OpenReadWriter(file.Path(), perm)
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

// Remove deletes the file.
func (file File) Remove() error {
	return file.FileSystem().Remove(file.Path())
}

// RemoveRecursive deletes the file or if it's a directory
// the complete recursive directory tree.
func (file File) RemoveRecursive() error {
	if file.IsDir() {
		err := file.ListDir(func(f File) error {
			err := f.RemoveRecursive()
			// Ignore files that have been deleted,
			// after all we wanted to get rid of the in the first place,
			// so this is not an error for us
			if err != nil {
				if _, notExist := err.(*ErrDoesNotExist); notExist {
					return nil
				}
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return file.Remove()
}

func (file File) ReadJSON(output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func (file File) WriteJSON(input interface{}, indent ...string) (err error) {
	var data []byte
	if len(indent) == 0 {
		data, err = json.Marshal(input)
	} else {
		data, err = json.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return err
	}
	return file.WriteAll(data)
}

func (file File) ReadXML(output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return xml.Unmarshal(data, output)
}

func (file File) WriteXML(input interface{}, indent ...string) (err error) {
	var data []byte
	if len(indent) == 0 {
		data, err = xml.Marshal(input)
	} else {
		data, err = xml.MarshalIndent(input, "", strings.Join(indent, ""))
	}
	if err != nil {
		return err
	}
	data = append([]byte(xml.Header), data...)
	return file.WriteAll(data)
}
