package fs

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// InvalidFile is a file with an empty path and thus invalid.
const InvalidFile = File("")

// File is a local file system path or a complete URI.
// It is a string underneath, so string literals can be passed everywhere a File is expected.
// Marshalling functions that use reflection will also work out of the box
// when they detect that File is of kind reflect.String.
// File implements FileReader.
type File string

// FileSystem returns the FileSystem of the File.
// Defaults to Local if not a complete URI,
// or Invalid for an empty path.
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

// String returns the path and meta information for the File.
// See RawURI to just get the string value of it.
// String implements the fmt.Stringer interface.
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
	return fileSystem.JoinCleanPath(path)
}

// PathWithSlashes returns the cleaned path of the file
// always using the slash '/' as separator.
// It may differ from the string value of File
// because it will be cleaned depending on the FileSystem
func (file File) PathWithSlashes() string {
	fileSystem, path := file.ParseRawURI()
	path = fileSystem.JoinCleanPath(path)
	if sep := fileSystem.Separator(); sep != "/" {
		path = strings.Replace(path, sep, "/", -1)
	}
	return path
}

// LocalPath returns the cleaned local file-system path of the file,
// or an empty string if it is not on the local file system.
func (file File) LocalPath() string {
	fileSystem, path := file.ParseRawURI()
	if fileSystem != Local {
		return ""
	}
	return fileSystem.JoinCleanPath(path)
}

// MustLocalPath returns the cleaned local file-system path of the file,
// or panics if it is not on the local file system or an empty path.
func (file File) MustLocalPath() string {
	if file == "" {
		panic("empty file path")
	}
	localPath := file.LocalPath()
	if localPath == "" {
		panic(fmt.Sprintf("not a local file-system path: %q", string(file)))
	}
	return localPath
}

// Name returns the name part of the file path,
// which is usually the string after the last path Separator.
func (file File) Name() string {
	_, name := file.DirAndName()
	return name
}

// Dir returns the parent directory of the File.
func (file File) Dir() File {
	dir, _ := file.DirAndName()
	return dir
}

// DirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
// If filePath is the root of the file systeme, then an empty string will be returned for name.
func (file File) DirAndName() (dir File, name string) {
	fileSystem, path := file.ParseRawURI()
	dirPath, name := fileSystem.DirAndName(path)
	return File(dirPath), name
}

// VolumeName returns the name of the volume at the beginning of the file path,
// or an empty string if the path has no volume.
// A volume is for example "C:" on Windows
func (file File) VolumeName() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.VolumeName(path)
}

// Ext returns the extension of file name including the point, or an empty string.
// Example: File("image.png").Ext() == ".png"
func (file File) Ext() string {
	return fsimpl.Ext(string(file))
}

// ExtLower returns the lower case extension of file name including the point, or an empty string.
// Example: File("Image.PNG").ExtLower() == ".png"
func (file File) ExtLower() string {
	return strings.ToLower(file.Ext())
}

// TrimExt returns a File with a path where the extension is removed.
// Note that this does not rename an actual existing file.
func (file File) TrimExt() File {
	return File(fsimpl.TrimExt(string(file)))
}

// Join returns a new File with pathParts cleaned and joined to the current File's URI.
// Every element of pathParts is a subsequent directory or file
// that will be appended to the File URI with a path separator.
// The resulting URI path will be cleaned, removing relative directory names like "..".
func (file File) Join(pathParts ...string) File {
	if len(pathParts) == 0 {
		return file
	}
	fileSystem, path := file.ParseRawURI()
	if path != "" {
		pathParts = append([]string{path}, pathParts...)
	}
	return fileSystem.JoinCleanFile(pathParts...)
}

// Joinf returns a new File with smf.Sprintf(format, args...) cleaned and joined to the current File's URI.
// The resulting URI path will be cleaned, removing relative directory names like "..".
func (file File) Joinf(format string, args ...interface{}) File {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.JoinCleanFile(path, fmt.Sprintf(format, args...))
}

// Stat returns FileInfo. The FileInfo.ContentHash field is optional.
func (file File) Stat() FileInfo {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Stat(path)
}

