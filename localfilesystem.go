package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/xattr"

	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// LocalPrefix is the prefix of the LocalFileSystem
	LocalPrefix = "file://"

	// Separator is the path separator of the local file system:
	// the platform-specific [filepath.Separator] as a string,
	// "/" on Unix and `\` on Windows.
	// Other file systems report their separator with [FileSystem.Separator].
	Separator = string(filepath.Separator)
)

// localDefaultPermissions are the permissions of new files and directories
// if neither the perm argument of a method nor the DefaultCreatePermissions
// or DefaultCreateDirPermissions field of the LocalFileSystem are set.
const localDefaultPermissions = UserAndGroupReadWrite

// localID is the file system id of the root directory
// formatted as hex, or "/" if it can't be determined.
var localID = sync.OnceValue(localFileSystemID)

var (
	_ FileSystem             = new(LocalFileSystem)
	_ WriteFileSystem        = new(LocalFileSystem)
	_ TouchFileSystem        = new(LocalFileSystem)
	_ MakeAllDirsFileSystem  = new(LocalFileSystem)
	_ RemoveAllFileSystem    = new(LocalFileSystem)
	_ ReadAllFileSystem      = new(LocalFileSystem)
	_ WriteAllFileSystem     = new(LocalFileSystem)
	_ AppendFileSystem       = new(LocalFileSystem)
	_ AppendWriterFileSystem = new(LocalFileSystem)
	_ ReadWriterFileSystem   = new(LocalFileSystem)
	_ TruncateFileSystem     = new(LocalFileSystem)
	_ CopyFileSystem         = new(LocalFileSystem)
	_ MoveFileSystem         = new(LocalFileSystem)
	_ VolumeNameFileSystem   = new(LocalFileSystem)
	_ AbsPathFileSystem      = new(LocalFileSystem)
	_ HiddenFileSystem       = new(LocalFileSystem)
	_ WatchFileSystem        = new(LocalFileSystem)
	_ PermissionsFileSystem  = new(LocalFileSystem)
	_ XAttrFileSystem        = new(LocalFileSystem)
	_ SymbolicLinkFileSystem = new(LocalFileSystem)
)

// LocalFileSystem implements FileSystem for the local file system.
//
// Use the package variable [Local]. It is registered for the [LocalPrefix],
// and every [File] with a local path or a "file://" URI resolves to it.
// Other LocalFileSystem values, for example with other default permissions,
// can't be registered because Local holds the prefix. They are only usable
// through their own methods, and the Files they return, like their RootDir,
// resolve to Local.
//
// The exported fields are the configuration. Set them once before the
// file system is used, for Local at program start, and don't change them
// while it is in use: they are read without synchronization.
// Zero fields fall back to the defaults documented at the fields,
// so the zero value is ready to use.
type LocalFileSystem struct {
	// DefaultCreatePermissions are the permissions of new files
	// if the perm argument of a method is zero.
	// Zero means UserAndGroupReadWrite.
	DefaultCreatePermissions Permissions

	// DefaultCreateDirPermissions are the permissions of new directories
	// if the perm argument of a method is zero. On Unix the execute bits
	// are always added to the permissions of directories.
	// Zero means UserAndGroupReadWrite.
	DefaultCreateDirPermissions Permissions

	// WatchEventLogger logs every event of the watcher started by
	// Watch if not nil. Watch passes it to the goroutine delivering
	// the events, so a change applies after the next call of Watch.
	WatchEventLogger Logger

	// WatchErrorLogger logs the errors of the watcher started by Watch
	// and the panics of watch callbacks if not nil. Watch passes it
	// to the goroutine delivering the events, so a change applies
	// after the next call of Watch.
	WatchErrorLogger Logger

	watcherMtx sync.RWMutex
	watcher    *localWatcher // nil while no watch is active

	// watchGoroutines counts the running goroutines of the watcher
	// and the watches, so that the tests can check that they end.
	watchGoroutines atomic.Int32
}

// filePermissions returns the permissions of a new file created
// with the perm argument of a method.
func (local *LocalFileSystem) filePermissions(perm Permissions) Permissions {
	return perm.OrDefault(local.DefaultCreatePermissions).OrDefault(localDefaultPermissions)
}

// dirPermissions returns the permissions of a new directory created
// with the perm argument of a method, with the extraDirPermissions
// of the platform added.
func (local *LocalFileSystem) dirPermissions(perm Permissions) Permissions {
	return perm.OrDefault(local.DefaultCreateDirPermissions).OrDefault(localDefaultPermissions) | extraDirPermissions
}

// wrapOSErr returns the typed error of this package for err,
// the error of an operation on filePath, or err if there is none.
func wrapOSErr(filePath string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EISDIR):
		return NewErrIsDirectory(File(filePath))
	case errors.Is(err, syscall.ENOTDIR):
		return NewErrIsNotDirectory(File(filePath))
	case errors.Is(err, os.ErrNotExist):
		return NewErrDoesNotExist(File(filePath))
	case errors.Is(err, os.ErrExist):
		return NewErrAlreadyExists(File(filePath))
	case errors.Is(err, os.ErrPermission):
		return NewErrPermission(File(filePath))
	default:
		return err
	}
}

