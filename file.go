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
	iofs "io/fs"
	"iter"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ FileReader     = File("")
	_ fmt.Stringer   = File("")
	_ gob.GobEncoder = File("")
	_ gob.GobDecoder = File("")
)

// InvalidFile is a file with an empty path and thus invalid.
const InvalidFile = File("")

// File is a local file system path or a complete URI.
// It is a string underneath, so string literals can be passed everywhere a File is expected.
// Marshalling functions that use reflection will also work out of the box
// when they detect that File is of kind reflect.String.
// File implements FileReader.
type File string

// FilesFromStrings returns Files for the given fileURIs.
func FilesFromStrings(fileURIs []string) []File {
	if len(fileURIs) == 0 {
		return nil
	}
	files := make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = File(fileURIs[i])
	}
	return files
}

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

// String returns information about the File and its FileSystem.
// String implements the fmt.Stringer interface.
func (file File) String() string {
	return fmt.Sprintf("%s (%s)", string(file), file.FileSystem().Name())
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
	dirPath, name := fileSystem.SplitDirAndName(path)
	return fileSystem.JoinCleanFile(dirPath), name
}

// VolumeName returns the name of the volume at the beginning of the file path,
// or an empty string if the path has no volume.
// A volume is for example "C:" on Windows
func (file File) VolumeName() string {
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(VolumeNameFileSystem); ok {
		return fs.VolumeName(path)
	}
	return ""
}

// Ext returns the extension of file name including the point, or an empty string.
//
// Example:
//
//	File("image.png").Ext() == ".png"
//	File("dir.with.ext/file").Ext() == ""
//	File("dir.with.ext/file.ext").Ext() == ".ext"
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
func (file File) Joinf(format string, args ...any) File {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.JoinCleanFile(path, fmt.Sprintf(format, args...))
}

// IsReadable returns if the file exists and is readable.
func (file File) IsReadable() bool {
	if file == "" {
		return false
	}
	fileSystem, filePath := file.ParseRawURI()
	if readFS, _ := fileSystem.ReadableWritable(); !readFS {
		return false
	}
	info, err := fileSystem.Stat(filePath)
	if err != nil {
		return false
	}
	return (info.Mode().IsDir() || info.Mode().IsRegular()) && (info.Mode()&0400 != 0)
}

// IsWriteable returns if the file exists and is writeable
// or in case it doesn't exist,
// if the parent directory exists and is writeable.
func (file File) IsWriteable() bool {
	if file == "" {
		return false
	}
	fileSystem, pathFile := file.ParseRawURI()
	if _, writeFS := fileSystem.ReadableWritable(); !writeFS {
		return false
	}
	info, err := fileSystem.Stat(pathFile)
	if err == nil {
		return info.Mode().IsRegular() && (info.Mode()&0200 != 0)
	}
	// File does not exist, check if parent directory is writeable
	parentDir, _ := fileSystem.SplitDirAndName(pathFile)
	info, err = fileSystem.Stat(parentDir)
	if err != nil {
		return false // Parent directory does not exist
	}
	return info.Mode().IsDir() && (info.Mode()&0200 != 0)
}

// Stat returns a standard library io/fs.FileInfo describing the file.
func (file File) Stat() (iofs.FileInfo, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.Stat(path)
}

// Info returns FileInfo.
//
// Use File.Stat to get a standard library io/fs.FileInfo.
func (file File) Info() *FileInfo {
	fileSystem, path := file.ParseRawURI()
	info, err := fileSystem.Stat(path)
	if err != nil {
		return NewNonExistingFileInfo(file)
	}
	return NewFileInfo(file, info, fileSystem.IsHidden(path))
}

// InfoWithContentHash returns a FileInfo, but in contrast to Stat
// it always fills the ContentHash field.
// func (file File) InfoWithContentHash() (FileInfo, error) {
// 	return file.InfoWithContentHashContext(context.Background())
// }

// InfoWithContentHashContext returns a FileInfo, but in contrast to Stat
// it always fills the ContentHash field.
// func (file File) InfoWithContentHashContext(ctx context.Context) (FileInfo, error) {
// 	if file == "" {
// 		return FileInfo{}, ErrEmptyPath
// 	}
// 	info := file.Info()
// 	if !info.IsDir && info.ContentHash == "" {
// 		reader, err := file.OpenReader()
// 		if err != nil {
// 			return FileInfo{}, err
// 		}
// 		defer reader.Close()
// 		info.ContentHash, err = DefaultContentHash(ctx, reader)
// 		if err != nil {
// 			return FileInfo{}, err
// 		}
// 	}
// 	return info, nil
// }

