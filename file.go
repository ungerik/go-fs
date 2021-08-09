package fs

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
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
	return fmt.Sprintf("%q (%s)", file.Path(), file.FileSystem().Name())
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

// AbsPath returns the absolute path of the file
// depending on the file system.
func (file File) AbsPath() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.AbsPath(path)
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
// Example:
//   File("image.png").Ext() == ".png"
//   File("dir.with.ext/file").Ext() == ""
//   File("dir.with.ext/file.ext").Ext() == ".ext"
func (file File) Ext() string {
	return fsimpl.Ext(string(file), file.FileSystem().Separator())
}

// ExtLower returns the lower case extension of file name including the point, or an empty string.
// Example: File("Image.PNG").ExtLower() == ".png"
func (file File) ExtLower() string {
	return strings.ToLower(file.Ext())
}

// TrimExt returns a File with a path where the extension is removed.
// Note that this does not rename an actual existing file.
func (file File) TrimExt() File {
	return File(fsimpl.TrimExt(string(file), file.FileSystem().Separator()))
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

// Stat returns a io/fs.FileInfo describing the File.
func (file File) Stat() (fs.FileInfo, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Stat(path)
}

// Info returns FileInfo. The FileInfo.ContentHash field is optional.
func (file File) Info() FileInfo {
	fileSystem, path := file.ParseRawURI()
	info, err := fileSystem.Stat(path)
	if err != nil {
		_, name := fileSystem.DirAndName(path)
		return NewNonExistingFileInfo(name)
	}
	return NewFileInfo(info, fileSystem.IsHidden(path))
}

// InfoWithContentHash returns a FileInfo, but in contrast to Stat
// it always fills the ContentHash field.
func (file File) InfoWithContentHash() (FileInfo, error) {
	return file.InfoWithContentHashContext(context.Background())
}

// InfoWithContentHashContext returns a FileInfo, but in contrast to Stat
// it always fills the ContentHash field.
func (file File) InfoWithContentHashContext(ctx context.Context) (FileInfo, error) {
	if file == "" {
		return FileInfo{}, ErrEmptyPath
	}
	info := file.Info()
	if !info.IsDir && info.ContentHash == "" {
		reader, err := file.OpenReader()
		if err != nil {
			return FileInfo{}, err
		}
		defer reader.Close()
		info.ContentHash, err = fsimpl.ContentHash(ctx, reader)
		if err != nil {
			return FileInfo{}, err
		}
	}
	return info, nil
}

// Exists returns a file or directory with the path of File exists.
func (file File) Exists() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Exists(path)
}

// CheckExists return an ErrDoesNotExist error
// if the file does not exist or ErrEmptyPath
// if the file path is empty.
func (file File) CheckExists() error {
	if file == "" {
		return ErrEmptyPath
	}
	if !file.Exists() {
		return NewErrDoesNotExist(file)
	}
	return nil
}

// IsDir returns a directory with the path of File exists.
func (file File) IsDir() bool {
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.IsDir()
}

// CheckIsDir return an ErrDoesNotExist error
// if the file does not exist, ErrEmptyPath
// if the file path is empty, or ErrIsNotDirectory
// if a file exists, but is not a directory,
// or nil if the file is a directory.
func (file File) CheckIsDir() error {
	stat, err := file.Stat()
	switch {
	case err != nil:
		return err
	case stat.IsDir():
		return nil
	default:
		return NewErrIsNotDirectory(file)
	}
}

// HasAbsPath returns wether the file has an absolute
// path depending on the file system.
func (file File) HasAbsPath() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsAbsPath(path)
}

// WithAbsPath returns the file with an absolute
// path depending on the file system.
func (file File) WithAbsPath() File {
	fileSystem, path := file.ParseRawURI()
	uri := fileSystem.Prefix() + fileSystem.AbsPath(path)
	return File(strings.TrimPrefix(uri, LocalPrefix))
}

// IsRegular reports if this is a regular file.
func (file File) IsRegular() bool {
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode().IsRegular()
}

// IsEmpty returns true in case of a zero size file,
// an empty directory, or a non existing file.
func (file File) IsEmpty() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsEmpty(path)
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
	stat, err := file.Stat()
	if err != nil {
		return 0
	}
	return stat.Size()
}

