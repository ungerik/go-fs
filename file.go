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
	"os"
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

// ParseRawURI returns a FileSystem for the passed URI and the clean path within that file system.
// Returns the local file system if no other file system could be identified.
func (file File) ParseRawURI() (fs FileSystem, fsPath string) {
	return ParseRawURI(string(file))
}

// RawURI returns the string value of File.
func (file File) RawURI() string {
	return string(file)
}

// String implements the fmt.Stringer interface
// by returning the URL of the file.
func (file File) String() string {
	return file.URL()
}

// URL returns the URL of the file.
func (file File) URL() string {
	fileSystem, path := file.ParseRawURI()
	return fsURL(fileSystem, path)
}

// Path returns the cleaned path of the file.
// It may differ from the string value of File
// because it will be cleaned depending on the FileSystem.
func (file File) Path() string {
	_, path := file.ParseRawURI()
	return path
}

// PathWithSlashes returns the cleaned path of the file
// always using the slash '/' as separator.
// It may differ from the string value of File
// because it will be cleaned depending on the FileSystem.
func (file File) PathWithSlashes() string {
	fileSystem, path := file.ParseRawURI()
	if sep := fileSystem.Separator(); sep != "/" {
		path = strings.ReplaceAll(path, sep, "/")
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
	return path
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
// If filePath is the root of the file system, then an empty string will be returned for name.
func (file File) DirAndName() (dir File, name string) {
	fileSystem, path := file.ParseRawURI()
	dirPath, name := fsSplitDirAndName(fileSystem, path)
	return fsJoinCleanFile(fileSystem, dirPath), name
}

// VolumeName returns the name of the volume at the beginning of the file path,
// or an empty string if the path has no volume.
// A volume is for example "C:" on Windows.
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
	return fsJoinCleanFile(fileSystem, pathParts...)
}

// Joinf returns a new File with fmt.Sprintf(format, args...) cleaned and joined to the current File's URI.
// The resulting URI path will be cleaned, removing relative directory names like "..".
func (file File) Joinf(format string, args ...any) File {
	fileSystem, path := file.ParseRawURI()
	return fsJoinCleanFile(fileSystem, path, fmt.Sprintf(format, args...))
}

// IsReadable returns if the file exists and is readable.
//
// It does not return an error by design: any error, including the file not
// existing or not being accessible, results in false.
func (file File) IsReadable() bool {
	if file == "" {
		return false
	}
	fileSystem, filePath := file.ParseRawURI()
	info, err := fsStat(fileSystem, filePath)
	if err != nil {
		return false
	}
	return (info.IsDir || info.IsRegular) && info.Permissions&UserRead != 0
}

// IsWritable returns if the file exists and is writable,
// or in case it doesn't exist,
// if the parent directory exists and is writable.
//
// It does not return an error by design: any error, including the file and
// its parent directory not existing or not being accessible, results in false.
func (file File) IsWritable() bool {
	if file == "" {
		return false
	}
	fileSystem, filePath := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return false
	}
	info, err := fsStat(fileSystem, filePath)
	if err == nil {
		return info.IsRegular && info.Permissions&UserWrite != 0
	}
	// File does not exist, check if parent directory is writable
	parentDir, _ := fsSplitDirAndName(fileSystem, filePath)
	info, err = fsStat(fileSystem, parentDir)
	if err != nil {
		return false // Parent directory does not exist
	}
	return info.IsDir && info.Permissions&UserWrite != 0
}

// Stat returns a standard library io/fs.FileInfo describing the file.
func (file File) Stat() (iofs.FileInfo, error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil {
		return nil, err
	}
	return info.StdFileInfo(), nil
}

// Info returns FileInfo.
//
// Use File.Stat to get a standard library io/fs.FileInfo.
func (file File) Info() *FileInfo {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil {
		return NewNonExistingFileInfo(file)
	}
	return info
}

// Exists returns if a file or directory with the path of File exists.
//
// It does not return an error by design: any error, including the file not
// being accessible, results in false. Use [File.CheckExists] to get an error.
func (file File) Exists() bool {
	fileSystem, path := file.ParseRawURI()
	exists, err := fsExists(fileSystem, path)
	return err == nil && exists
}

// CheckExists returns an ErrDoesNotExist error
// if the file does not exist or ErrEmptyPath
// if the file path is empty.
func (file File) CheckExists() error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	exists, err := fsExists(fileSystem, path)
	if err != nil {
		return err
	}
	if !exists {
		return NewErrDoesNotExist(file)
	}
	return nil
}