// Exists returns a file or directory with the path of File exists.
func (file File) Exists() bool {
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(ExistsFileSystem); ok {
		return fs.Exists(path)
	}
	_, err := fileSystem.Stat(path)
	return err == nil
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

// AbsPath returns the absolute path of the file
// depending on the file system.
func (file File) AbsPath() string {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.AbsPath(path)
}

// HasAbsPath returns wether the file has an absolute
// path depending on the file system.
func (file File) HasAbsPath() bool {
	fileSystem, path := file.ParseRawURI()
	return fileSystem.IsAbsPath(path)
}

// ToAbsPath returns the file with an absolute
// path depending on the file system.
func (file File) ToAbsPath() File {
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
	stat, err := file.Stat()
	if err != nil {
		return 0
	}
	return stat.Size()
}

// ContentHash returns the DefaultContentHash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
func (file File) ContentHash() (string, error) {
	return file.ContentHashContext(context.Background())
}

// ContentHashContext returns the DefaultContentHash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
func (file File) ContentHashContext(ctx context.Context) (string, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	if file.IsDir() {
		return "", nil
	}
	reader, err := file.OpenReader()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return DefaultContentHash(ctx, reader)
}

func (file File) Modified() time.Time {
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
	return PermissionsFromStdFileInfo(stat)
}

func (file File) SetPermissions(perm Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(PermissionsFileSystem); ok {
		return fs.SetPermissions(path, perm)
	}
	return NewErrUnsupported(fileSystem, "SetPermissions")
}

// ListDir calls the passed callback function for every file and directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDir(callback func(File) error, patterns ...string) error {
	return file.ListDirContext(context.Background(), callback, patterns...)
}

// ListDirContext calls the passed callback function for every file and directory in the directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// Canceling the context or returning an error from the callback
// will stop the listing and return the context or callback error.
func (file File) ListDirContext(ctx context.Context, callback func(File) error, patterns ...string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfo(ctx, path, FileInfoToFileCallback(callback), patterns)
}

// ListDirIter returns an iterator that yields every file and directory in the directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
func (file File) ListDirIter(patterns ...string) iter.Seq2[File, error] {
	return file.ListDirIterContext(context.Background(), patterns...)
}

// ListDirIterContext returns an iterator that yields every file and directory in the directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
// Canceling the context will stop the iteration and yield the context error.
func (file File) ListDirIterContext(ctx context.Context, patterns ...string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		var cancel SentinelError
		err := file.ListDirContext(ctx,
			func(listedFile File) error {
				if !yield(listedFile, nil) {
					return cancel
				}
				return nil
			},
			patterns...,
		)
		if err != nil && !errors.Is(err, cancel) {
			yield(InvalidFile, err)
		}
	}
}

// MustGlob yields files and wildcard substituting path segments
// matching a path pattern.
//
// The pattern is appended to the current working directory
// if it is not an absolute path.
//
// The yielded path segments are the strings necessary
// to substitute all wildcard containing path segments
// in the pattern to form a valid path for a yielded file.
// This includes the name of the yielded file itself
// if the pattern contains wildcards for the last segment.
// Non wildcard segments are not included.
//
// The syntax of patterns is the same as in [path.Match].
// It always uses slash '/' as path segment separator
// independently of the file's file system.
//
// MustGlob ignores file system errors such as I/O errors reading directories.
// The only possible panic is in case of a malformed pattern.
func MustGlob(pattern string) iter.Seq2[File, []string] {
	globIter, err := Glob(pattern)
	if err != nil {
		panic(err)
	}
	return globIter
}

// MustGlob yields files and wildcard substituting path segments
// matching a path pattern.
//
// The yielded path segments are the strings necessary
// to substitute all wildcard containing path segments
// in the pattern to form a valid path for a yielded file.
// This includes the name of the yielded file itself
// if the pattern contains wildcards for the last segment.
// Non wildcard segments are not included.
//
// The syntax of patterns is the same as in [path.Match].
// It always uses slash '/' as path segment separator
// independently of the file's file system.
//
// A pattern ending with a slash '/' will match only directories.
//
// MustGlob ignores file system errors such as I/O errors reading directories.
// The only possible panic is in case of a malformed pattern.
func (file File) MustGlob(pattern string) iter.Seq2[File, []string] {
	globIter, err := file.Glob(pattern)
	if err != nil {
		panic(err)
	}
	return globIter
}