// wrapFileOpErr is wrapOSErr for the error of an operation
// that only works with files, like opening filePath for writing.
// Not every platform reports such an operation on a directory
// with EISDIR, so for other errors the type of filePath is checked.
func wrapFileOpErr(filePath string, err error) error {
	if err != nil && !errors.Is(err, syscall.EISDIR) && !errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Stat(filePath); statErr == nil && info.IsDir() {
			return NewErrIsDirectory(File(filePath))
		}
	}
	return wrapOSErr(filePath, err)
}

// expandTilde replaces a leading "~" of path with the home
// directory of the current user if path is "~" or begins with
// "~" followed by a separator. Paths like "~name" are returned
// unchanged, the home directories of other users are not looked up.
func expandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~"+Separator) {
		return path
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, path[1:])
	}
	currentUser, _ := user.Current()
	if currentUser == nil || currentUser.HomeDir == "" {
		return path
	}
	return filepath.Join(currentUser.HomeDir, path[1:])
}

// ReadableWritable always returns true, true because the local file system
// is read-write at the file-system level (individual files may still be denied
// access by their OS permissions).
func (local *LocalFileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

// RootDir returns the platform-specific root directory:
// "/" on Unix and `C:\` on Windows.
func (local *LocalFileSystem) RootDir() File {
	return localRoot
}

// ID returns the identifier of the file system of the root directory,
// looked up once and cached: the file system id from statfs (statvfs
// on NetBSD, Solaris and illumos) formatted as hex on Unix, and the volume
// serial number on Windows. WebAssembly platforms (js, wasip1) have no
// file system id, there the ID is the constant "local".
// If the lookup fails, the ID is the root directory.
func (local *LocalFileSystem) ID() string {
	return localID()
}

// Prefix returns [LocalPrefix] ("file://").
func (local *LocalFileSystem) Prefix() string {
	return LocalPrefix
}

// Name returns the human readable name "local file system".
func (local *LocalFileSystem) Name() string {
	return "local file system"
}

// String implements [fmt.Stringer] and returns the [Name] followed by " with prefix " and [Prefix].
func (local *LocalFileSystem) String() string {
	return local.Name() + " with prefix " + local.Prefix()
}

// IsAbsPath reports whether filePath is absolute using [filepath.IsAbs],
// so the rules are platform-specific (e.g. drive-letter prefixes count on Windows).
func (local *LocalFileSystem) IsAbsPath(filePath string) bool {
	return filepath.IsAbs(filePath)
}

// AbsPath resolves filePath to an absolute path via [filepath.Abs].
// If resolution fails, filePath is returned unchanged.
// A leading "~" is not expanded, that is done by [LocalFileSystem.CleanPath].
func (local *LocalFileSystem) AbsPath(filePath string) string {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return filePath
	}
	return absPath
}

// RelPath implements the RelPath method of [AbsPathFileSystem]
// by delegating to [filepath.Rel].
func (local *LocalFileSystem) RelPath(basePath, targPath string) (string, error) {
	return filepath.Rel(basePath, targPath)
}

// URL returns the [LocalPrefix] followed by the absolute path with
// platform separators converted to forward slashes, so a Windows path like
// `C:\dir\file` becomes "file://C:/dir/file".
func (local *LocalFileSystem) URL(cleanPath string) string {
	return LocalPrefix + fsimpl.EscapeURIPath(filepath.ToSlash(local.AbsPath(cleanPath)))
}

// CleanPath strips a leading [LocalPrefix] from the first element,
// joins the parts with [filepath.Join], cleans the result, and finally
// expands "~" and a leading "~" followed by a separator to the home
// directory of the current user. It is the only method that expands the tilde.
// URL escapes are not decoded, that is done by
// [ParseRawURI] for URIs with the [LocalPrefix].
func (local *LocalFileSystem) CleanPath(uriParts ...string) string {
	var cleanPath string
	if len(uriParts) > 0 {
		// Don't modify the passed slice
		cleanPath = filepath.Join(append([]string{strings.TrimPrefix(uriParts[0], LocalPrefix)}, uriParts[1:]...)...)
	}
	cleanPath = filepath.Clean(cleanPath)
	cleanPath = expandTilde(cleanPath)
	return cleanPath
}

// SplitPath trims an optional [LocalPrefix] and splits on [Separator].
// Leading and trailing separators are dropped, so the result has no
// empty elements. An empty or root-only path returns nil.
func (local *LocalFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, LocalPrefix)
	filePath = strings.Trim(filePath, Separator)
	if filePath == "" {
		return nil
	}
	return strings.Split(filePath, Separator)
}