// IsDir returns if a directory with the path of File exists.
//
// It does not return an error by design: any error, including the path not
// existing or not being accessible, results in false. Use [File.CheckIsDir]
// to get an error.
func (file File) IsDir() bool {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	return err == nil && info.IsDir
}

// CheckIsDir returns an ErrDoesNotExist error
// if the file does not exist, ErrEmptyPath
// if the file path is empty, or ErrIsNotDirectory
// if a file exists, but is not a directory,
// or nil if the file is a directory.
func (file File) CheckIsDir() error {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	switch {
	case err != nil:
		return err
	case info.IsDir:
		return nil
	default:
		return NewErrIsNotDirectory(file)
	}
}

// AbsPath returns the absolute path of the file
// depending on the file system.
func (file File) AbsPath() string {
	fileSystem, path := file.ParseRawURI()
	return fsAbsPath(fileSystem, path)
}

// HasAbsPath returns whether the file has an absolute
// path depending on the file system.
func (file File) HasAbsPath() bool {
	fileSystem, path := file.ParseRawURI()
	return fsIsAbsPath(fileSystem, path)
}

// ToAbsPath returns the File with an absolute
// path depending on the file system.
func (file File) ToAbsPath() File {
	fileSystem, path := file.ParseRawURI()
	return fsFile(fileSystem, fsAbsPath(fileSystem, path))
}

// RelPathOf returns the path of target relative to file,
// like [filepath.Rel] but interpreted by file's [FileSystem].
//
// file is treated as the base; the result is the path that,
// when joined with file, refers to the same location as target.
// The result is cleaned and may contain ".." segments.
//
// Both files must belong to the same [FileSystem], otherwise an error is returned.
// An error is also returned if target can't be made relative to file
// (for example when one path is absolute and the other is not,
// or when computing the relation would require knowing the current working directory).
func (file File) RelPathOf(target File) (string, error) {
	fileSystem, basePath := file.ParseRawURI()
	targetFileSystem, targetPath := target.ParseRawURI()
	if targetFileSystem != fileSystem {
		return "", fmt.Errorf("file systems do not match: %s and %s", fileSystem, targetFileSystem)
	}
	return fsRelPath(fileSystem, basePath, targetPath)
}

// IsRegular reports if this is a regular file.
//
// It does not return an error by design: any error, including the file not
// existing or not being accessible, results in false.
func (file File) IsRegular() bool {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	return err == nil && info.IsRegular
}

// IsEmptyDir returns if file is an empty directory.
//
// It does not return an error by design: any error, including the directory
// not existing or not being accessible, results in false.
func (file File) IsEmptyDir() bool {
	l, err := file.ListDirMax(1)
	return len(l) == 0 && err == nil
}

// IsHidden returns true if the filename begins with a dot,
// or if on Windows the hidden file attribute is set.
func (file File) IsHidden() bool {
	fileSystem, path := file.ParseRawURI()
	return fsIsHidden(fileSystem, path)
}

// IsSymbolicLink returns if the file is a symbolic link.
// Use [File.CreateSymbolicLink] and [File.ReadSymbolicLink]
// to create and resolve symbolic links.
//
// It does not return an error by design: any error, including the file not
// existing or not being accessible, results in false. File systems without
// support for symbolic links always return false.
func (file File) IsSymbolicLink() bool {
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(SymbolicLinkFileSystem); ok {
		return fs.IsSymbolicLink(path)
	}
	return false
}

// CreateSymbolicLink creates file as a symbolic link pointing to target.
// file is the link to be created; target is what the link points to.
//
// Both files must belong to the same [FileSystem], otherwise an error is returned.
// An error is also returned if the file system does not implement
// [SymbolicLinkFileSystem].
//
// The target path is stored as given. If you want a stable link regardless of
// where it is resolved from, pass an absolute target.
func (file File) CreateSymbolicLink(target File) error {
	if file == "" || target == "" {
		return ErrEmptyPath
	}
	linkFS, linkPath := file.ParseRawURI()
	targetFS, targetPath := target.ParseRawURI()
	if linkFS != targetFS {
		return fmt.Errorf("file systems do not match: %s and %s", linkFS, targetFS)
	}
	if _, err := fsWritable(linkFS); err != nil {
		return err
	}
	symlinkFS, ok := linkFS.(SymbolicLinkFileSystem)
	if !ok {
		return NewErrUnsupported(linkFS, "CreateSymbolicLink")
	}
	return symlinkFS.CreateSymbolicLink(targetPath, linkPath)
}

