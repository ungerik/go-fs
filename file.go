package fs

import (
	"bytes"
	"context"
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
	_ FileReader   = File("")
	_ fmt.Stringer = File("")
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
	fileSystem, path := file.ParseRawURI()
	if _, ok := fileSystem.(URIFileSystem); ok {
		// The path of a URI can end with a query or fragment
		_, name := fsSplitDirAndName(fileSystem, path)
		return fsimpl.Ext(name, fileSystem.Separator())
	}
	return fsimpl.Ext(path, fileSystem.Separator())
}

// ExtLower returns the lower case extension of file name including the point, or an empty string.
// Example: File("Image.PNG").ExtLower() == ".png"
func (file File) ExtLower() string {
	return strings.ToLower(file.Ext())
}

// TrimExt returns a File with a path where the extension is removed.
// Note that this does not rename an actual existing file.
// For URIs with a query or fragment like HTTP URLs,
// the extension of the name before the query is removed.
func (file File) TrimExt() File {
	fileSystem, path := file.ParseRawURI()
	if _, ok := fileSystem.(URIFileSystem); ok {
		ext := file.Ext()
		if ext == "" {
			return file
		}
		// The path of a URI ends at the first "?" or "#"
		end := strings.IndexAny(path, "?#")
		if end < 0 {
			end = len(path)
		}
		if !strings.HasSuffix(path[:end], ext) {
			return file
		}
		return fsFile(fileSystem, path[:end-len(ext)]+path[end:])
	}
	return File(fsimpl.TrimExt(string(file), fileSystem.Separator()))
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

// IsWritable returns if the file or directory exists and is writable,
// or in case it doesn't exist,
// if the parent directory exists and is writable.
//
// Only the user write permission bit is checked, not whether the
// current process can actually write to the file.
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
		return (info.IsRegular || info.IsDir) && info.Permissions&UserWrite != 0
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
// Like os.Stat it follows symbolic links, see [FileInfo.StdFileInfo].
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
		return "", fmt.Errorf("%w: %s and %s", ErrFileSystemsDoNotMatch, fileSystem, targetFileSystem)
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
	l, err := file.ListDirMax(context.Background(), 1)
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
	info, err := file.Lstat()
	return err == nil && info.IsSymlink
}