// Separator returns [Separator], which is the platform-specific
// [filepath.Separator] as a string ("/" on Unix, "\\" on Windows).
func (local *LocalFileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (local *LocalFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	if name == "" {
		return false, ErrEmptyPath
	}
	for _, pattern := range patterns {
		if pattern == "" {
			return false, ErrEmptyPath
		}
		match, err := filepath.Match(pattern, name)
		if err != nil {
			return false, fmt.Errorf("LocalFileSystem.MatchAnyPattern: error matching pattern %q with name %q: %w", pattern, name, err)
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

// SplitDirAndName honors the platform-specific volume prefix
// (e.g. `C:` on Windows) and splits filePath into its
// parent directory and last element.
func (*LocalFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, len(filepath.VolumeName(filePath)), Separator)
}

// VolumeName returns the volume prefix of filePath via [filepath.VolumeName].
// On Unix the result is always empty.
func (local *LocalFileSystem) VolumeName(filePath string) string {
	return filepath.VolumeName(filePath)
}

// Stat returns the FileInfo of filePath. A symbolic link is followed,
// so the FileInfo describes its target, with IsSymlink set.
// A non-existent path is returned as [ErrDoesNotExist].
func (local *LocalFileSystem) Stat(filePath string) (*FileInfo, error) {
	linkInfo, err := os.Lstat(filePath)
	if err != nil {
		return nil, wrapOSErr(filePath, err)
	}
	info := linkInfo
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(filePath)
		if err != nil {
			return nil, wrapOSErr(filePath, err)
		}
	}
	fileInfo := NewFileInfo(File(filePath), info, localIsHidden(filePath, linkInfo))
	fileInfo.IsSymlink = linkInfo.Mode()&os.ModeSymlink != 0
	return fileInfo, nil
}

// IsHidden reports whether the file is hidden. A name beginning with "."
// is considered hidden on every platform; additionally on Windows the
// FILE_ATTRIBUTE_HIDDEN attribute is checked. A file whose attributes
// can't be read, for example because it doesn't exist, is not hidden
// unless its name begins with ".".
func (local *LocalFileSystem) IsHidden(filePath string) bool {
	return localIsHidden(filePath, nil)
}

// localIsHidden implements LocalFileSystem.IsHidden.
// info can be nil, else the hidden attribute is read from it
// on platforms where it carries the attribute.
func localIsHidden(filePath string, info os.FileInfo) bool {
	name := filepath.Base(filePath)
	if len(name) > 0 && name[0] == '.' {
		return true
	}
	return localHiddenAttribute(filePath, info)
}

// Lstat implements [SymbolicLinkFileSystem] by calling [os.Lstat],
// so the FileInfo of a symbolic link describes the link itself
// with IsSymlink set. A non-existent path is returned as [ErrDoesNotExist].
func (local *LocalFileSystem) Lstat(filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, wrapOSErr(filePath, err)
	}
	fileInfo := NewFileInfo(File(filePath), info, localIsHidden(filePath, info))
	fileInfo.IsSymlink = info.Mode()&os.ModeSymlink != 0
	return fileInfo, nil
}

// CreateSymbolicLink implements [SymbolicLinkFileSystem] by creating a symbolic
// link at linkPath pointing to targetPath via [os.Symlink].
// On Windows this typically requires the
// "Create symbolic links" privilege or Developer Mode.
func (local *LocalFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if targetPath == "" || linkPath == "" {
		return ErrEmptyPath
	}
	return wrapOSErr(linkPath, os.Symlink(targetPath, linkPath))
}

// ReadSymbolicLink implements [SymbolicLinkFileSystem] by returning the target
// of the symbolic link at linkPath via [os.Readlink]. The returned path is the
// raw link target as stored on disk, so it may be relative to linkPath's
// directory.
func (local *LocalFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if linkPath == "" {
		return "", ErrEmptyPath
	}
	targetPath, err = os.Readlink(linkPath)
	if err != nil {
		return "", wrapOSErr(linkPath, err)
	}
	return targetPath, nil
}

// ListDir reads dirPath in batches of 256 entries using [os.File.ReadDir],
// honoring ctx cancellation between batches. Each entry is filtered through
// [LocalFileSystem.MatchAnyPattern] and its hidden flag is computed via the
// same rules as [LocalFileSystem.IsHidden]. The FileInfo of a symbolic link
// describes the link itself, like [LocalFileSystem.Lstat].
// Entries that are deleted while the directory is read are skipped.
// The function returns [ErrEmptyPath] for an empty dirPath
// and [ErrIsNotDirectory] when dirPath exists but is not a directory.
func (local *LocalFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	dirPath = filepath.Clean(dirPath)

	defer func() {
		if err != nil {
			err = fmt.Errorf("LocalFileSystem.ListDir(%#v): %w", dirPath, err)
		}
	}()

	info, err := local.Stat(dirPath)
	if err != nil {
		return err
	}
	if !info.IsDir {
		return NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath) //#nosec G304
	if err != nil {
		return fmt.Errorf("error opening directory: %w", err)
	}
	defer f.Close() //#nosec G307

	for eof := false; !eof; {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		entries, err := f.ReadDir(256)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return fmt.Errorf("error reading directory: %w", err)
			}
		}

		for _, entry := range entries {
			name := entry.Name()
			match, err := local.MatchAnyPattern(name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
				// Deleted after the directory was read
				continue
			}
			if err != nil {
				return fmt.Errorf("error from fs.DirEntry.Info: %w", err)
			}
			filePath := filepath.Join(dirPath, name)
			err = callback(NewFileInfo(File(filePath), info, localIsHidden(filePath, info)))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// SetPermissions overwrites only the 9 [os.ModePerm] bits of filePath via
// [os.Chmod]. Special bits (setuid, setgid, sticky) and the file-type bits
// already on the file are preserved.
// On Windows only the [UserWrite] bit has an effect (read-only attribute),
// as documented for [os.Chmod].
func (local *LocalFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return wrapOSErr(filePath, err)
	}
	// Combine os.ModePerm bits from perm and non os.ModePerm bits from info.Mode()
	// to keep special non permission related file modes
	mode := (os.FileMode(perm) & os.ModePerm) | (info.Mode() &^ os.ModePerm)
	return wrapOSErr(filePath, os.Chmod(filePath, mode))
}

// Touch updates the access and modification times of an existing file to now
// via [os.Chtimes]. If filePath does not exist, an empty file is created
// with perm, or [LocalFileSystem.DefaultCreatePermissions] if perm is zero.
func (local *LocalFileSystem) Touch(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if _, e := os.Stat(filePath); e == nil {
		now := time.Now()
		return wrapOSErr(filePath, os.Chtimes(filePath, now, now))
	}
	f, err := os.OpenFile(filePath, os.O_CREATE, local.filePermissions(perm).FileMode(false)) //#nosec G304
	if err != nil {
		return wrapOSErr(filePath, err)
	}
	return f.Close()
}

// MakeDir creates a single directory at dirPath via [os.Mkdir]
// with perm, or [LocalFileSystem.DefaultCreateDirPermissions] if perm is zero,
// ORed with the platform-specific extraDirPermissions (the execute bits
// on Unix, zero on Windows). On Unix, an explicit [os.Chmod] is issued
// afterward when OthersWrite is requested because [os.Mkdir] honors umask
// and may drop that bit. [os.ErrExist] is translated to [ErrAlreadyExists]
// (and [os.ErrNotExist] in the parent path to [ErrDoesNotExist]).
func (local *LocalFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	p := local.dirPermissions(perm)
	err := wrapOSErr(dirPath, os.Mkdir(dirPath, p.FileMode(true)))
	if err != nil {
		return err
	}

	if extraDirPermissions != 0 && p&OthersWrite != 0 {
		// On Linux need additional chmod because os.Mkdir does not set OthersWrite bit
		err = os.Chmod(dirPath, p.FileMode(true))
		if err != nil {
			return fmt.Errorf("LocalFileSystem.MakeDir(%#v): can't chmod to %0o: %w", dirPath, p, err)
		}
	}

	return nil
}

// MakeAllDirs implements [MakeAllDirsFileSystem] by calling [os.MkdirAll].
// The same permission and umask-workaround rules as [LocalFileSystem.MakeDir]
// apply; the umask chmod is issued for every directory that did not exist
// before the call, never for existing parent directories.
func (local *LocalFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	p := local.dirPermissions(perm)

	// The directories that don't exist yet, from dirPath upwards
	var missing []string
	if extraDirPermissions != 0 && p&OthersWrite != 0 {
		dir := filepath.Clean(dirPath)
		for {
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				break
			}
			missing = append(missing, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	err := os.MkdirAll(dirPath, p.FileMode(true))
	if err != nil {
		if info, statErr := os.Stat(dirPath); statErr == nil && !info.IsDir() {
			return NewErrIsNotDirectory(File(dirPath))
		}
		return wrapOSErr(dirPath, err)
	}

	for _, dir := range missing {
		// On Linux need additional chmod because os.MkdirAll does not set OthersWrite bit
		err = os.Chmod(dir, p.FileMode(true))
		if err != nil {
			return fmt.Errorf("LocalFileSystem.MakeAllDirs(%#v): can't chmod to %0o: %w", dir, p, err)
		}
	}
	return nil
}

// openFileReader opens the file at filePath for reading and returns it
// with its FileInfo. A directory is an [ErrIsDirectory], because opening
// a directory for reading succeeds, only reading it fails.
func openFileReader(filePath string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(filePath) //#nosec G304
	if err != nil {
		return nil, nil, wrapOSErr(filePath, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, wrapOSErr(filePath, err)
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, nil, NewErrIsDirectory(File(filePath))
	}
	return f, info, nil
}

// ReadAll reads filePath in one shot like [os.ReadFile].
// ctx is only checked before the read starts; the read itself is not cancelable.
// A directory is an [ErrIsDirectory], other OS errors are mapped
// to the typed errors of this package.
func (local *LocalFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	f, info, err := openFileReader(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close() //#nosec G307

	// Like os.ReadFile, use the size as hint for the buffer,
	// a file can change while it is read and the files
	// in /proc for example report a size of zero.
	hint := bytes.MinRead
	if size := info.Size(); size > 0 && size <= math.MaxInt-bytes.MinRead {
		hint += int(size)
	}
	buf := bytes.NewBuffer(make([]byte, 0, hint))
	_, err = buf.ReadFrom(f)
	if err != nil {
		return nil, wrapOSErr(filePath, err)
	}
	return buf.Bytes(), nil
}

// WriteAll writes data to filePath in one shot using [os.WriteFile],
// creating or truncating the file. A new file gets perm, or
// [LocalFileSystem.DefaultCreatePermissions] if perm is zero.
// ctx is only checked before the write starts; the write itself
// is not cancelable. A directory is an [ErrIsDirectory].
func (local *LocalFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return wrapFileOpErr(filePath, os.WriteFile(filePath, data, local.filePermissions(perm).FileMode(false)))
}

// Append opens filePath in append mode via [LocalFileSystem.OpenAppendWriter]
// and writes data. A short write is reported as [io.ErrShortWrite].
// ctx is only checked before opening; the write itself is not cancelable.
func (local *LocalFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	writer, err := local.OpenAppendWriter(filePath, perm)
	if err != nil {
		return err
	}
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	return errors.Join(err, writer.Close())
}

// OpenReader opens filePath read-only. A directory is an [ErrIsDirectory],
// other OS errors are mapped to the typed errors of this package.
// The returned [*os.File] must be closed by the caller.
func (local *LocalFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	f, _, err := openFileReader(filePath)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// OpenWriter opens filePath write-only with O_WRONLY|O_CREATE|O_TRUNC,
// truncating any existing content. A new file gets perm, or
// [LocalFileSystem.DefaultCreatePermissions] if perm is zero.
// A directory is an [ErrIsDirectory].
func (local *LocalFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, local.filePermissions(perm).FileMode(false)) //#nosec G304
	if err != nil {
		return nil, wrapFileOpErr(filePath, err)
	}
	return f, nil
}

// OpenAppendWriter opens filePath with O_WRONLY|O_CREATE|O_APPEND, so writes
// are positioned at end-of-file. A new file gets perm, or
// [LocalFileSystem.DefaultCreatePermissions] if perm is zero.
// A directory is an [ErrIsDirectory].
func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, local.filePermissions(perm).FileMode(false)) //#nosec G304
	if err != nil {
		return nil, wrapFileOpErr(filePath, err)
	}
	return f, nil
}

// OpenReadWriter opens filePath read-write with O_RDWR|O_CREATE.
// Existing content is preserved (no O_TRUNC). A new file gets perm, or
// [LocalFileSystem.DefaultCreatePermissions] if perm is zero.
// A directory is an [ErrIsDirectory].
func (local *LocalFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, local.filePermissions(perm).FileMode(false)) //#nosec G304
	if err != nil {
		return nil, wrapFileOpErr(filePath, err)
	}
	return f, nil
}

// Truncate sets filePath to newSize using [os.Truncate].
// Returns [ErrDoesNotExist] if the file is missing and [ErrIsDirectory]
// if filePath is a directory. A no-op is performed when the file already
// has the requested size.
func (local *LocalFileSystem) Truncate(ctx context.Context, filePath string, newSize int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := local.Stat(filePath)
	if err != nil {
		return err
	}
	if info.IsDir {
		return NewErrIsDirectory(File(filePath))
	}
	if info.Size == newSize {
		return nil
	}
	return wrapOSErr(filePath, os.Truncate(filePath, newSize))
}

// CopyFile copies the file srcFilePath to destFilePath.
// If both paths refer to the same file ([os.SameFile]) the call is a no-op.
// The destination is created with the source file's permission bits and is
// truncated if it already exists. A directory as source or destination
// is an [ErrIsDirectory].
//
// ctx is only checked before the copy starts; the copy itself is not cancelable.
func (local *LocalFileSystem) CopyFile(ctx context.Context, srcFilePath string, destFilePath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if srcFilePath == "" || destFilePath == "" {
		return ErrEmptyPath
	}

	r, srcStat, err := openFileReader(srcFilePath)
	if err != nil {
		return err
	}
	defer r.Close() //#nosec G307

	if destStat, err := os.Stat(destFilePath); err == nil {
		if os.SameFile(srcStat, destStat) {
			return nil
		}
		if destStat.IsDir() {
			return NewErrIsDirectory(File(destFilePath))
		}
	}

	w, err := os.OpenFile(destFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcStat.Mode().Perm()) //#nosec G304
	if err != nil {
		return wrapFileOpErr(destFilePath, err)
	}
	_, err = io.Copy(w, r)
	err = errors.Join(err, w.Close())
	if err != nil {
		return fmt.Errorf("LocalFileSystem.CopyFile(%q, %q): %w", srcFilePath, destFilePath, err)
	}
	return nil
}

// osRename is [os.Rename], a variable so that the tests
// can simulate a rename between devices.
var osRename = os.Rename

// Move moves filePath to destPath, which is always the final path,
// never a directory to move into.
//
// The overwrite semantics are those of [MoveFileSystem.Move]:
// an existing file at destPath is replaced, except by a directory,
// which is an [ErrIsNotDirectory]; an existing directory at destPath
// is an [ErrAlreadyExists], even if it is empty; and moving a directory
// into itself or one of its sub-directories is an error wrapping
// [os.ErrInvalid]. The destination is checked before the rename,
// not atomically with it.
//
// When filePath and destPath are the same after cleaning, Move returns nil
// without touching the file. A destination that only differs in case
// from the source on a case-insensitive file system renames the file.
//
// Move uses [os.Rename], which is atomic but only works within the same
// underlying file system. If the OS returns a cross-device error
// (EXDEV on Unix, ERROR_NOT_SAME_DEVICE on Windows) it falls back to
// copying and removing the source after the copy completely succeeded.
// The fallback is not atomic but can be canceled with ctx.
func (local *LocalFileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	filePath = filepath.Clean(filePath)
	destPath = filepath.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	srcInfo, err := os.Lstat(filePath)
	if err != nil {
		return wrapOSErr(filePath, err)
	}
	if srcInfo.IsDir() && isLocalSubPath(filePath, destPath) {
		return fmt.Errorf("%w: can't move directory %s into itself at %s", os.ErrInvalid, filePath, destPath)
	}
	destInfo, err := os.Lstat(destPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Nothing to replace
	case err != nil:
		return wrapOSErr(destPath, err)
	case os.SameFile(srcInfo, destInfo):
		// A case-only rename on a case-insensitive file system
		// or another hard link of the file, os.Rename handles both
	case destInfo.IsDir():
		// os.Rename would replace an empty directory on some platforms
		return NewErrAlreadyExists(File(destPath))
	case srcInfo.IsDir():
		return NewErrIsNotDirectory(File(destPath))
	}

	err = osRename(filePath, destPath)
	if err != nil && isCrossDeviceError(err) {
		return moveByCopy(ctx, local, filePath, local, destPath)
	}
	return wrapRenameErr(destPath, err)
}

// wrapRenameErr maps the error of os.Rename to destPath
// to the errors documented for Move.
func wrapRenameErr(destPath string, err error) error {
	var linkErr *os.LinkError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EINVAL) && errors.As(err, &linkErr):
		// A directory moved into itself, like os.ErrInvalid
		// for the paths os.Rename got
		return &os.LinkError{Op: linkErr.Op, Old: linkErr.Old, New: linkErr.New, Err: os.ErrInvalid}
	case errors.Is(err, syscall.ENOTDIR):
		return NewErrIsNotDirectory(File(destPath))
	case errors.Is(err, syscall.EISDIR), errors.Is(err, os.ErrExist):
		return NewErrAlreadyExists(File(destPath))
	default:
		// Not mapped to a typed error of one path,
		// because the error can be about both paths
		return err
	}
}