// ReadSymbolicLink returns the target of the symbolic link at file.
// The returned [File] is the raw link target as stored on disk and may be
// relative to file's directory.
//
// Returns an [ErrUnsupported] error if file's [FileSystem] does not
// implement [SymbolicLinkFileSystem].
func (file File) ReadSymbolicLink() (File, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, linkPath := file.ParseRawURI()
	symlinkFS, ok := fileSystem.(SymbolicLinkFileSystem)
	if !ok {
		return "", NewErrUnsupported(fileSystem, "ReadSymbolicLink")
	}
	targetPath, err := symlinkFS.ReadSymbolicLink(linkPath)
	if err != nil {
		return "", err
	}
	return File(targetPath), nil
}

// Size returns the size of the file or 0 if it does not exist or is a directory.
func (file File) Size() int64 {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil {
		return 0
	}
	return info.Size
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

// Modified returns the modification time of the file,
// or a zero time.Time if the file does not exist.
func (file File) Modified() time.Time {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil {
		return time.Time{}
	}
	return info.Modified
}

// Permissions returns the file permissions,
// or NoPermissions if the file does not exist.
func (file File) Permissions() Permissions {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil {
		return NoPermissions
	}
	return info.Permissions
}

// SetPermissions sets the file permissions.
func (file File) SetPermissions(perm Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
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
	fileSystem, path := file.ParseRawURI()
	return fsListDir(ctx, fileSystem, path, patterns, FileInfoToFileCallback(callback))
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
		err := file.ListDirContext(ctx,
			func(listedFile File) error {
				if !yield(listedFile, nil) {
					return errStopListing
				}
				return nil
			},
			patterns...,
		)
		if err != nil && !errors.Is(err, errStopListing) {
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
// matching a path pattern relative to the file.
//
// See [File.Glob] for the pattern syntax.
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
// matching a path pattern relative to the file.
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
					matchedFile.glob(onlyDirs, segments[1:], append(slices.Clone(values), matchedFile.Name()))(yield)
				}
			} else {
				// No wildcard in segment, join path and recurse
				file.Join(segments[0]).glob(onlyDirs, segments[1:], values)(yield)
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
	fileSystem, path := file.ParseRawURI()
	return fsListDir(ctx, fileSystem, path, patterns, callback)
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

// ListDirRecursiveIter returns an iterator that yields every file
// recursively in the directory and sub-directories.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
func (file File) ListDirRecursiveIter(patterns ...string) iter.Seq2[File, error] {
	return file.ListDirRecursiveIterContext(context.Background(), patterns...)
}

// ListDirRecursiveIterContext returns an iterator that yields every file
// recursively in the directory and sub-directories.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
// Canceling the context will stop the iteration and yield the context error.
func (file File) ListDirRecursiveIterContext(ctx context.Context, patterns ...string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		err := file.ListDirRecursiveContext(ctx,
			func(listedFile File) error {
				if !yield(listedFile, nil) {
					return errStopListing
				}
				return nil
			},
			patterns...,
		)
		if err != nil && !errors.Is(err, errStopListing) {
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
	fileSystem, path := file.ParseRawURI()
	return fsListDirRecursive(ctx, fileSystem, path, patterns, callback)
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
	fileSystem, path := file.ParseRawURI()
	return fsListDirMax(ctx, fileSystem, path, max, patterns)
}

// ListDirRecursiveMax returns at most max files from the directory and its sub-directories.
// A max value of -1 returns all files.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirRecursiveMax(max int, patterns ...string) (files []File, err error) {
	return file.ListDirRecursiveMaxContext(context.Background(), max, patterns...)
}

// ListDirRecursiveMaxContext returns at most max files from the directory and its sub-directories.
// A max value of -1 returns all files.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
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

// User returns the user owner of the file.
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

// SetUser sets the user owner of the file.
func (file File) SetUser(user string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
	if fs, ok := fileSystem.(UserFileSystem); ok {
		return fs.SetUser(path, user)
	}
	return NewErrUnsupported(fileSystem, "SetUser")
}

// Group returns the group owner of the file.
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

// SetGroup sets the group owner of the file.
func (file File) SetGroup(group string) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
	if fs, ok := fileSystem.(GroupFileSystem); ok {
		return fs.SetGroup(path, group)
	}
	return NewErrUnsupported(fileSystem, "SetGroup")
}

// Touch creates an empty file or updates the modification time of an existing file.
//
// On file systems without native touch support only a missing file is created,
// touching an existing file returns an ErrUnsupported error
// instead of destroying its content.
func (file File) Touch(perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fsTouch(fileSystem, path, JoinPermissions(perm, NoPermissions))
}

// MakeDir creates a directory if it does not exist yet.
// No error is returned if the directory already exists.
//
// Compatible with [os.MkdirAll]: if the underlying filesystem reports the
// path already exists (a race with another goroutine or process creating
// the directory between the [File.IsDir] check and the [FileSystem.MakeDir]
// call), MakeDir re-stats the path. If the path is now a directory, returns
// nil (race winner created it; treat as success). If the path exists but is
// not a directory, returns a descriptive [ErrIsNotDirectory].
func (file File) MakeDir(perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	fileSystem, path := file.ParseRawURI()
	if info, err := fsStat(fileSystem, path); err == nil {
		if info.IsDir {
			return nil
		}
		return NewErrIsNotDirectory(file)
	}
	err := fsMakeDir(fileSystem, path, JoinPermissions(perm, NoPermissions))
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		// Race recovery: another goroutine or process may have created the
		// directory between our Stat above and the MakeDir call.
		// Stat the path and only treat the EEXIST as an error if it now
		// exists as a non-directory entry.
		if info, statErr := fsStat(fileSystem, path); statErr == nil {
			if info.IsDir {
				return nil
			}
			return NewErrIsNotDirectory(file)
		}
	}
	return err
}