// ContentHash returns a Dropbox compatible content hash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
// See https://www.dropbox.com/developers/reference/content-hash
func (file File) ContentHash() (string, error) {
	return file.ContentHashContext(context.Background())
}

// ContentHashContext returns a Dropbox compatible content hash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
// See https://www.dropbox.com/developers/reference/content-hash
func (file File) ContentHashContext(ctx context.Context) (string, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	info := file.Info()
	if info.IsDir || info.ContentHash != "" {
		return info.ContentHash, nil
	}
	reader, err := file.OpenReader()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return fsimpl.ContentHash(ctx, reader)
}

func (file File) ModTime() time.Time {
	stat, err := file.Stat()
	if err != nil {
		return time.Time{}
	}
	return stat.ModTime()
}

func (file File) Permissions() Permissions {
	stat, err := file.Stat()
	if err != nil {
		return 0
	}
	return Permissions(stat.Mode().Perm())
}

func (file File) SetPermissions(perm Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetPermissions(path, perm)
}

// ListDir lists files in a directory.
// If listDirs is false then directories will not be listed.
// The optional patterns are used to filter entry names using the MatchAnyPattern method.
// Returning an error from onFile stops the listing
// with the error also getting returned by ListDir.
// An ErrIsNotDirectory error will be returned if called on a File that is not a directory.
func (file File) ListDir(listDirs bool, onFile func(File) error, patterns ...string) error {
	return file.ListDirEntries(
		listDirs,
		func(entry DirEntry) error {
			return onFile(file.Join(entry.Name()))
		},
		patterns...,
	)
}

func (file File) ListDirSorted(listDirs bool, patterns ...string) ([]File, error) {
	var files []File
	err := file.ListDir(
		listDirs,
		func(f File) error {
			files = append(files, f)
			return nil
		},
		patterns...,
	)
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	return files, err
}

func (file File) ListDirRecursive(listDirs bool, onFile func(File) error, patterns ...string) error {
	return file.ListDirEntriesRecursive(
		listDirs,
		func(dir File, entry DirEntry) error {
			return onFile(dir.Join(entry.Name()))
		},
		patterns...,
	)
}

// ListDir lists files in a directory as DirEntry.
// If listDirs is false then directories will not be listed.
// The optional patterns are used to filter entry names using the MatchAnyPattern method.
// Returning an error from onDirEntry stops the listing
// with the error also getting returned by ListDirEntries.
// An ErrIsNotDirectory error will be returned if called on a File that is not a directory.
func (file File) ListDirEntries(listDirs bool, onDirEntry func(DirEntry) error, patterns ...string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	// First try if fileSystem supports ListDir directly
	err := fileSystem.ListDir(path, listDirs, patterns, onDirEntry)
	if err == nil || !errors.Is(err, ErrNotSupported) {
		return err
	}
	// Use ListDirRecursive when the fileSystem does not support ListDir
	return fileSystem.ListDirRecursive(
		path,
		listDirs,
		patterns,
		func(dir string, entry DirEntry) error {
			if dir != path {
				return nil
			}
			return onDirEntry(entry)
		},
	)
}

func (file File) ListDirEntriesSorted(listDirs bool, patterns ...string) ([]DirEntry, error) {
	var entries []DirEntry
	err := file.ListDirEntries(
		listDirs,
		func(entry DirEntry) error {
			entries = append(entries, entry)
			return nil
		},
		patterns...,
	)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

// ListDirEntriesRecursive calls onDirEntry for entries in a directory and recursive sub-directories.
// If listDirs is true then directories will be reported by onDirEntry.
// This does not influence the recursive nature of the method.
// The optional patterns are used to filter entry names using the MatchAnyPattern method.
// The pattern filtering is only applied to the entries reported by onDirEntry,
// not to the recursing into sub-directories.
// Returning an error from onDirEntry stops the listing
// with the error also getting returned by ListDir.
// In case of a directory entry onDirEntry can return SkipDir to prevent
// recursion into that directory without stopping the listing and returning an error.
func (file File) ListDirEntriesRecursive(listDirs bool, onDirEntry func(dir File, entry DirEntry) error, patterns ...string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	// First try if fileSystem supports ListDirRecursive directly
	err := fileSystem.ListDirRecursive(
		path,
		listDirs,
		patterns,
		func(dir string, entry DirEntry) error {
			return onDirEntry(fileSystem.JoinCleanFile(dir), entry)
		},
	)
	if err == nil || !errors.Is(err, ErrNotSupported) {
		return err
	}
	// Use ListDir when the fileSystem does not support ListDirRecursive
	return fileSystem.ListDir(path, true, patterns, func(entry DirEntry) error {
		isDir := entry.IsDir()
		if !isDir || listDirs {
			err := onDirEntry(file, entry)
			if err != nil {
				if isDir && errors.Is(err, SkipDir) {
					return nil
				}
				return err
			}
		}
		if !isDir {
			return nil
		}
		dir := file.Join(entry.Name())
		return dir.ListDirEntriesRecursive(listDirs, onDirEntry, patterns...)
	})
}

func (file File) User() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.User(path)
}