// isLocalSubPath reports if filePath is inside the directory dirPath,
// not counting dirPath itself. The absolute paths are compared with
// the rules of the platform, case-insensitive on Windows.
func isLocalSubPath(dirPath, filePath string) bool {
	dirPath, err := filepath.Abs(dirPath)
	if err != nil {
		return false
	}
	filePath, err = filepath.Abs(filePath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dirPath, filePath)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+Separator)
}

// Remove deletes filePath via [os.Remove]. Directories must be empty,
// a non-empty directory is an [ErrDirectoryNotEmpty].
// For recursive removal use [LocalFileSystem.RemoveAll].
// Other OS errors are mapped to the typed errors of this package.
func (local *LocalFileSystem) Remove(filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	err := os.Remove(filePath)
	if errors.Is(err, os.ErrExist) {
		// rmdir reports a non-empty directory with ENOTEMPTY or EEXIST,
		// Windows with ERROR_DIR_NOT_EMPTY, all of them match os.ErrExist
		return NewErrDirectoryNotEmpty(File(filePath))
	}
	return wrapOSErr(filePath, err)
}

// RemoveAll removes filePath and any children it contains via [os.RemoveAll].
// No error is returned if the path does not exist.
func (local *LocalFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return wrapOSErr(filePath, os.RemoveAll(filePath))
}