// MakeAllDirs creates all directories up to this one.
// It does not return an error if the directories already exist.
func (file File) MakeAllDirs(perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fsMakeAllDirs(fileSystem, path, JoinPermissions(perm, NoPermissions))
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

// ReadFrom implements the io.ReaderFrom interface.
// The file is written with the existing permissions if it exists,
// or with the default write permissions if it does not exist yet.
func (file File) ReadFrom(reader io.Reader) (n int64, err error) {
	if file == "" {
		return 0, ErrEmptyPath
	}
	writer, err := file.OpenWriter(file.Permissions())
	if err != nil {
		return 0, err
	}
	defer writer.Close()
	return io.Copy(writer, reader)
}

// OpenReader opens the file and returns a io/fs.File that has to be closed after reading.
func (file File) OpenReader() (ReadCloser, error) {
	fileSystem, path := file.ParseRawURI()
	reader, err := fsOpenReader(fileSystem, path)
	if err != nil {
		return nil, err
	}
	if f, ok := reader.(iofs.File); ok {
		return f, nil
	}
	return statReadCloser{ReadCloser: reader, file: file}, nil
}

// statReadCloser adds a Stat method to an io.ReadCloser
// to implement io/fs.File.
type statReadCloser struct {
	io.ReadCloser
	file File
}

func (r statReadCloser) Stat() (iofs.FileInfo, error) {
	return r.file.Stat()
}

// OpenReadSeeker opens the file and returns a ReadSeekCloser.
// If the FileSystem implementation doesn't support ReadSeekCloser,
// then the complete file is read into memory and wrapped with a ReadSeekCloser.
// Warning: this can use up a lot of memory for big files.
func (file File) OpenReadSeeker() (ReadSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	reader, err := fsOpenReader(fileSystem, path)
	if err != nil {
		return nil, err
	}
	if r, ok := reader.(ReadSeekCloser); ok {
		return r, nil
	}
	defer reader.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return fsimpl.NewReadonlyFileBufferReadAll(reader, info)
}

// OpenWriter opens the file for writing and returns a WriteCloser that has to be closed after writing.
func (file File) OpenWriter(perm ...Permissions) (WriteCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fsOpenWriter(fileSystem, path, JoinPermissions(perm, NoPermissions))
}

// OpenAppendWriter opens the file for appending and returns a WriteCloser that has to be closed after writing.
func (file File) OpenAppendWriter(perm ...Permissions) (WriteCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fsOpenAppendWriter(fileSystem, path, JoinPermissions(perm, NoPermissions))
}

// OpenReadWriter opens the file for reading and writing and returns a ReadWriteSeekCloser that has to be closed.
func (file File) OpenReadWriter(perm ...Permissions) (ReadWriteSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	return fsOpenReadWriter(fileSystem, path, JoinPermissions(perm, NoPermissions))
}

// ReadAll reads and returns all bytes of the file.
func (file File) ReadAll() (data []byte, err error) {
	return file.ReadAllContext(context.Background())
}

// ReadAllContext reads and returns all bytes of the file.
func (file File) ReadAllContext(ctx context.Context) (data []byte, err error) {
	fileSystem, path := file.ParseRawURI()
	return fsReadAll(ctx, fileSystem, path)
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

// WriteAll writes all data to the file.
func (file File) WriteAll(data []byte, perm ...Permissions) error {
	return file.WriteAllContext(context.Background(), data, perm...)
}

// WriteAllContext writes all data to the file.
func (file File) WriteAllContext(ctx context.Context, data []byte, perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fsWriteAll(ctx, fileSystem, path, data, JoinPermissions(perm, NoPermissions))
}

// WriteAllString writes a string to the file.
func (file File) WriteAllString(str string, perm ...Permissions) error {
	return file.WriteAllStringContext(context.Background(), str, perm...)
}

// WriteAllStringContext writes a string to the file.
func (file File) WriteAllStringContext(ctx context.Context, str string, perm ...Permissions) error {
	return file.WriteAllContext(ctx, []byte(str), perm...)
}

// Append appends data to the file.
func (file File) Append(ctx context.Context, data []byte, perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fsAppend(ctx, fileSystem, path, data, JoinPermissions(perm, NoPermissions))
}

// AppendString appends a string to the file.
func (file File) AppendString(ctx context.Context, str string, perm ...Permissions) error {
	return file.Append(ctx, []byte(str), perm...)
}

// Watch a file or directory for changes.
// If the file describes a directory, then
// changes directly within it will be reported.
// This does not apply to changes in deeper
// recursive sub-directories.
//
// It is valid to watch a file with multiple
// callbacks. Calling the returned cancel function
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

// Truncate changes the size of the file.
// If the file is larger than newSize, it is truncated.
// If the file is smaller, it is extended with zeros.
func (file File) Truncate(newSize int64) error {
	fileSystem, path := file.ParseRawURI()
	return fsTruncate(context.Background(), fileSystem, path, newSize)
}

// Rename changes the name of a file where newName is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directory.
func (file File) Rename(newName string) (renamedFile File, err error) {
	fileSystem, path := file.ParseRawURI()
	newPath, err := fsRename(fileSystem, path, newName)
	if err != nil {
		return "", err
	}
	return fsFile(fileSystem, newPath), nil
}

// Renamef changes the name of a file where fmt.Sprintf(newNameFormat, args...)
// is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directory.
func (file File) Renamef(newNameFormat string, args ...any) (renamedFile File, err error) {
	return file.Rename(fmt.Sprintf(newNameFormat, args...))
}

// MoveTo moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
// If the file and distination are using the LocalFileSystem
// but are on different volumes, then the file will be copied
// to the destination and then deleted.
//
// When file and destination resolve to the same location, MoveTo
// returns nil without touching the file, matching the no-op behavior
// of [os.Rename]. See [Move] for the full contract.
func (file File) MoveTo(destination File) error {
	return Move(context.Background(), file, destination)
}

// Remove deletes the file.
func (file File) Remove() error {
	fileSystem, path := file.ParseRawURI()
	return fsRemove(fileSystem, path)
}

// RemoveRecursive deletes the file or if it's a directory
// the complete recursive directory tree.
func (file File) RemoveRecursive() error {
	return file.RemoveRecursiveContext(context.Background())
}

// RemoveRecursiveContext deletes the file or if it's a directory
// the complete recursive directory tree.
// No error is returned if the file does not exist.
func (file File) RemoveRecursiveContext(ctx context.Context) error {
	fileSystem, path := file.ParseRawURI()
	return fsRemoveAll(ctx, fileSystem, path)
}

// RemoveDirContentsRecursive deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursive() error {
	return file.RemoveDirContentsRecursiveContext(context.Background())
}

// RemoveDirContentsRecursiveContext deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursiveContext(ctx context.Context) error {
	return file.ListDirContext(ctx, func(f File) error {
		return f.RemoveRecursiveContext(ctx)
	})
}