func (file File) SetUser(user string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetUser(path, user)
}

func (file File) Group() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Group(path)
}

func (file File) SetGroup(group string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.SetGroup(path, group)
}

func (file File) Touch(perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Touch(path, perm)
}

func (file File) MakeDir(perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	if file.IsDir() {
		return nil
	}

	fileSystem, path := file.ParseRawURI()
	return fileSystem.MakeDir(path, perm)
}

// MakeAllDirs creates all directories up to this one,
// does not return an error if the directories already exist
func (file File) MakeAllDirs(perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	if info, err := file.Stat(); err == nil {
		// File exists
		if !info.IsDir() {
			return NewErrIsNotDirectory(file)
		}
		return nil
	}

	dir, name := file.DirAndName()
	if name != "" {
		// if name != "" then dir is not the root
		// so we can attempt to make the dir
		err := dir.MakeAllDirs(perm...)
		if err != nil {
			return err
		}
	}
	return file.MakeDir(perm...)
}

// ReadAll reads and returns all bytes of the file
func (file File) ReadAll() (data []byte, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
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
	if file == "" {
		return 0, ErrEmptyPath
	}
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
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.WriteAll(path, data, perm)
}

func (file File) WriteAllString(str string, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	return file.WriteAll([]byte(str), perm...)
}

func (file File) Append(data []byte, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Append(path, data, perm)
}

func (file File) AppendString(str string, perm ...Permissions) error {
	return file.Append([]byte(str), perm...)
}

// OpenReader opens the file and returns a os/fs.File that has be closed after reading
func (file File) OpenReader() (fs.File, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReader(path)
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// If the FileSystem implementation doesn't support ReadSeekCloser,
// then the complete file is read into memory and wrapped with a ReadSeekCloser.
// Warning: this can use up a lot of memory for big files.
func (file File) OpenReadSeeker() (ReadSeekCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	readCloser, err := fileSystem.OpenReader(path)
	if err != nil {
		return nil, err
	}
	if r, ok := readCloser.(ReadSeekCloser); ok {
		return r, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	defer readCloser.Close()
	return fsimpl.NewReadonlyFileBufferReadAll(readCloser, info)
}

func (file File) OpenWriter(perm ...Permissions) (io.WriteCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenWriter(path, perm)
}

func (file File) OpenAppendWriter(perm ...Permissions) (io.WriteCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenAppendWriter(path, perm)
}

func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReadWriter(path, perm)
}

func (file File) Watch() (<-chan WatchEvent, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Watch(path)
}

func (file File) Truncate(size int64) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Truncate(path, size)
}

// Rename changes the name of a file where newName is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directdory.
func (file File) Rename(newName string) (renamedFile File, err error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	err = fileSystem.Rename(path, newName)
	if err != nil {
		return "", err
	}
	return file.Dir().Join(newName), nil
}

// Renamef changes the name of a file where fmt.Sprintf(newNameFormat, args...)
// is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directdory.
func (file File) Renamef(newNameFormat string, args ...interface{}) (renamedFile File, err error) {
	return file.Rename(fmt.Sprintf(newNameFormat, args...))
}

// MoveTo moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
func (file File) MoveTo(destination File) error {
	return Move(file, destination)
}