// localWatcher is the fsnotify watcher of a LocalFileSystem
// with the watches that get its events.
// Its fields are protected by LocalFileSystem.watcherMtx.
type localWatcher struct {
	fsnotify    *fsnotify.Watcher
	watches     map[string][]*localWatch // key is the cleaned path, nil after Close
	eventLogger Logger
	errorLogger Logger
}

// localWatch is a callback registered with LocalFileSystem.Watch.
// Its own goroutine calls the callback for the queued events one
// after the other, so a slow callback delays only its own events.
type localWatch struct {
	path    string
	onEvent func(File, Event)
	wake    chan struct{} // signals queued events, closed by stop

	mtx     sync.Mutex
	queue   []fsnotify.Event
	stopped bool
}

func newLocalWatch(path string, onEvent func(File, Event)) *localWatch {
	return &localWatch{
		path:    path,
		onEvent: onEvent,
		wake:    make(chan struct{}, 1),
	}
}

// push queues event for the callback unless the watch is stopped.
func (w *localWatch) push(event fsnotify.Event) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.stopped {
		return
	}
	w.queue = append(w.queue, event)
	select {
	case w.wake <- struct{}{}:
	default: // Already signaled
	}
}

// stop drops the queued events and ends the goroutine of the watch
// after the callback that it is running, if any.
func (w *localWatch) stop() {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.stopped {
		return
	}
	w.stopped = true
	w.queue = nil
	close(w.wake)
}