// RemoveDirContents deletes all files in this directory,
// or if given all files with patterns from the this directory.
func (file File) RemoveDirContents(patterns ...string) error {
	return file.RemoveDirContentsContext(context.Background(), patterns...)
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

// WriteJSON marshals input to JSON and writes it as the file.
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

// WriteXML marshals input to XML and writes it as the file.
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

// ListXAttr returns the names of all extended attributes for the file.
// Symbolic links are resolved to their target.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) ListXAttr() ([]string, error) {
	return file.listXAttr(true)
}

// LListXAttr returns the names of all extended attributes for the file.
// Symbolic links are not resolved.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) LListXAttr() ([]string, error) {
	return file.listXAttr(false)
}

func (file File) listXAttr(followSymlinks bool) ([]string, error) {
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(XAttrFileSystem); ok {
		return fs.ListXAttr(path, followSymlinks)
	}
	return nil, NewErrUnsupported(fileSystem, "ListXAttr")
}

// GetXAttr returns the value of the named extended attribute.
// Symbolic links are resolved to their target.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) GetXAttr(name string) ([]byte, error) {
	return file.getXAttr(name, true)
}

// LGetXAttr returns the value of the named extended attribute.
// Symbolic links are not resolved.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) LGetXAttr(name string) ([]byte, error) {
	return file.getXAttr(name, false)
}