// Remove deletes the file.
func (file File) Remove() error {
	if file == "" {
		return ErrEmptyPath
	}
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

// RemoveRecursiveContext deletes the file or if it's a directory
// the complete recursive directory tree.
func (file File) RemoveRecursiveContext(ctx context.Context) error {
	if file.IsDir() {
		err := file.RemoveDirContentsRecursiveContext(ctx)
		if err != nil {
			return err
		}
	}
	return file.Remove()
}

// RemoveDirContentsRecursive deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursive() error {
	return file.ListDir(
		true,
		func(f File) error {
			// Ignore files that have been deleted,
			// after all we wanted to get rid of the in the first place,
			// so this is not an error for us
			return ReplaceErrDoesNotExist(f.RemoveRecursive(), nil)
		},
	)
}

// RemoveDirContentsRecursiveContext deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursiveContext(ctx context.Context) error {
	return file.ListDir(
		true,
		func(f File) error {
			// Ignore files that have been deleted,
			// after all we wanted to get rid of the in the first place,
			// so this is not an error for us
			return ReplaceErrDoesNotExist(f.RemoveRecursiveContext(ctx), nil)
		},
	)
}

// RemoveDirContents deletes all files in this directory,
// or if given all files with patterns from the this directory.
func (file File) RemoveDirContents(patterns ...string) error {
	return file.ListDir(
		true,
		func(f File) error {
			// Ignore files that have been deleted,
			// after all we wanted to get rid of the in the first place,
			// so this is not an error for us
			return ReplaceErrDoesNotExist(f.Remove(), nil)
		},
		patterns...,
	)
}

// RemoveDirContentsContext deletes all files in this directory,
// or if given all files with patterns from the this directory.
func (file File) RemoveDirContentsContext(ctx context.Context, patterns ...string) error {
	return file.ListDir(
		true,
		func(f File) error {
			// Ignore files that have been deleted,
			// after all we wanted to get rid of the in the first place,
			// so this is not an error for us
			return ReplaceErrDoesNotExist(f.Remove(), nil)
		},
		patterns...,
	)
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
	if file == "" {
		return ErrEmptyPath
	}
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
	if file == "" {
		return ErrEmptyPath
	}
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
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileName := file.Name()
	fileData, err := file.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("File.GobEncode: error reading file data: %w", err)
	}
	buf := bytes.NewBuffer(make([]byte, 0, 16+len(fileName)+len(fileData)))
	enc := gob.NewEncoder(buf)
	err = enc.Encode(fileName)
	if err != nil {
		return nil, fmt.Errorf("File.GobEncode: error encoding file name: %w", err)
	}
	err = enc.Encode(fileData)
	if err != nil {
		return nil, fmt.Errorf("File.GobEncode: error encoding file data: %w", err)
	}
	return buf.Bytes(), nil

}

// GobDecode decodes a file name and content from gobBytes
// and writes the content to this file ignoring the decoded name.
// Implements encoding/gob.GobDecoder.
func (file File) GobDecode(gobBytes []byte) error {
	if file == "" {
		return ErrEmptyPath
	}
	var (
		fileName string
		fileData []byte
	)
	dec := gob.NewDecoder(bytes.NewReader(gobBytes))
	err := dec.Decode(&fileName)
	if err != nil {
		return fmt.Errorf("File.GobDecode: error decoding file name: %w", err)
	}
	err = dec.Decode(&fileData)
	if err != nil {
		return fmt.Errorf("File.GobDecode: error decoding file data: %w", err)
	}
	err = file.WriteAll(fileData)
	if err != nil {
		return fmt.Errorf("File.GobDecode: error writing file data: %w", err)
	}
	return nil
}

// HTTPFileSystem returns a http.FileSystem with the file as root.
func (file File) HTTPFileSystem() http.FileSystem {
	return httpFileSystem{root: file}
}

// AsFS wraps the file as a FileFS that implements
// the FS interfaces of the os/fs package for a File.
//
// FileFS implements the following interfaces:
//   os/fs.FS
//   os/fs.SubFS
//   os/fs.StatFS
//   os/fs.GlobFS
//   os/fs.ReadDirFS
//   os/fs.ReadFileFS
func (file File) AsFS() FileFS {
	return FileFS{file}
}

// AsDirEntry wraps the file as DirEntry (identical to os/fs.DirEntry).
func (file File) AsDirEntry() DirEntry {
	return FileDirEntry{file}
}