// StatWithContentHash returns a FileInfo, but in contrast to Stat
// it always fills the ContentHash field.
func (file File) StatWithContentHash() (FileInfo, error) {
	info := file.Stat()
	if !info.IsDir && info.ContentHash == "" {
		reader, err := file.OpenReader()
		if err != nil {
			return FileInfo{}, err
		}
		defer reader.Close()
		info.ContentHash, err = fsimpl.ContentHash(reader)
		if err != nil {
			return FileInfo{}, err
		}
	}
	return info, nil
}

// Exists returns a file or directory with the path of File exists.
func (file File) Exists() bool {
	return file.Stat().Exists
}

// IsDir returns a directory with the path of File exists.
func (file File) IsDir() bool {
	return file.Stat().IsDir
}

func (file File) IsAbsolute() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsAbsPath(path)
}

func (file File) MakeAbsolute() File {
	fileSystem, path := file.ParseRawURI()
	return File(fileSystem.AbsPath(path))
}

// IsRegular reports if this is a regular file.
func (file File) IsRegular() bool {
	return file.Stat().IsRegular
}

// IsEmptyDir returns if file is an empty directory.
func (file File) IsEmptyDir() bool {
	l, err := file.ListDirMax(1)
	return len(l) == 0 && err == nil
}

// IsHidden returns true if the filename begins with a dot,
// or if on Windows the hidden file attribute is set.
func (file File) IsHidden() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsHidden(path)
}

// IsSymbolicLink returns if the file is a symbolic link
// Use LocalFileSystem.CreateSymbolicLink and LocalFileSystem.ReadSymbolicLink
// to handle symbolic links.
func (file File) IsSymbolicLink() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsSymbolicLink(path)
}

// Size returns the size of the file or 0 if it does not exist or is a directory.
func (file File) Size() int64 {
	return file.Stat().Size
}

// ContentHash returns a Dropbox compatible content hash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
// See https://www.dropbox.com/developers/reference/content-hash
func (file File) ContentHash() (string, error) {
	info := file.Stat()
	if info.IsDir || info.ContentHash != "" {
		return info.ContentHash, nil
	}
	reader, err := file.OpenReader()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return fsimpl.ContentHash(reader)
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

// ListDir calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDir(callback func(File) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfo(path, FileCallback(callback).FileInfoCallback, patterns)
}

// ListDirInfo calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfo(callback func(File, FileInfo) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfo(path, callback, patterns)
}

// ListDirRecursive returns only files.
// patterns are only applied to files, not to directories
func (file File) ListDirRecursive(callback func(File) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfoRecursive(path, FileCallback(callback).FileInfoCallback, patterns)
}

// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
// recursing into all sub-directories.
// If any patterns are passed, then only files (not directories) with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfoRecursive(callback func(File, FileInfo) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfoRecursive(path, callback, patterns)
}

// ListDirMax returns at most max files and directories in dirPath.
// A max value of -1 returns all files.
// If any patterns are passed, then only files or directories with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirMax(max int, patterns ...string) (files []File, err error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirMax(path, max, patterns)
}

func (file File) ListDirRecursiveMax(max int, patterns ...string) (files []File, err error) {
	return ListDirMaxImpl(max, func(callback func(File) error) error {
		return file.ListDirRecursive(callback, patterns...)
	})
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
	if file.IsDir() {
		return nil
	}

	fileSystem, path := file.ParseRawURI()
	return fileSystem.MakeDir(path, perm)
}

// MakeAllDirs creates all directories up to this one,
// does not return an error if the directories already exist
func (file File) MakeAllDirs(perm ...Permissions) (err error) {
	if file == "" {
		return nil
	}
	info := file.Stat()
	if info.IsDir {
		return nil
	} else if info.Exists {
		return NewErrIsNotDirectory(file)
	}

	dir, name := file.DirAndName()
	if name != "" {
		// if name != "" then dir is not the root
		// so we can attempt to make the dir
		err = dir.MakeAllDirs(perm...)
		if err != nil {
			return err
		}
	}
	return file.MakeDir(perm...)
}