func (file File) getXAttr(name string, followSymlinks bool) ([]byte, error) {
	fileSystem, path := file.ParseRawURI()
	if fs, ok := fileSystem.(XAttrFileSystem); ok {
		return fs.GetXAttr(path, name, followSymlinks)
	}
	return nil, NewErrUnsupported(fileSystem, "GetXAttr")
}

// SetXAttr sets the value of the named extended attribute.
// Symbolic links are resolved to their target.
// The optional flags parameter controls the behavior
// (e.g., xattr.XATTR_CREATE, xattr.XATTR_REPLACE).
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) SetXAttr(name string, data []byte, flags ...int) error {
	return file.setXAttr(name, data, flags, true)
}

// LSetXAttr sets the value of the named extended attribute.
// Symbolic links are not resolved.
// The optional flags parameter controls the behavior
// (e.g., xattr.XATTR_CREATE, xattr.XATTR_REPLACE).
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) LSetXAttr(name string, data []byte, flags ...int) error {
	return file.setXAttr(name, data, flags, false)
}

func (file File) setXAttr(name string, data []byte, flags []int, followSymlinks bool) error {
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
	if fs, ok := fileSystem.(XAttrFileSystem); ok {
		combinedFlags := 0
		for _, flag := range flags {
			combinedFlags |= flag
		}
		return fs.SetXAttr(path, name, data, combinedFlags, followSymlinks)
	}
	return NewErrUnsupported(fileSystem, "SetXAttr")
}

// RemoveXAttr removes the named extended attribute from the file.
// Symbolic links are resolved to their target.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) RemoveXAttr(name string) error {
	return file.removeXAttr(name, true)
}

// LRemoveXAttr removes the named extended attribute from the file.
// Symbolic links are not resolved.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) LRemoveXAttr(name string) error {
	return file.removeXAttr(name, false)
}

func (file File) removeXAttr(name string, followSymlinks bool) error {
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
	if fs, ok := fileSystem.(XAttrFileSystem); ok {
		return fs.RemoveXAttr(path, name, followSymlinks)
	}
	return NewErrUnsupported(fileSystem, "RemoveXAttr")
}

// NewFileReadWriteAllSeekCloser creates a ReadWriteSeekCloser for a File
// using only File.ReadAll and File.WriteAll.
// The permissions parameter will be used for WriteAll operations.
//
// It uses fsimpl.NewReadWriteAllSeekCloser to implement ReadWriteSeekCloser by lazily reading
// all data from a file when first needed, buffering modifications in memory, and writing
// everything back to the file on Close().
//
// This is useful for file systems that don't support true random access writes,
// such as ZIP archives, where files must be completely rewritten.
func NewFileReadWriteAllSeekCloser(file File, permissions ...Permissions) ReadWriteSeekCloser {
	return fsimpl.NewReadWriteAllSeekCloser(
		file.ReadAll,
		func(data []byte) error {
			return file.WriteAll(data, permissions...)
		},
		// File.ReadAll and File.WriteAll open and close their own handles
		// internally, so there is no persistent handle to release here.
		nil,
	)
}