// next removes and returns the next queued event.
// ok is false if the queue is empty or the watch is stopped.
func (w *localWatch) next() (event fsnotify.Event, ok bool) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.stopped || len(w.queue) == 0 {
		return fsnotify.Event{}, false
	}
	event = w.queue[0]
	w.queue = w.queue[1:]
	return event, true
}

// Watch registers onEvent for changes to filePath using [fsnotify].
//
// The callbacks of one watch are called serially, in the order of the
// events, on a goroutine of the watch, so a slow callback delays only
// the later events of its own watch. Panics of a callback are recovered
// and logged to [LocalFileSystem.WatchErrorLogger].
//
// A watch of a directory gets the events of the directory itself
// and of the entries directly inside it. One fsnotify watcher is shared
// by all watches of the LocalFileSystem; it is started by the first Watch
// and closed when the last watch is canceled or by [LocalFileSystem.Close].
// Errors and (when configured) raw events of the watcher are logged
// to [LocalFileSystem.WatchErrorLogger] and [LocalFileSystem.WatchEventLogger].
//
// The returned cancel function ends this watch: queued events are dropped
// and no callback starts after it returned, except the one that is running.
// It can be called more than once and also after Close, then it returns nil.
func (local *LocalFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if onEvent == nil {
		return nil, errors.New("nil callback")
	}
	filePath = filepath.Clean(filePath)
	if _, e := os.Stat(filePath); e != nil {
		return nil, wrapOSErr(filePath, e)
	}

	local.watcherMtx.Lock()
	defer local.watcherMtx.Unlock()

	lw := local.watcher
	if lw == nil {
		fsw, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, fmt.Errorf("LocalFileSystem.Watch(%q): %w", filePath, err)
		}
		lw = &localWatcher{
			fsnotify: fsw,
			watches:  make(map[string][]*localWatch),
		}
		local.watcher = lw
		local.watchGoroutines.Add(1)
		go local.watchLoop(lw)
	}
	lw.eventLogger = local.WatchEventLogger
	lw.errorLogger = local.WatchErrorLogger

	if len(lw.watches[filePath]) == 0 {
		err = lw.fsnotify.Add(filePath)
		if err != nil {
			if len(lw.watches) == 0 {
				// No cancel would close the watcher without watches
				local.watcher = nil
				err = errors.Join(err, lw.fsnotify.Close())
			}
			return nil, fmt.Errorf("LocalFileSystem.Watch(%q): %w", filePath, wrapOSErr(filePath, err))
		}
	}
	w := newLocalWatch(filePath, onEvent)
	lw.watches[filePath] = append(lw.watches[filePath], w)
	local.watchGoroutines.Add(1)
	go local.deliverWatchEvents(lw, w)

	return func() error { return local.cancelWatch(lw, w) }, nil
}