// ReadAll reads and returns all bytes of the file
func (file File) ReadAll() (data []byte, err error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ReadAll(path)
}

// ReadAllContentHash reads and returns all bytes of the file
// together with a Dropbox compatible content hash.
// See https://www.dropbox.com/developers/reference/content-hash
func (file File) ReadAllContentHash() (data []byte, hash string, err error) {
	data, err = file.ReadAll()
	if err != nil {
		return nil, "", err
	}
	return data, fsimpl.ContentHashBytes(data), nil
}

// ReadAllString reads the complete file and returns the content as string.
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

func (file File) WriteAllString(str string, perm ...Permissions) error {
	return file.WriteAll([]byte(str), perm...)
}

func (file File) Append(data []byte, perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Append(path, data, perm)
}

func (file File) AppendString(str string, perm ...Permissions) error {
	return file.Append([]byte(str), perm...)
}

// OpenReader opens the file and returns a io.ReadCloser that has be closed after reading
func (file File) OpenReader() (io.ReadCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReader(path)
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// If the FileSystem implementation doesn't support ReadSeekCloser,
// then the complete file is read into memory and wrapped with a ReadSeekCloser.
// Warning: this can use up a lot of memory for big files.
func (file File) OpenReadSeeker() (ReadSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	readCloser, err := fileSystem.OpenReader(path)
	if err != nil {
		return nil, err
	}
	if r, ok := readCloser.(ReadSeekCloser); ok {
		return r, nil
	}

	defer readCloser.Close()

	return fsimpl.NewReadonlyFileBufferReadAll(readCloser)
}

func (file File) OpenWriter(perm ...Permissions) (io.WriteCloser, error) {
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
// it only changes the name of the file within its directdory.
func (file File) Rename(newName string) (renamedFile File, err error) {
	fileSystem, path := file.ParseRawURI()
	err = fileSystem.Rename(path, newName)
	if err != nil {
		return "", err
	}
	return file.Dir().Join(newName), nil
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
		err := file.RemoveDirContentsRecursive()
		if err != nil {
			return err
		}
	}
	return file.Remove()
}

// RemoveDirContentsRecursive deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursive() error {
	return file.ListDir(func(f File) error {
		err := f.RemoveRecursive()
		// Ignore files that have been deleted,
		// after all we wanted to get rid of the in the first place,
		// so this is not an error for us
		if IsErrDoesNotExist(err) {
			return nil
		}
		return err
	})
}

// RemoveDirContents deletes all files in this directory,
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

// ReadJSON reads and unmarshalles the JSON content of the file to output.
func (file File) ReadJSON(output interface{}) error {
	reader, err := file.OpenReader()
	if err != nil {
		return err
	}
	defer reader.Close()

	return json.NewDecoder(reader).Decode(output)
}

// WriteJSON mashalles input to JSON and writes it as the file.
// Any indent arguments will be concanated and used as JSON line indentation.
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

// ReadXML reads and unmarshalles the XML content of the file to output.
func (file File) ReadXML(output interface{}) error {
	reader, err := file.OpenReader()
	if err != nil {
		return err
	}
	defer reader.Close()

	return xml.NewDecoder(reader).Decode(output)
}

// WriteXML mashalles input to XML and writes it as the file.
// Any indent arguments will be concanated and used as XML line indentation.
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

// GobEncode reads and gob encodes the file name and content,
// implementing encoding/gob.GobEncoder.
func (file File) GobEncode() ([]byte, error) {
	memFile, err := NewMemFileFrom(file)
	if err != nil {
		return nil, err
	}
	return memFile.GobEncode()
}

// GobDecode decodes a file name and content from gobBytes
// and writes the content to this file, ignoring the decoded name.
// Implements encoding/gob.GobDecoder.
func (file File) GobDecode(gobBytes []byte) error {
	var memFile MemFile
	err := memFile.GobDecode(gobBytes)
	if err != nil {
		return err
	}
	return file.WriteAll(memFile.FileData)
}