// Glob yields files and wildcard substituting path segments
// matching a path pattern.
//
// The pattern is appended to the current working directory
// if it is not an absolute path.
//
// The yielded path segments are the strings necessary
// to substitute all wildcard containing path segments
// in the pattern to form a valid path for a yielded file.
// This includes the name of the yielded file itself
// if the pattern contains wildcards for the last segment.
// Non wildcard segments are not included.
//
// The syntax of patterns is the same as in [path.Match].
// It always uses slash '/' as path segment separator
// independently of the file's file system.
//
// A pattern ending with a slash '/' will match only directories.
//
// Glob ignores file system errors such as I/O errors reading directories.
// The only possible returned error is [path.ErrBadPattern],
// reporting that the pattern is malformed.
func Glob(pattern string) (iter.Seq2[File, []string], error) {
	// Find the first wildcard
	i := strings.IndexAny(pattern, `*?[\`)
	if i == -1 {
		// No wildcard in pattern, yield the pattern as File
		return File(path.Clean(pattern)).Glob("")
	}
	// Find the last path separator before the first wildcard
	i = strings.LastIndexByte(pattern[:i], '/')
	if i == -1 {
		// No path separator before the first wildcard
		// means that the pattern is relative to the current directory
		return CurrentWorkingDir().Glob(pattern)
	}
	// Split pattern into base directory and glob pattern
	return File(pattern[:i+1]).Glob(pattern[i+1:])
}

// Glob yields files and wildcard substituting path segments
// matching a path pattern.
//
// The yielded path segments are the strings necessary
// to substitute all wildcard containing path segments
// in the pattern to form a valid path for a yielded file.
// This includes the name of the yielded file itself
// if the pattern contains wildcards for the last segment.
// Non wildcard segments are not included.
//
// The syntax of patterns is the same as in [path.Match].
// It always uses slash '/' as path segment separator
// independently of the file's file system.
//
// A pattern ending with a slash '/' will match only directories.
//
// Glob ignores file system errors such as I/O errors reading directories.
// The only possible returned error is [path.ErrBadPattern],
// reporting that the pattern is malformed.
func (file File) Glob(pattern string) (iter.Seq2[File, []string], error) {
	onlyDirs := strings.HasSuffix(pattern, "/")
	pattern = strings.Trim(pattern, "/")
	// Check if the pattern is valid
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("%w: %s", err, pattern)
	}
	pSegments := strings.Split(pattern, "/")
	pSegments = slices.DeleteFunc(pSegments, func(s string) bool {
		return s == "" || s == "."
	})
	if slices.Contains(pSegments, "..") {
		return nil, fmt.Errorf("%w, must not contain '..': %s", path.ErrBadPattern, pattern)
	}
	switch i := slices.IndexFunc(pSegments, containsWildcard); {
	case i < 0:
		// No wildcard in pattern, join path and yield file if it exists
		file = file.Join(pSegments...)
		pSegments = nil
	case i > 0:
		// Join non wildcard path before first wildcard
		file = file.Join(pSegments[:i]...)
		pSegments = pSegments[i:]
	}
	file.CheckIsDir()
	return file.glob(onlyDirs, pSegments, nil), nil
}

func containsWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, `*?[\`)
}

func (file File) glob(onlyDirs bool, segments, values []string) iter.Seq2[File, []string] {
	return func(yield func(File, []string) bool) {
		switch len(segments) {
		case 0:
			// No more segments, yield the file itself
			if onlyDirs && file.IsDir() || !onlyDirs && file.Exists() {
				yield(file, values)
			}

		case 1:
			// Last segment, yield all matching files
			if pattern := segments[0]; containsWildcard(pattern) {
				// Wildcard in last segment, list directory with segment as pattern
				for f, err := range file.ListDirIter(pattern) {
					// If file is not a directory then ErrIsNotDirectory is expected
					if err != nil {
						return
					}
					if onlyDirs && !f.IsDir() || !onlyDirs && !f.Exists() {
						continue
					}
					if !yield(f, append(slices.Clone(values), f.Name())) {
						return
					}
				}
			} else {
				// No wildcard in last segment, join path and yield file if it exists
				f := file.Join(pattern)
				if onlyDirs && f.IsDir() || !onlyDirs && f.Exists() {
					yield(f, values)
				}
			}

		default:
			if pattern := segments[0]; containsWildcard(pattern) {
				// Wildcard in segment, list directory with segment as pattern
				for matchedFile, err := range file.ListDirIter(pattern) {
					// If file is not a directory then ErrIsNotDirectory is expected
					if err != nil {
						return
					}
					for f, v := range matchedFile.glob(onlyDirs, segments[1:], append(slices.Clone(values), matchedFile.Name())) {
						if !yield(f, v) {
							return
						}
					}
				}
			} else {
				// No wildcard in segment, join path and recurse
				for f, v := range file.Join(segments[0]).glob(onlyDirs, segments[1:], values) {
					if !yield(f, v) {
						return
					}
				}
			}
		}
	}
}

// ListDirInfo calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfo(callback func(*FileInfo) error, patterns ...string) error {
	return file.ListDirInfoContext(context.Background(), callback, patterns...)
}

// ListDirInfoContext calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfoContext(ctx context.Context, callback func(*FileInfo) error, patterns ...string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.ListDirInfo(ctx, path, callback, patterns)
}

// ListDirRecursive returns only files.
// patterns are only applied to files, not to directories
func (file File) ListDirRecursive(callback func(File) error, patterns ...string) error {
	return file.ListDirInfoRecursiveContext(context.Background(), FileInfoToFileCallback(callback), patterns...)
}

// ListDirRecursiveContext returns only files.
// patterns are only applied to files, not to directories
func (file File) ListDirRecursiveContext(ctx context.Context, callback func(File) error, patterns ...string) error {
	return file.ListDirInfoRecursiveContext(ctx, FileInfoToFileCallback(callback), patterns...)
}

// ListDirRecursiveIter returns an iterator that yields every file and directory
// recursively in the directory and sub-directories.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
func (file File) ListDirRecursiveIter(patterns ...string) iter.Seq2[File, error] {
	return file.ListDirRecursiveIterContext(context.Background(), patterns...)
}

// ListDirRecursiveIterContext returns an iterator that yields every file and directory
// recursively in the directory and sub-directories.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
// Canceling the context will stop the iteration and yield the context error.
func (file File) ListDirRecursiveIterContext(ctx context.Context, patterns ...string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		const cancel SentinelError = "cancel"
		err := file.ListDirRecursiveContext(ctx,
			func(listedFile File) error {
				if !yield(listedFile, nil) {
					return cancel
				}
				return nil
			},
			patterns...,
		)
		if err != nil && !errors.Is(err, cancel) {
			yield(InvalidFile, err)
		}
	}
}

// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
// recursing into all sub-directories.
// If any patterns are passed, then only files (not directories) with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfoRecursive(callback func(*FileInfo) error, patterns ...string) error {
	return file.ListDirInfoRecursiveContext(context.Background(), callback, patterns...)
}

// ListDirInfoRecursiveContext calls the passed callback function for every file
// (not directory) in dirPath recursing into all sub-directories.
// If any patterns are passed, then only files (not directories) with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfoRecursiveContext(ctx context.Context, callback func(*FileInfo) error, patterns ...string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(ListDirRecursiveFileSystem); ok {
		return fs.ListDirInfoRecursive(ctx, path, callback, patterns)
	}
	return fileSystem.ListDirInfo(ctx, path,
		func(info *FileInfo) error {
			if info.IsDir {
				// Not returning directories, but recursing into them
				err := info.File.ListDirInfoRecursiveContext(ctx, callback, patterns...)
				// Don't mind files that have been deleted while iterating
				return RemoveErrDoesNotExist(err)
			}
			match, err := fileSystem.MatchAnyPattern(info.Name, patterns)
			if !match || err != nil {
				return err
			}
			return callback(info)
		},
		nil, // No patterns
	)
}

// ListDirMax returns at most max files and directories in dirPath.
// A max value of -1 returns all files.
// If any patterns are passed, then only files or directories with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirMax(max int, patterns ...string) (files []File, err error) {
	return file.ListDirMaxContext(context.Background(), max, patterns...)
}

// ListDirMaxContext returns at most max files and directories in dirPath.
// A max value of -1 returns all files.
// If any patterns are passed, then only files or directories with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirMaxContext(ctx context.Context, max int, patterns ...string) (files []File, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	if max == 0 {
		return nil, nil
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(ListDirMaxFileSystem); ok {
		return fs.ListDirMax(ctx, path, max, patterns)
	}
	done := errors.New("done") // used as an internal flag, won't be returned
	err = fileSystem.ListDirInfo(ctx, path, func(info *FileInfo) error {
		if max >= 0 && len(files) >= max {
			return done
		}
		if files == nil {
			// Reserve space for files
			capacity := max
			if capacity < 0 {
				capacity = 64
			}
			files = make([]File, 0, capacity)
		}
		files = append(files, info.File)
		return nil
	}, patterns)
	if err != nil && !errors.Is(err, done) {
		return nil, err
	}
	return files, nil
}

func (file File) ListDirRecursiveMax(max int, patterns ...string) (files []File, err error) {
	return file.ListDirRecursiveMaxContext(context.Background(), max, patterns...)
}

func (file File) ListDirRecursiveMaxContext(ctx context.Context, max int, patterns ...string) (files []File, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	return listDirMaxImpl(ctx, max, func(ctx context.Context, callback func(File) error) error {
		return file.ListDirRecursiveContext(ctx, callback, patterns...)
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

func (file File) User() (string, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(UserFileSystem); ok {
		return fs.User(path)
	}
	return "", NewErrUnsupported(fileSystem, "User")
}

func (file File) SetUser(user string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(UserFileSystem); ok {
		return fs.SetUser(path, user)
	}
	return NewErrUnsupported(fileSystem, "SetUser")
}

func (file File) Group() (string, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(GroupFileSystem); ok {
		return fs.Group(path)
	}
	return "", NewErrUnsupported(fileSystem, "Group")
}

func (file File) SetGroup(group string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(GroupFileSystem); ok {
		return fs.SetGroup(path, group)
	}
	return NewErrUnsupported(fileSystem, "SetGroup")
}

func (file File) Touch(perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(TouchFileSystem); ok {
		return fs.Touch(path, perm)
	}
	w, err := file.OpenWriter(perm...)
	if err != nil {
		return err
	}
	return w.Close()
}

// MakeDir creates a directory if it does not exist yet.
// No error is returned if the directory already exists.
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
	if info, e := file.Stat(); e == nil {
		// File exists
		if !info.IsDir() {
			return NewErrIsNotDirectory(file)
		}
		return nil // File is already a directory
	}

	dir, name := file.DirAndName()
	if name != "" {
		// if name != "" then dir is not the root
		// so we can attempt to make the dir
		// by recursively calling MakeAllDirs
		err := dir.MakeAllDirs(perm...)
		if err != nil {
			return err
		}
	}
	return file.MakeDir(perm...)
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
	var writer WriteCloser
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

// OpenReader opens the file and returns a io/fs.File that has to be closed after reading
func (file File) OpenReader() (ReadCloser, error) {
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

func (file File) OpenWriter(perm ...Permissions) (WriteCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenWriter(path, perm)
}

func (file File) OpenAppendWriter(perm ...Permissions) (WriteCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(AppendWriterFileSystem); ok {
		return fs.OpenAppendWriter(path, perm)
	}
	// Emulate append writer by reading file into
	// a buffer first and write everything back to
	// the file on closing that buffer.
	current, err := file.ReadAll()
	if err != nil {
		return nil, err
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(current, func() error {
		return file.WriteAll(fileBuffer.Bytes(), perm...)
	})
	return fileBuffer, nil

}

func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	return fileSystem.OpenReadWriter(path, perm)
}

// ReadAll reads and returns all bytes of the file
func (file File) ReadAll() (data []byte, err error) {
	return file.ReadAllContext(context.Background())
}

// ReadAllContext reads and returns all bytes of the file
func (file File) ReadAllContext(ctx context.Context) (data []byte, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(ReadAllFileSystem); ok {
		return fs.ReadAll(ctx, path)
	}
	r, err := fileSystem.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ReadAllContext(ctx, r)
}

// ReadAllContentHash reads and returns all bytes of the file
// together with the DefaultContentHash.
func (file File) ReadAllContentHash(ctx context.Context) (data []byte, hash string, err error) {
	data, err = file.ReadAllContext(ctx)
	if err != nil {
		return nil, "", err
	}
	hash, err = DefaultContentHash(ctx, bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	return data, hash, nil
}

// ReadAllString reads the complete file and returns the content as string.
func (file File) ReadAllString() (string, error) {
	return file.ReadAllStringContext(context.Background())
}

// ReadAllStringContext reads the complete file and returns the content as string.
func (file File) ReadAllStringContext(ctx context.Context) (string, error) {
	data, err := file.ReadAllContext(ctx)
	if data == nil || err != nil {
		return "", err
	}
	return string(data), nil
}

func (file File) WriteAll(data []byte, perm ...Permissions) error {
	return file.WriteAllContext(context.Background(), data, perm...)
}

func (file File) WriteAllContext(ctx context.Context, data []byte, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(WriteAllFileSystem); ok {
		return fs.WriteAll(ctx, path, data, perm)
	}
	w, err := fileSystem.OpenReadWriter(path, perm)
	if err != nil {
		return err
	}
	defer w.Close()
	return WriteAllContext(ctx, w, data)
}

func (file File) WriteAllString(str string, perm ...Permissions) error {
	return file.WriteAllStringContext(context.Background(), str, perm...)
}

func (file File) WriteAllStringContext(ctx context.Context, str string, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	return file.WriteAllContext(ctx, []byte(str), perm...)
}

func (file File) Append(ctx context.Context, data []byte, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(AppendFileSystem); ok {
		return fs.Append(ctx, path, data, perm)
	}
	if fs, ok := fileSystem.(AppendWriterFileSystem); ok {
		w, err := fs.OpenAppendWriter(path, perm)
		if err != nil {
			return err
		}
		defer w.Close()
		return WriteAllContext(ctx, w, data)
	}
	// Emulate append by first reading all file
	// content and then writing the file with
	// appended data.
	current, err := file.ReadAllContext(ctx)
	if err != nil {
		return err
	}
	return file.WriteAllContext(ctx, append(current, data...))
}

func (file File) AppendString(ctx context.Context, str string, perm ...Permissions) error {
	return file.Append(ctx, []byte(str), perm...)
}

// Watch a file or directory for changes.
// If the file describes a directory then
// changes directly within it will be reported.
// This does not apply changes in deeper
// recursive sub-directories.
//
// It is valid to watch a file with multiple
// callbacks, calling the returned cancel function
// will cancel a particular watch.
func (file File) Watch(onEvent func(File, Event)) (cancel func() error, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	if onEvent == nil {
		return nil, errors.New("nil callback")
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(WatchFileSystem); ok {
		return fs.Watch(path, onEvent)
	}
	return nil, NewErrUnsupported(fileSystem, "Watch")
}

func (file File) Truncate(newSize int64) error {
	if file == "" {
		return ErrEmptyPath
	}
	if newSize < 0 {
		return fmt.Errorf("negative file size: %d", newSize)
	}
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(TruncateFileSystem); ok {
		return fs.Truncate(path, newSize)
	}
	info, err := fileSystem.Stat(path)
	if err != nil {
		return NewErrDoesNotExist(file)
	}
	if info.IsDir() {
		return NewErrIsDirectory(file)
	}
	if info.Size() == newSize {
		return nil
	}
	if info.Size() < newSize {
		// Append zeros if current file is smaller than newSize
		zeros := make([]byte, newSize-info.Size())
		return file.Append(context.Background(), zeros)
	}
	// Truncate be reading up to newSize and then rewriting the file
	r, err := fileSystem.OpenReader(path)
	if err != nil {
		return err
	}
	data := make([]byte, newSize)
	_, err = io.ReadFull(r, data)
	err = errors.Join(err, r.Close())
	if err != nil {
		return err
	}
	w, err := fileSystem.OpenWriter(path, []Permissions{PermissionsFromStdFileInfo(info)})
	if err != nil {
		return err
	}
	n, err := w.Write(data)
	if err != nil {
		return errors.Join(err, w.Close())
	}
	if int64(n) < newSize {
		return errors.Join(fmt.Errorf("%w from truncating file %s", io.ErrShortWrite, file), w.Close())
	}
	return w.Close()
}

// Rename changes the name of a file where newName is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directdory.
func (file File) Rename(newName string) (renamedFile File, err error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if strings.ContainsAny(newName, fileSystem.Separator()) {
		return "", fmt.Errorf("newName %#v for File.Rename contains path separator %s", newName, fileSystem.Separator())
	}
	switch fs := fileSystem.(type) {
	case RenameFileSystem:
		newPath, err := fs.Rename(path, newName)
		if err != nil {
			return "", err
		}
		return fs.RootDir().Join(newPath), nil
	case MoveFileSystem:
		dir, _ := fs.SplitDirAndName(path)
		newPath := fs.JoinCleanPath(dir, newName)
		err = fs.Move(path, newPath)
		if err != nil {
			return "", err
		}
		return fs.RootDir().Join(newPath), nil
	default:
		renamedFile = file.Dir().Join(newName)
		if file.IsDir() {
			err = renamedFile.MakeDir()
		} else {
			err = CopyFile(context.Background(), file, renamedFile)
		}
		if err != nil {
			return "", err
		}
		return renamedFile, file.Remove()
	}
}

// Renamef changes the name of a file where fmt.Sprintf(newNameFormat, args...)
// is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directdory.
func (file File) Renamef(newNameFormat string, args ...any) (renamedFile File, err error) {
	return file.Rename(fmt.Sprintf(newNameFormat, args...))
}

// MoveTo moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
func (file File) MoveTo(destination File) error {
	return Move(context.Background(), file, destination)
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
	return file.ListDir(func(f File) error {
		err := f.RemoveRecursive()
		// Ignore files that have been deleted,
		// after all we wanted to get rid of the in the first place,
		// so this is not an error for us
		return RemoveErrDoesNotExist(err)
	})
}

// RemoveDirContentsRecursiveContext deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursiveContext(ctx context.Context) error {
	return file.ListDirContext(ctx, func(f File) error {
		err := f.RemoveRecursiveContext(ctx)
		// Ignore files that have been deleted,
		// after all we wanted to get rid of the in the first place,
		// so this is not an error for us
		return RemoveErrDoesNotExist(err)
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
		return RemoveErrDoesNotExist(err)
	}, patterns...)
}

// RemoveDirContentsContext deletes all files in this directory,
// or if given all files with patterns from the this directory.
func (file File) RemoveDirContentsContext(ctx context.Context, patterns ...string) error {
	return file.ListDirContext(ctx, func(f File) error {
		err := f.Remove()
		// Ignore files that have been deleted,
		// after all we wanted to get rid of the in the first place,
		// so this is not an error for us
		return RemoveErrDoesNotExist(err)
	}, patterns...)
}

// ReadJSON reads and unmarshalles the JSON content of the file to output.
//
// Returns a wrapped ErrUnmarshalJSON when the unmarshalling failed.
func (file File) ReadJSON(ctx context.Context, output any) error {
	data, err := file.ReadAllContext(ctx)
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalJSON, err)
	}
	return nil
}

// WriteJSON mashalles input to JSON and writes it as the file.
// Any indent arguments will be concanated and used as JSON line indentation.
//
// Returns a wrapped ErrMarshalJSON when the marshalling failed.
func (file File) WriteJSON(ctx context.Context, input any, indent ...string) (err error) {
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
		return fmt.Errorf("%w because: %w", ErrMarshalJSON, err)
	}
	return file.WriteAllContext(ctx, data)
}

// ReadXML reads and unmarshalles the XML content of the file to output.
//
// Returns a wrapped ErrUnmarshalXML when the unmarshalling failed.
func (file File) ReadXML(ctx context.Context, output any) error {
	data, err := file.ReadAllContext(ctx)
	if err != nil {
		return err
	}
	err = xml.Unmarshal(data, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalXML, err)
	}
	return nil
}

// WriteXML mashalles input to XML and writes it as the file.
// Any indent arguments will be concanated and used as XML line indentation.
//
// Returns a wrapped ErrMarshalXML when the marshalling failed.
func (file File) WriteXML(ctx context.Context, input any, indent ...string) (err error) {
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
		return fmt.Errorf("%w because: %w", ErrMarshalXML, err)
	}
	data = append([]byte(xml.Header), data...)
	return file.WriteAllContext(ctx, data)
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

// StdFS wraps the file as a StdFS struct that
// implements the io/fs.FS interface
// of the standard library for a File.
//
// StdFS implements the following interfaces:
//   - io/fs.FS
//   - io/fs.SubFS
//   - io/fs.StatFS
//   - io/fs.GlobFS
//   - io/fs.ReadDirFS
//   - io/fs.ReadFileFS
func (file File) StdFS() StdFS {
	return StdFS{file}
}

// StdDirEntry wraps the file as a StdDirEntry struct
// that implements the io/fs.DirEntry interface
// from the standard library for a File.
func (file File) StdDirEntry() StdDirEntry {
	return StdDirEntry{file}
}