// cancelWatch stops the watch w of the watcher lw and removes it.
// The watcher is closed when its last watch is canceled.
func (local *LocalFileSystem) cancelWatch(lw *localWatcher, w *localWatch) error {
	local.watcherMtx.Lock()
	if local.watcher != lw {
		// Closed, w has been stopped by Close
		local.watcherMtx.Unlock()
		return nil
	}
	watches := lw.watches[w.path]
	i := slices.Index(watches, w)
	if i < 0 {
		// Canceled before
		local.watcherMtx.Unlock()
		return nil
	}
	w.stop()
	if len(watches) > 1 {
		lw.watches[w.path] = slices.Delete(watches, i, i+1)
		local.watcherMtx.Unlock()
		return nil
	}
	delete(lw.watches, w.path)
	if len(lw.watches) > 0 {
		err := lw.fsnotify.Remove(w.path)
		local.watcherMtx.Unlock()
		if errors.Is(err, fsnotify.ErrNonExistentWatch) || errors.Is(err, os.ErrNotExist) {
			// The watch ended with the removal of the watched file,
			// or on Windows the removed file can't be looked up anymore
			return nil
		}
		return err
	}
	local.watcher = nil
	local.watcherMtx.Unlock()
	return lw.fsnotify.Close()
}

// watchLoop dispatches the events of the watcher lw
// to the queues of its watches until lw is closed.
func (local *LocalFileSystem) watchLoop(lw *localWatcher) {
	defer local.watchGoroutines.Add(-1)

	events, errs := lw.fsnotify.Events, lw.fsnotify.Errors
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			local.watcherMtx.RLock()
			logger := lw.eventLogger
			for _, w := range lw.watches[event.Name] {
				w.push(event)
			}
			// Also the watches of the parent directory
			if dir := filepath.Dir(event.Name); dir != event.Name {
				for _, w := range lw.watches[dir] {
					w.push(event)
				}
			}
			local.watcherMtx.RUnlock()
			if logger != nil {
				logger.Printf("watch event: %s", event)
			}

		case err, ok := <-errs:
			if !ok {
				return
			}
			local.watcherMtx.RLock()
			logger := lw.errorLogger
			local.watcherMtx.RUnlock()
			if logger != nil {
				logger.Printf("watch error: %s", err)
			}
		}
	}
}