// Lstat returns the FileInfo of the file without following
// a symbolic link: for a symbolic link the FileInfo describes
// the link itself with IsSymlink set.
// On file systems without support for symbolic links
// Lstat returns the same as Stat would.
func (file File) Lstat() (*FileInfo, error) {
	fileSystem, path := file.ParseRawURI()
	return fsLstat(fileSystem, path)
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
		return fmt.Errorf("%w: %s and %s", ErrFileSystemsDoNotMatch, linkFS, targetFS)
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
// The returned target path is raw as stored by the file system and may be
// relative to file's directory.
//
// Returns an [ErrUnsupported] error if file's [FileSystem] does not
// implement [SymbolicLinkFileSystem].
func (file File) ReadSymbolicLink() (string, error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileSystem, linkPath := file.ParseRawURI()
	symlinkFS, ok := fileSystem.(SymbolicLinkFileSystem)
	if !ok {
		return "", NewErrUnsupported(fileSystem, "ReadSymbolicLink")
	}
	return symlinkFS.ReadSymbolicLink(linkPath)
}

// Size returns the size of the file or 0 if it does not exist or is a directory.
// File systems report different sizes for directories,
// the size of their directory entries for example,
// so a directory always has the size 0 here.
func (file File) Size() int64 {
	fileSystem, path := file.ParseRawURI()
	info, err := fsStat(fileSystem, path)
	if err != nil || info.IsDir {
		return 0
	}
	return info.Size
}

// ContentHash returns the DefaultContentHash for the file.
// If the FileSystem implementation does not have this hash pre-computed,
// then the whole file is read to compute it.
// If the file is a directory, then an empty string will be returned.
func (file File) ContentHash(ctx context.Context) (string, error) {
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

// ListDir calls the passed callback function for every file and directory in the directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// Canceling the context or returning an error from the callback
// will stop the listing and return the context or callback error.
func (file File) ListDir(ctx context.Context, callback func(File) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fsListDir(ctx, fileSystem, path, patterns, FileInfoToFileCallback(callback))
}

// ListDirIter returns an iterator that yields every file and directory in the directory.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
// Canceling the context will stop the iteration and yield the context error.
func (file File) ListDirIter(ctx context.Context, patterns ...string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		err := file.ListDir(ctx,
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

// Glob yields the files matching a path pattern.
//
// A relative pattern is matched relative to the current working directory,
// an empty pattern yields the current working directory ".".
// A pattern can also be the URI of a registered file system,
// for example "mem://id/dir/*.txt".
// The part of the pattern before the path segment with the first
// wildcard is the base directory, see [File.Glob] for the syntax
// of the rest and for the errors that are yielded.
//
// A pattern without wildcards yields the file it names if it exists,
// or only if it is a directory when the pattern ends with a slash '/'.
func Glob(ctx context.Context, pattern string) iter.Seq2[File, error] {
	// Find the first wildcard
	i := strings.IndexAny(pattern, globWildcards)
	if i == -1 {
		// No wildcard in pattern, yield the file the pattern names.
		// The pattern is cleaned as path of its file system,
		// path.Clean would break the "://" of URIs.
		file := File(".")
		if pattern != "" {
			fileSystem, filePath := ParseRawURI(pattern)
			file = fsFile(fileSystem, filePath)
		}
		if strings.HasSuffix(pattern, "/") {
			return file.Glob(ctx, "/") // only a directory matches
		}
		return file.Glob(ctx, "")
	}
	// Find the last path separator before the first wildcard
	i = strings.LastIndexByte(pattern[:i], '/')
	if i == -1 {
		// No path separator before the first wildcard
		// means that the pattern is relative to the current directory
		return CurrentWorkingDir().Glob(ctx, pattern)
	}
	// Split pattern into base directory and glob pattern
	return File(pattern[:i+1]).Glob(ctx, pattern[i+1:])
}

// Glob yields the files matching a path pattern relative to the file.
//
// The syntax of patterns is the same as in [path.Match].
// It always uses slash '/' as path segment separator
// independently of the file's file system.
// Empty and "." segments are ignored, ".." segments are not allowed.
// A pattern ending with a slash '/' matches only directories,
// an empty pattern yields the file itself if it exists.
//
// Like [path/filepath.Glob], a path that does not exist or that continues
// below a file is no match. Errors are yielded together with InvalidFile:
// a malformed pattern (wrapping [path.ErrBadPattern]), an empty file path,
// errors listing a directory (for example missing permissions) and the
// error of a canceled context. The iteration stops after a pattern or
// context error and continues after other errors as long as the loop does.
func (file File) Glob(ctx context.Context, pattern string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		if file == "" {
			yield(InvalidFile, ErrEmptyPath)
			return
		}
		segments, onlyDirs, err := globSegments(pattern)
		if err != nil {
			yield(InvalidFile, err)
			return
		}
		g := globber{ctx: ctx, onlyDirs: onlyDirs, yield: yield}
		g.walk(file, segments)
	}
}

// globWildcards are the characters that make a pattern segment a wildcard.
const globWildcards = `*?[\`

func containsWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, globWildcards)
}

// globSegments splits a Glob pattern into its path segments
// and reports if it matches only directories.
func globSegments(pattern string) (segments []string, onlyDirs bool, err error) {
	onlyDirs = strings.HasSuffix(pattern, "/")
	for segment := range strings.SplitSeq(strings.Trim(pattern, "/"), "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			return nil, false, fmt.Errorf("%w, must not contain '..': %q", path.ErrBadPattern, pattern)
		}
		// path.Match validates the whole segment even if it does not match
		if _, err := path.Match(segment, ""); err != nil {
			return nil, false, fmt.Errorf("%w: %q", err, pattern)
		}
		segments = append(segments, segment)
	}
	return segments, onlyDirs, nil
}

// globber holds the state of one Glob iteration.
type globber struct {
	ctx      context.Context
	onlyDirs bool
	yield    func(File, error) bool
	stopped  bool // yield returned false or the context was canceled
}

// walk yields the files below dir that match segments.
func (g *globber) walk(dir File, segments []string) {
	if g.stopped {
		return
	}
	if err := g.ctx.Err(); err != nil {
		g.fail(err)
		return
	}
	i := slices.IndexFunc(segments, containsWildcard)
	if i < 0 {
		// No wildcard left, the joined path is the only candidate
		g.yieldIfExists(dir.Join(segments...))
		return
	}
	dir = dir.Join(segments[:i]...)
	pattern, rest := segments[i], segments[i+1:]
	if len(rest) == 0 {
		// Last segment: yield the matching entries while listing
		err := dir.ListDirInfo(g.ctx, func(info *FileInfo) error {
			if g.onlyDirs && !info.IsDir {
				return nil
			}
			if !g.yield(info.File, nil) {
				g.stopped = true
				return errStopListing
			}
			return nil
		}, pattern)
		g.listErr(dir, err)
		return
	}
	// Collect the matching directories before descending
	// so that only one directory is listed at a time
	var subDirs []File
	err := dir.ListDirInfo(g.ctx, func(info *FileInfo) error {
		if info.IsDir {
			subDirs = append(subDirs, info.File)
		}
		return nil
	}, pattern)
	g.listErr(dir, err)
	for _, subDir := range subDirs {
		g.walk(subDir, rest)
	}
}

// yieldIfExists yields file if it exists
// and is a directory in case of onlyDirs.
func (g *globber) yieldIfExists(file File) {
	fileSystem, filePath := file.ParseRawURI()
	info, err := fsStat(fileSystem, filePath)
	switch {
	case err != nil:
		if !globNoMatch(file, err) && !g.yield(InvalidFile, err) {
			g.stopped = true
		}
	case g.onlyDirs && !info.IsDir:
		// Not a directory, no match
	case !g.yield(file, nil):
		g.stopped = true
	}
}

// listErr handles the error of listing dir.
func (g *globber) listErr(dir File, err error) {
	switch {
	case err == nil || g.stopped:
		// Listed completely or stopped by yield
	case g.ctx.Err() != nil:
		g.fail(err)
	case globNoMatch(dir, err) || globIsNoDir(dir):
		// Missing directory or not a directory, no match
	case !g.yield(InvalidFile, err):
		g.stopped = true
	}
}

// globIsNoDir reports if file exists and is not a directory,
// for file systems that don't return an ErrIsNotDirectory
// when listing a file.
func globIsNoDir(file File) bool {
	fileSystem, filePath := file.ParseRawURI()
	info, err := fsStat(fileSystem, filePath)
	return err == nil && !info.IsDir
}

// fail yields an error that ends the iteration.
func (g *globber) fail(err error) {
	g.yield(InvalidFile, err)
	g.stopped = true
}

// globNoMatch reports if err from accessing file means
// that the path does not exist as needed by the pattern,
// which is no match instead of an error that may hide matches.
func globNoMatch(file File, err error) bool {
	for {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrIsNotDirectory{}) {
			return true
		}
		// Errors like the "not a directory" of a local path that
		// continues below a file are no match if a parent is not
		// a directory. The loop ends at the root, where Dir
		// doesn't return a shorter path anymore.
		dir := file.Dir()
		if dir == "" || len(dir) >= len(file) {
			return false
		}
		fileSystem, dirPath := dir.ParseRawURI()
		info, statErr := fsStat(fileSystem, dirPath)
		if statErr == nil {
			return !info.IsDir
		}
		file, err = dir, statErr
	}
}

// ListDirInfo calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirInfo(ctx context.Context, callback func(*FileInfo) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fsListDir(ctx, fileSystem, path, patterns, callback)
}

// ListDirRecursive calls the passed callback function for every file (not directory)
// recursing into all sub-directories.
// patterns are only applied to files, not to directories.
// Symbolic links to directories are followed, except a link back to a directory
// of the listing, see [ListDirRecursiveFileSystem].
func (file File) ListDirRecursive(ctx context.Context, callback func(File) error, patterns ...string) error {
	return file.ListDirInfoRecursive(ctx, FileInfoToFileCallback(callback), patterns...)
}

// ListDirRecursiveIter returns an iterator that yields every file
// recursively in the directory and sub-directories.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
// In case of an error, the iterator will yield InvalidFile and the error
// as last key and value and then stop the iteration.
// Canceling the context will stop the iteration and yield the context error.
func (file File) ListDirRecursiveIter(ctx context.Context, patterns ...string) iter.Seq2[File, error] {
	return func(yield func(File, error) bool) {
		err := file.ListDirRecursive(ctx,
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

// ListDirInfoRecursive calls the passed callback function for every file
// (not directory) in dirPath recursing into all sub-directories.
// If any patterns are passed, then only files (not directories) with a name that matches
// at least one of the patterns are returned.
// Symbolic links to directories are followed, except a link back to a directory
// of the listing, see [ListDirRecursiveFileSystem].
func (file File) ListDirInfoRecursive(ctx context.Context, callback func(*FileInfo) error, patterns ...string) error {
	fileSystem, path := file.ParseRawURI()
	return fsListDirRecursive(ctx, fileSystem, path, patterns, callback)
}

// ListDirMax returns at most max files and directories in dirPath.
// A max value of -1 returns all files.
// If any patterns are passed, then only files or directories with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirMax(ctx context.Context, max int, patterns ...string) (files []File, err error) {
	fileSystem, path := file.ParseRawURI()
	return fsListDirMax(ctx, fileSystem, path, max, patterns)
}

// ListDirRecursiveMax returns at most max files from the directory and its sub-directories.
// A max value of -1 returns all files.
// If any patterns are passed, then only files with a name that matches
// at least one of the patterns are returned.
func (file File) ListDirRecursiveMax(ctx context.Context, max int, patterns ...string) (files []File, err error) {
	if file == "" {
		return nil, ErrEmptyPath
	}
	return listDirMaxImpl(ctx, max, func(ctx context.Context, callback func(File) error) error {
		return file.ListDirRecursive(ctx, callback, patterns...)
	})
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
//
// The error of closing the writer is returned, because writers
// of uploads commit the data in Close. If reading or writing fails,
// a writer implementing [WriteAborter] is aborted instead of closed,
// so no partial data replaces the file.
func (file File) ReadFrom(reader io.Reader) (n int64, err error) {
	if file == "" {
		return 0, ErrEmptyPath
	}
	writer, err := file.OpenWriter(file.Permissions())
	if err != nil {
		return 0, err
	}
	n, err = io.Copy(writer, reader)
	if err != nil {
		if aborter, ok := writer.(WriteAborter); ok {
			return n, errors.Join(err, aborter.Abort())
		}
	}
	return n, errors.Join(err, writer.Close())
}

// OpenReader opens the file and returns a io/fs.File that has to be closed after reading.
// A reader of the file system that can seek, like an fsimpl.RangeReader,
// still implements io.Seeker.
func (file File) OpenReader() (ReadCloser, error) {
	fileSystem, path := file.ParseRawURI()
	reader, err := fsOpenReader(fileSystem, path)
	if err != nil {
		return nil, err
	}
	if f, ok := reader.(iofs.File); ok {
		return f, nil
	}
	if seeker, ok := reader.(io.ReadSeekCloser); ok {
		return statReadSeekCloser{ReadSeekCloser: seeker, file: file}, nil
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

// statReadSeekCloser adds a Stat method to an io.ReadSeekCloser
// to implement io/fs.File without hiding its Seek method.
type statReadSeekCloser struct {
	io.ReadSeekCloser
	file File
}

func (r statReadSeekCloser) Stat() (iofs.FileInfo, error) {
	return r.file.Stat()
}

// OpenReadSeeker opens the file and returns a ReadAtSeekCloser.
// If the FileSystem implementation doesn't support ReadAtSeekCloser,
// then the complete file is read into memory and wrapped with a ReadAtSeekCloser.
// Warning: this can use up a lot of memory for big files.
func (file File) OpenReadSeeker() (ReadAtSeekCloser, error) {
	fileSystem, path := file.ParseRawURI()
	reader, err := fsOpenReader(fileSystem, path)
	if err != nil {
		return nil, err
	}
	if r, ok := reader.(ReadAtSeekCloser); ok {
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
func (file File) ReadAll(ctx context.Context) (data []byte, err error) {
	fileSystem, path := file.ParseRawURI()
	return fsReadAll(ctx, fileSystem, path)
}

// ReadAllContentHash reads and returns all bytes of the file
// together with the DefaultContentHash.
func (file File) ReadAllContentHash(ctx context.Context) (data []byte, hash string, err error) {
	data, err = file.ReadAll(ctx)
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
func (file File) ReadAllString(ctx context.Context) (string, error) {
	data, err := file.ReadAll(ctx)
	if data == nil || err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteAll writes all data to the file.
func (file File) WriteAll(ctx context.Context, data []byte, perm ...Permissions) error {
	fileSystem, path := file.ParseRawURI()
	return fsWriteAll(ctx, fileSystem, path, data, JoinPermissions(perm, NoPermissions))
}

// WriteAllString writes a string to the file.
func (file File) WriteAllString(ctx context.Context, str string, perm ...Permissions) error {
	return file.WriteAll(ctx, []byte(str), perm...)
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
func (file File) Truncate(ctx context.Context, newSize int64) error {
	fileSystem, path := file.ParseRawURI()
	return fsTruncate(ctx, fileSystem, path, newSize)
}

// Rename changes the name of a file where newName is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directory.
//
// An existing file with newName is replaced, an existing directory
// with newName is an error wrapping os.ErrExist.
// The ctx is used for file systems without native move support
// where the file is copied and the original removed.
func (file File) Rename(ctx context.Context, newName string) (renamedFile File, err error) {
	fileSystem, path := file.ParseRawURI()
	newPath, err := fsRename(ctx, fileSystem, path, newName)
	if err != nil {
		return "", err
	}
	return fsFile(fileSystem, newPath), nil
}

// Renamef changes the name of a file where fmt.Sprintf(newNameFormat, args...)
// is the name part after file.Dir().
// Note: this does not move the file like in other rename implementations,
// it only changes the name of the file within its directory.
func (file File) Renamef(ctx context.Context, newNameFormat string, args ...any) (renamedFile File, err error) {
	return file.Rename(ctx, fmt.Sprintf(newNameFormat, args...))
}

// MoveTo moves and/or renames the file to destination.
// destination can be a directory or file-path and
// can be on another FileSystem.
// If the file and destination are using the LocalFileSystem
// but are on different volumes, then the file will be copied
// to the destination and then deleted.
//
// When file and destination resolve to the same location, MoveTo
// returns nil without touching the file, matching the no-op behavior
// of [os.Rename]. See [Move] for the full contract.
func (file File) MoveTo(ctx context.Context, destination File) error {
	return Move(ctx, file, destination)
}

// Remove deletes the file.
func (file File) Remove() error {
	fileSystem, path := file.ParseRawURI()
	return fsRemove(fileSystem, path)
}

// RemoveRecursive deletes the file or if it's a directory
// the complete recursive directory tree.
// No error is returned if the file does not exist.
func (file File) RemoveRecursive(ctx context.Context) error {
	fileSystem, path := file.ParseRawURI()
	return fsRemoveAll(ctx, fileSystem, path)
}

// RemoveDirContentsRecursive deletes all files and directories in this directory recursively.
func (file File) RemoveDirContentsRecursive(ctx context.Context) error {
	return file.ListDir(ctx, func(f File) error {
		return f.RemoveRecursive(ctx)
	})
}

// RemoveDirContents deletes all files in this directory,
// or if patterns are given the files with names matching
// at least one of the patterns.
//
// Sub-directories and their contents are kept,
// use [File.RemoveDirContentsRecursive] to delete them too.
// Symbolic links are deleted, not the files they point to.
func (file File) RemoveDirContents(ctx context.Context, patterns ...string) error {
	return file.ListDirInfo(ctx, func(info *FileInfo) error {
		if info.IsDir && !info.IsSymlink {
			return nil
		}
		err := info.File.Remove()
		// Ignore files that have been deleted,
		// after all we wanted to get rid of them in the first place,
		// so this is not an error for us
		return RemoveErrDoesNotExist(err)
	}, patterns...)
}

// ReadJSON reads and unmarshals the JSON content of the file to output.
//
// Returns a wrapped ErrUnmarshalJSON when the unmarshalling failed.
func (file File) ReadJSON(ctx context.Context, output any) error {
	data, err := file.ReadAll(ctx)
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalJSON, err)
	}
	return nil
}

// WriteJSON marshals input to JSON and writes it as the file
// with the optional permissions like WriteAll.
//
// Returns a wrapped ErrMarshalJSON when the marshalling failed.
func (file File) WriteJSON(ctx context.Context, input any, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	data, err := marshalJSON(input, "")
	if err != nil {
		return err
	}
	return file.WriteAll(ctx, data, perm...)
}

// WriteJSONIndent marshals input to JSON with every line indented
// by indent and writes it as the file
// with the optional permissions like WriteAll.
//
// Returns a wrapped ErrMarshalJSON when the marshalling failed.
func (file File) WriteJSONIndent(ctx context.Context, input any, indent string, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	data, err := marshalJSON(input, indent)
	if err != nil {
		return err
	}
	return file.WriteAll(ctx, data, perm...)
}

// marshalJSON marshals input to JSON, indented if indent is not empty,
// and wraps marshalling errors with ErrMarshalJSON.
func marshalJSON(input any, indent string) (data []byte, err error) {
	if indent == "" {
		data, err = json.Marshal(input)
	} else {
		data, err = json.MarshalIndent(input, "", indent)
	}
	if err != nil {
		return nil, fmt.Errorf("%w because: %w", ErrMarshalJSON, err)
	}
	return data, nil
}

// ReadXML reads and unmarshals the XML content of the file to output.
//
// Returns a wrapped ErrUnmarshalXML when the unmarshalling failed.
func (file File) ReadXML(ctx context.Context, output any) error {
	data, err := file.ReadAll(ctx)
	if err != nil {
		return err
	}
	err = xml.Unmarshal(data, output)
	if err != nil {
		return fmt.Errorf("%w because: %w", ErrUnmarshalXML, err)
	}
	return nil
}

// WriteXML marshals input to XML and writes it
// with a leading xml.Header as the file
// with the optional permissions like WriteAll.
//
// Returns a wrapped ErrMarshalXML when the marshalling failed.
func (file File) WriteXML(ctx context.Context, input any, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	data, err := marshalXML(input, "")
	if err != nil {
		return err
	}
	return file.WriteAll(ctx, data, perm...)
}

// WriteXMLIndent marshals input to XML with every line indented
// by indent and writes it with a leading xml.Header as the file
// with the optional permissions like WriteAll.
//
// Returns a wrapped ErrMarshalXML when the marshalling failed.
func (file File) WriteXMLIndent(ctx context.Context, input any, indent string, perm ...Permissions) error {
	if file == "" {
		return ErrEmptyPath
	}
	data, err := marshalXML(input, indent)
	if err != nil {
		return err
	}
	return file.WriteAll(ctx, data, perm...)
}

// marshalXML marshals input to XML with a leading xml.Header,
// indented if indent is not empty,
// and wraps marshalling errors with ErrMarshalXML.
func marshalXML(input any, indent string) (data []byte, err error) {
	if indent == "" {
		data, err = xml.Marshal(input)
	} else {
		data, err = xml.MarshalIndent(input, "", indent)
	}
	if err != nil {
		return nil, fmt.Errorf("%w because: %w", ErrMarshalXML, err)
	}
	return append([]byte(xml.Header), data...), nil
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
//
// The entry describes the file itself, not the target of a symbolic link,
// and calls [File.Lstat] for every method except Name.
// Use [NewStdDirEntry] with the FileInfo of a directory listing
// to avoid that.
func (file File) StdDirEntry() StdDirEntry {
	return StdDirEntry{File: file}
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
// The optional flags control the behavior for an existing
// or missing attribute, see XAttrCreate and XAttrReplace.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) SetXAttr(name string, data []byte, flags ...XAttrFlags) error {
	return file.setXAttr(name, data, flags, true)
}

// LSetXAttr sets the value of the named extended attribute.
// Symbolic links are not resolved.
// The optional flags control the behavior for an existing
// or missing attribute, see XAttrCreate and XAttrReplace.
// Returns ErrUnsupported if the file system does not support extended attributes.
func (file File) LSetXAttr(name string, data []byte, flags ...XAttrFlags) error {
	return file.setXAttr(name, data, flags, false)
}

func (file File) setXAttr(name string, data []byte, flags []XAttrFlags, followSymlinks bool) error {
	fileSystem, path := file.ParseRawURI()
	if _, err := fsWritable(fileSystem); err != nil {
		return err
	}
	if fs, ok := fileSystem.(XAttrFileSystem); ok {
		var combinedFlags XAttrFlags
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
		func() ([]byte, error) {
			return file.ReadAll(context.Background())
		},
		func(data []byte) error {
			return file.WriteAll(context.Background(), data, permissions...)
		},
		// File.ReadAll and File.WriteAll open and close their own handles
		// internally, so there is no persistent handle to release here.
		nil,
	)
}
