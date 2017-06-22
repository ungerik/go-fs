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
	return GetFileSystem(string(file))
}

// ParseRawURI returns a FileSystem for the passed URI and the path component within that file system.
// Returns the local file system if no other file system could be identified.
func (file File) ParseRawURI() (fs FileSystem, fsPath string) {
	return ParseRawURI(string(file))
}

// RawURI rurns the string value of File.
func (file File) RawURI() string {
	return string(file)
}

func (file File) String() string {
	return fmt.Sprintf("%s (%s)", file.Path(), file.FileSystem().Name())
}

// URL of the file
func (file File) URL() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.URL(path)
}

// Path returns the cleaned path of the file.
// It may differ from the string value of File
// because it will be cleaned depending on the FileSystem
func (file File) Path() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.CleanPath(path)
}

// Name returns the name part of the file path,
// which is usually the string after the last path Separator.
func (file File) Name() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.FileName(path)
}

// Ext returns the extension of file name including the extension separator.
// Example: File("image.png").Ext() == ".png"
func (file File) Ext() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Ext(path)
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
	fileSystem, path := file.ParseRawURI()
	return File(fileSystem.Dir(path))
}

// Relative returns a File with a path relative to this file.
// Every part of pathParts is a subsequent directory of file
// concaternated with a path Separator.
func (file File) Relative(pathParts ...string) File {
	if len(pathParts) == 0 {
		return file
	}
	fileSystem, path := file.ParseRawURI()
	if file != "" {
		pathParts = append([]string{path}, pathParts...)
	}
	return fileSystem.File(pathParts...)
}

// Relativef returns a File with a path relative to this file,
// using the methods arguments with fmt.Sprintf to create the relativ path.
func (file File) Relativef(format string, args ...interface{}) File {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.File(path, fmt.Sprintf(format, args...))
}

// Stat returns FileInfo.
func (file File) Stat() FileInfo {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Stat(path)
}

// Exists returns a file or directory with the path of File exists.
func (file File) Exists() bool {
	return file.Stat().Exists
}

// IsDir returns a directory with the path of File exists.
func (file File) IsDir() bool {
	return file.Stat().IsDir
}

// IsRegular reports if this is a regular file.
func (file File) IsRegular() bool {
	return file.Stat().IsRegular
}

// Size returns the size of the file or 0 if it does not exist or is a directory.
func (file File) Size() int64 {
	return file.Stat().Size
}

// ContentHash returns a Dropbox compatible content hash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// An empty string is returned in case of an error or when the file is a directory.
// See https://www.dropbox.com/developers/reference/content-hash
func (file File) ContentHash() string {
	info := file.Stat()
	hash := info.ContentHash
	if !info.IsDir && hash == "" {
		reader, err := file.OpenReader()
		if err == nil {
			defer reader.Close()
			hash, _ = ContentHash(reader)
		}
	}
	return hash
}

func (file File) ModTime() time.Time {
	return file.Stat().ModTime
}

func (file File) Permissions() Permissions {
	return file.Stat().Permissions
}

func (file File) SetPermissions(perm Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetPermissions(path, perm)
}

func (file File) ListDir(callback func(File) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDir(path, callback, patterns)
}

// ListDirRecursive returns only files.
// patterns are only applied to files, not to directories
func (file File) ListDirRecursive(callback func(File) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirRecursive(path, callback, patterns)
}

func (file File) ListDirMax(max int, patterns ...string) (files []File, err error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirMax(path, max, patterns)
}

func (file File) ListDirRecursiveMax(max int, patterns ...string) (files []File, err error) {
	listDirFunc := ListDirFunc(func(callback func(File) error) error {
		return file.ListDirRecursive(callback, patterns...)
	})
	return listDirFunc.ListDirMaxImpl(max)
}

// ListDirChan returns listed files over a channel.
// An error or nil will returned from the error channel.
// The file channel will be closed after sending all files.
// If cancel is not nil and an error is sent to this channel, then the listing will be canceled
// and the error returned in the error channel returned by the method.
// See pipeline pattern: http://blog.golang.org/pipelines
func (file File) ListDirChan(cancel <-chan error, patterns ...string) (<-chan File, <-chan error) {
	files := make(chan File)
	errs := make(chan error, 1)

	go func() {
		defer close(files)

		callback := func(f File) error {
			select {
			case files <- f:
				return nil
			case err := <-cancel:
				return err
			}
		}

		errs <- file.ListDir(callback, patterns...)
	}()

	return files, errs
}

// ListDirRecursiveChan returns listed files over a channel.
// An error or nil will returned from the error channel.
// The file channel will be closed after sending all files.
// If cancel is not nil and an error is sent to this channel, then the listing will be canceled
// and the error returned in the error channel returned by the method.
// See pipeline pattern: http://blog.golang.org/pipelines
func (file File) ListDirRecursiveChan(cancel <-chan error, patterns ...string) (<-chan File, <-chan error) {
	files := make(chan File)
	errs := make(chan error, 1)

	go func() {
		defer close(files)

		callback := func(f File) error {
			select {
			case files <- f:
				return nil
			case err := <-cancel:
				return err
			}
		}

		errs <- file.ListDirRecursive(callback, patterns...)
	}()

	return files, errs
}

func (file File) User() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.User(path)
}

func (file File) SetUser(user string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetUser(path, user)
}

func (file File) Group() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Group(path)
}

func (file File) SetGroup(group string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetGroup(path, group)
}

func (file File) Touch(perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Touch(path, perm)
}

func (file File) MakeDir(perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.MakeDir(path, perm)
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
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ReadAll(path)
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
	fileSystem, path := file.ParseRawURI()
	return fileSystem.WriteAll(path, data, perm)
}

func (file File) WriteAllString(data string, perm ...Permissions) error {
	return file.WriteAll([]byte(data), perm...)
}

func (file File) Append(data []byte, perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Append(path, data, perm)
}

func (file File) OpenReader() (ReadSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReader(path)
}

func (file File) OpenWriter(perm ...Permissions) (WriteSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenWriter(path, perm)
}

func (file File) OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenAppendWriter(path, perm)
}

func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReadWriter(path, perm)
}

func (file File) Watch() (<-chan WatchEvent, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Watch(path)
}

func (file File) Truncate(size int64) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Truncate(path, size)
}

// Rename changes the name of a file where newName is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the with within its directdory.
func (file File) Rename(newName string) (renamedFile File, err error) {
	fileSystem, path := file.ParseRawURI()
	err = fileSystem.Rename(path, newName)
	if err != nil {
		return "", err
	}
	return file.Dir().Relative(newName), nil
}

// MoveTo moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
func (file File) MoveTo(destination File) error {
	return Move(file, destination)
}

// Remove deletes the file.
func (file File) Remove() error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Remove(path)
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
			if IsErrDoesNotExist(err) {
				return nil
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return file.Remove()
}

// RemoveDirContents removes all files in this directory,
// or if given all files with patterns from the this directory.
func (file File) RemoveDirContents(patterns ...string) error {
	return file.ListDir(func(f File) error {
		err := f.Remove()
		// Ignore files that have been deleted,
		// after all we wanted to get rid of the in the first place,
		// so this is not an error for us
		if IsErrDoesNotExist(err) {
			return nil
		}
		return err
	}, patterns...)
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