// deliverWatchEvents is the goroutine of the watch w
// that calls its callback for the queued events until w is stopped.
func (local *LocalFileSystem) deliverWatchEvents(lw *localWatcher, w *localWatch) {
	defer local.watchGoroutines.Add(-1)

	for range w.wake {
		for {
			event, ok := w.next()
			if !ok {
				break
			}
			local.callWatchCallback(lw, w, event)
		}
	}
}

// callWatchCallback calls the callback of w for event,
// recovering and logging a panic of the callback.
func (local *LocalFileSystem) callWatchCallback(lw *localWatcher, w *localWatch, event fsnotify.Event) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		local.watcherMtx.RLock()
		logger := lw.errorLogger
		local.watcherMtx.RUnlock()
		if logger != nil {
			logger.Printf("watch callback panic: %v", p)
		}
	}()
	w.onEvent(File(event.Name), eventFromFSNotify(event.Op))
}

// Close stops the watcher started by Watch, if one is running:
// all watches end without further callbacks, except the ones that are running.
//
// Unlike other file systems the LocalFileSystem is not closed: it stays
// registered and usable, and a later Watch starts a new watcher,
// because [Local] is a process wide singleton.
func (local *LocalFileSystem) Close() error {
	local.watcherMtx.Lock()
	lw := local.watcher
	local.watcher = nil
	if lw != nil {
		for _, watches := range lw.watches {
			for _, w := range watches {
				w.stop()
			}
		}
		lw.watches = nil
	}
	local.watcherMtx.Unlock()

	if lw == nil {
		return nil
	}
	return lw.fsnotify.Close()
}

// ListXAttr returns the names of all extended attributes for filePath
// using the [xattr] package. If followSymlinks is true the symlink target's
// attributes are read ([xattr.List]); otherwise the link's own attributes
// are read ([xattr.LList]). Extended-attribute support is platform-
// and filesystem-dependent (e.g. tmpfs and FAT do not support xattrs).
func (local *LocalFileSystem) ListXAttr(filePath string, followSymlinks bool) ([]string, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if !xattr.XATTR_SUPPORTED {
		return nil, NewErrUnsupported(local, "ListXAttr")
	}
	if followSymlinks {
		return xattr.List(filePath)
	}
	return xattr.LList(filePath)
}

// GetXAttr returns the value of the named extended attribute via the [xattr]
// package ([xattr.Get] when followSymlinks is true, [xattr.LGet] otherwise).
func (local *LocalFileSystem) GetXAttr(filePath string, name string, followSymlinks bool) ([]byte, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if !xattr.XATTR_SUPPORTED {
		return nil, NewErrUnsupported(local, "GetXAttr")
	}
	if followSymlinks {
		return xattr.Get(filePath, name)
	}
	return xattr.LGet(filePath, name)
}

// SetXAttr sets the value of the named extended attribute via the [xattr]
// package ([xattr.SetWithFlags] when followSymlinks is true, otherwise
// [xattr.LSetWithFlags]). The flags are translated to the platform values
// of XATTR_CREATE and XATTR_REPLACE, on platforms without them
// they are emulated by checking for an existing attribute first.
func (local *LocalFileSystem) SetXAttr(filePath string, name string, data []byte, flags XAttrFlags, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if !xattr.XATTR_SUPPORTED {
		return NewErrUnsupported(local, "SetXAttr")
	}
	nativeFlags, ok := xattrNativeFlags(flags)
	if !ok && flags != 0 {
		// Emulate the flags on platforms without native flag values
		_, getErr := local.GetXAttr(filePath, name, followSymlinks)
		switch exists := getErr == nil; {
		case flags&XAttrCreate != 0 && exists:
			return fmt.Errorf("SetXAttr %q: %w", name, os.ErrExist)
		case flags&XAttrReplace != 0 && !exists:
			return fmt.Errorf("SetXAttr %q: %w", name, os.ErrNotExist)
		}
	}
	if followSymlinks {
		return xattr.SetWithFlags(filePath, name, data, nativeFlags)
	}
	return xattr.LSetWithFlags(filePath, name, data, nativeFlags)
}

// RemoveXAttr removes the named extended attribute via the [xattr]
// package ([xattr.Remove] when followSymlinks is true, [xattr.LRemove] otherwise).
func (local *LocalFileSystem) RemoveXAttr(filePath string, name string, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if !xattr.XATTR_SUPPORTED {
		return NewErrUnsupported(local, "RemoveXAttr")
	}
	if followSymlinks {
		return xattr.Remove(filePath, name)
	}
	return xattr.LRemove(filePath, name)
}

// eventFromFSNotify maps the operations of an fsnotify event to an Event.
func eventFromFSNotify(op fsnotify.Op) Event {
	var event Event
	for _, m := range []struct {
		op    fsnotify.Op
		event Event
	}{
		{fsnotify.Create, EventCreate},
		{fsnotify.Write, EventWrite},
		{fsnotify.Remove, EventRemove},
		{fsnotify.Rename, EventRename},
		{fsnotify.Chmod, EventChmod},
	} {
		if op.Has(m.op) {
			event |= m.event
		}
	}
	return event
}
