package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/xattr"

	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// LocalPrefix is the prefix of the LocalFileSystem
	LocalPrefix = "file://"

	// Separator used in LocalFileSystem paths
	Separator = string(filepath.Separator)
)

// LocalFileSystem implements FileSystem for the local file system.
type LocalFileSystem struct {
	// DefaultCreatePermissions are the default file permissions used for creating new files
	DefaultCreatePermissions Permissions
	// DefaultCreateDirPermissions are the default file permissions used for creating new directories
	DefaultCreateDirPermissions Permissions

	WatchEventLogger Logger
	WatchErrorLogger Logger

	watcherMtx     sync.RWMutex
	watcher        *fsnotify.Watcher
	lastCallbackID uint64
	callbacks      map[string]map[uint64]func(File, Event)
}

func wrapOSErr(filePath string, err error) error {
	switch {
	case err == nil:
		return nil
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

func expandTilde(path string) string {
	if len(path) == 0 || path[0] != '~' {
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

// ID returns "/" as a placeholder. It does not currently identify
// the underlying physical file system.
func (local *LocalFileSystem) ID() (string, error) {
	return "/", nil // TODO something more meaningful like platform dependent the ID of the actual file system
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

// JoinCleanFile is a thin wrapper over [LocalFileSystem.JoinCleanPath] that returns a [File].
func (local *LocalFileSystem) JoinCleanFile(uri ...string) File {
	return File(local.JoinCleanPath(uri...))
}

// IsAbsPath reports whether filePath is absolute using [filepath.IsAbs],
// so the rules are platform-specific (e.g. drive-letter prefixes count on Windows).
func (local *LocalFileSystem) IsAbsPath(filePath string) bool {
	return filepath.IsAbs(filePath)
}

// AbsPath expands a leading "~" to the current user's home directory
// and then resolves the path via [filepath.Abs].
// If resolution fails the (tilde-expanded) input is returned unchanged.
func (local *LocalFileSystem) AbsPath(filePath string) string {
	filePath = expandTilde(filePath)
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return filePath
	}
	return absPath
}

// RelPath implements [RelPathFileSystem] by delegating to [filepath.Rel]
// after expanding a leading "~" in both paths.
func (local *LocalFileSystem) RelPath(basePath, targPath string) (string, error) {
	return filepath.Rel(expandTilde(basePath), expandTilde(targPath))
}

// URL returns the [LocalPrefix] followed by the absolute path with
// platform separators converted to forward slashes, so a Windows path like
// `C:\dir\file` becomes "file://C:/dir/file".
func (local *LocalFileSystem) URL(cleanPath string) string {
	return LocalPrefix + filepath.ToSlash(local.AbsPath(cleanPath))
}

// CleanPathFromURI strips a leading [LocalPrefix], ensures a leading separator,
// runs [filepath.Clean], and finally expands a leading "~".
func (local *LocalFileSystem) CleanPathFromURI(uri string) string {
	cleanPath := strings.TrimPrefix(uri, LocalPrefix)
	if cleanPath != "" && !strings.HasPrefix(cleanPath, Separator) {
		cleanPath = Separator + cleanPath
	}
	cleanPath = filepath.Clean(cleanPath)
	cleanPath = expandTilde(cleanPath)
	return cleanPath
}

// JoinCleanPath strips a leading [LocalPrefix] from the first element,
// joins the parts with [filepath.Join], URL-unescapes the result on a best-effort
// basis (a decoding error keeps the escaped form), cleans it, and finally
// expands a leading "~".
func (local *LocalFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], LocalPrefix)
	}
	cleanPath := filepath.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = filepath.Clean(cleanPath)
	cleanPath = expandTilde(cleanPath)
	return cleanPath
}

// SplitPath trims an optional [LocalPrefix], expands a leading "~",
// and splits on [Separator]. Leading and trailing separators are dropped,
// so the result has no empty elements. An empty or root-only path returns nil.
func (local *LocalFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, LocalPrefix)
	filePath = expandTilde(filePath)
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

// SplitDirAndName expands a leading "~", honors the platform-specific
// volume prefix (e.g. `C:` on Windows), and splits filePath into its
// parent directory and last element.
func (*LocalFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	filePath = expandTilde(filePath)
	return fsimpl.SplitDirAndName(filePath, len(filepath.VolumeName(filePath)), Separator)
}

// VolumeName returns the volume prefix of filePath via [filepath.VolumeName]
// after expanding a leading "~". On Unix the result is always empty.
func (local *LocalFileSystem) VolumeName(filePath string) string {
	filePath = expandTilde(filePath)
	return filepath.VolumeName(filePath)
}

// Stat expands a leading "~" and calls [os.Stat]. A non-existent path
// is returned as [ErrDoesNotExist]; other errors are passed through unwrapped.
func (local *LocalFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	filePath = expandTilde(filePath)
	info, err := os.Stat(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = NewErrDoesNotExist(File(filePath))
		}
		return nil, err
	}
	return info, nil
}

// IsHidden reports whether the file is hidden. A name beginning with "."
// is considered hidden on every platform; additionally on Windows the
// FILE_ATTRIBUTE_HIDDEN attribute is checked. On Unix the attribute check
// is a no-op. Errors from the attribute lookup are logged to stderr and
// treated as not-hidden.
func (local *LocalFileSystem) IsHidden(filePath string) bool {
	filePath = expandTilde(filePath)
	name := filepath.Base(filePath)
	if len(name) > 0 && name[0] == '.' {
		return true
	}
	hidden, err := hasLocalFileAttributeHidden(filePath)
	if err != nil {
		// Should not happen, this is why we are logging the error
		// TODO panic or configurable logger instead?
		fmt.Fprintf(os.Stderr, "hasLocalFileAttributeHidden(): %s\n", err)
	}
	return hidden
}

// IsSymbolicLink reports whether filePath is a symbolic link using [os.Lstat].
// Any stat error (including not-exist) returns false.
func (local *LocalFileSystem) IsSymbolicLink(filePath string) bool {
	filePath = expandTilde(filePath)
	info, err := os.Lstat(filePath)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// CreateSymbolicLink implements [SymbolicLinkFileSystem] by creating a symbolic
// link at linkPath pointing to targetPath via [os.Symlink]. A leading "~" in
// either path is expanded. On Windows this typically requires the
// "Create symbolic links" privilege or Developer Mode.
func (local *LocalFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if targetPath == "" || linkPath == "" {
		return ErrEmptyPath
	}
	targetPath = expandTilde(targetPath)
	linkPath = expandTilde(linkPath)
	return os.Symlink(targetPath, linkPath)
}

// ReadSymbolicLink implements [SymbolicLinkFileSystem] by returning the target
// of the symbolic link at linkPath via [os.Readlink]. The returned path is the
// raw link target as stored on disk, so it may be relative to linkPath's
// directory. A leading "~" in linkPath is expanded.
func (local *LocalFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if linkPath == "" {
		return "", ErrEmptyPath
	}
	linkPath = expandTilde(linkPath)
	targetPath, err = os.Readlink(linkPath)
	if err != nil {
		return "", fmt.Errorf("LocalFileSystem.ReadSymbolicLink(%#v): error reading link: %w", linkPath, err)
	}
	return targetPath, nil
}

// ListDirInfo reads dirPath in batches of 256 entries using [os.File.ReadDir],
// honoring ctx cancellation between batches. Each entry is filtered through
// [LocalFileSystem.MatchAnyPattern] and its hidden flag is computed via the
// same rules as [LocalFileSystem.IsHidden]. The function returns
// [ErrEmptyPath] for an empty dirPath and [ErrIsNotDirectory] when dirPath
// exists but is not a directory.
func (local *LocalFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	dirPath = filepath.Clean(dirPath)
	dirPath = expandTilde(dirPath)

	defer func() {
		if err != nil {
			err = fmt.Errorf("LocalFileSystem.ListDirInfo(%#v): %w", dirPath, err)
		}
	}()

	info, err := local.Stat(dirPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
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
			if err != nil {
				return fmt.Errorf("error from fs.DirEntry.Info: %w", err)
			}
			filePath := filepath.Join(dirPath, name)
			hidden := strings.HasPrefix(name, ".")
			if !hidden {
				hidden, err = hasLocalFileAttributeHidden(filePath)
				if err != nil {
					return fmt.Errorf("hasLocalFileAttributeHidden(%#v): %+v\n", filePath, err)
				}
			}
			err = callback(NewFileInfo(File(filePath), info, hidden))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ListDirMax returns up to max entries of dirPath. A negative max means
// "all entries" and is read in 64-name batches via [os.File.Readdirnames];
// a positive max preallocates the result slice and reads exactly enough
// names to satisfy the cap. max == 0 returns nil without touching the disk.
// Entries are filtered by [LocalFileSystem.MatchAnyPattern]. Cancellation
// is checked between batches. Symbolic semantics match [LocalFileSystem.ListDirInfo]
// (same error types for empty / not-a-directory).
func (local *LocalFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []File, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if dirPath == "" {
		return nil, ErrEmptyPath
	}
	if max == 0 {
		return nil, nil
	}

	dirPath = filepath.Clean(dirPath)
	dirPath = expandTilde(dirPath)

	defer func() {
		if err != nil {
			err = fmt.Errorf("LocalFileSystem.ListDirMax(%#v): %w", dirPath, err)
		}
	}()

	info, err := local.Stat(dirPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath) //#nosec G304
	if err != nil {
		return nil, fmt.Errorf("error opening directory: %w", err)
	}
	defer f.Close() //#nosec G307

	var numFilesToDo int
	if max > 0 {
		files = make([]File, 0, max)
		numFilesToDo = max
	} else {
		numFilesToDo = 64
	}

	for eof := false; !eof && numFilesToDo > 0; {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		names, err := f.Readdirnames(numFilesToDo)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return nil, fmt.Errorf("error reading directory: %w", err)
			}
		}

		for _, name := range names {
			match, err := local.MatchAnyPattern(name, patterns)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
			files = append(files, File(filepath.Join(dirPath, name)))
		}

		if max > 0 {
			numFilesToDo = max - len(files)
		}
	}

	return files, nil
}

// SetPermissions overwrites only the 9 [os.ModePerm] bits of filePath via
// [os.Chmod]. Special bits (setuid, setgid, sticky) and the file-type bits
// already on the file are preserved.
func (local *LocalFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	// Combine os.ModePerm bits from perm and non os.ModePerm bits from info.Mode()
	// to keep special non permission related file modes
	mode := (os.FileMode(perm) & os.ModePerm) | (info.Mode() &^ os.ModePerm)
	return os.Chmod(filePath, mode)
}

// Touch updates the access and modification times of an existing file to now
// via [os.Chtimes]. If filePath does not exist, an empty file is created using
// the supplied [Permissions] joined with [LocalFileSystem.DefaultCreatePermissions].
func (local *LocalFileSystem) Touch(filePath string, perm []Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	if _, e := os.Stat(filePath); e == nil {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	}
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_CREATE, p.FileMode(false)) //#nosec G304
	if err != nil {
		return err
	}
	return f.Close()
}

// MakeDir creates a single directory at dirPath via [os.Mkdir].
// Permissions are joined with [LocalFileSystem.DefaultCreateDirPermissions]
// and ORed with the platform-specific extraDirPermissions (the execute bits
// on Unix, zero on Windows). On Unix, an explicit [os.Chmod] is issued
// afterward when OthersWrite is requested because [os.Mkdir] honors umask
// and may drop that bit. [os.ErrExist] is translated to [ErrAlreadyExists]
// (and [os.ErrNotExist] in the parent path to [ErrDoesNotExist]).
func (local *LocalFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	dirPath = expandTilde(dirPath)
	p := JoinPermissions(perm, Local.DefaultCreateDirPermissions) | extraDirPermissions
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
// apply; the umask chmod is issued for every path component that the call
// may have created.
func (local *LocalFileSystem) MakeAllDirs(dirPath string, perm []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	dirPath = expandTilde(dirPath)
	p := JoinPermissions(perm, Local.DefaultCreateDirPermissions) | extraDirPermissions
	err := wrapOSErr(dirPath, os.MkdirAll(dirPath, p.FileMode(true)))
	if err != nil {
		return err
	}

	if extraDirPermissions != 0 && p&OthersWrite != 0 {
		parts := local.SplitPath(dirPath)
		for i := range parts {
			// On Linux need additional chmod because os.Mkdir does not set OthersWrite bit
			subPath := local.JoinCleanPath(parts[0 : i+1]...)
			err = os.Chmod(subPath, p.FileMode(true))
			if err != nil {
				return fmt.Errorf("LocalFileSystem.MakeAllDirs(%#v): can't chmod to %0o: %w", subPath, p, err)
			}
		}
	}

	return nil
}

// ReadAll reads filePath in one shot using [os.ReadFile]. ctx is only
// checked before the read starts; the read itself is not cancelable.
// OS errors are mapped through [wrapOSErr].
func (local *LocalFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	// TODO make really large file op cancelable
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	data, err := os.ReadFile(filePath) //#nosec G304
	return data, wrapOSErr(filePath, err)
}

// WriteAll writes data to filePath in one shot using [os.WriteFile],
// creating or truncating the file with permissions joined from perm and
// [LocalFileSystem.DefaultCreatePermissions]. ctx is only checked
// before the write starts; the write itself is not cancelable.
func (local *LocalFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	// TODO make really large file op cancelable
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	return wrapOSErr(filePath, os.WriteFile(filePath, data, p.FileMode(false)))
}

// Append opens filePath in append mode via [LocalFileSystem.OpenAppendWriter]
// and writes data. A short write is reported as [io.ErrShortWrite].
// ctx is only checked before opening; the write itself is not cancelable.
func (local *LocalFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	// TODO make really large file op cancelable
	if ctx.Err() != nil {
		return ctx.Err()
	}
	writer, err := local.OpenAppendWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		return io.ErrShortWrite
	}
	return err
}

// OpenReader opens filePath read-only via [os.OpenFile] with O_RDONLY.
// OS errors are mapped through [wrapOSErr]. The returned [*os.File]
// must be closed by the caller.
func (local *LocalFileSystem) OpenReader(filePath string) (ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	f, err := os.OpenFile(filePath, os.O_RDONLY, 0) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

// OpenWriter opens filePath write-only with O_WRONLY|O_CREATE|O_TRUNC,
// truncating any existing content. The file is created with permissions
// joined from perm and [LocalFileSystem.DefaultCreatePermissions] when
// it doesn't yet exist.
func (local *LocalFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

// OpenAppendWriter opens filePath with O_WRONLY|O_CREATE|O_APPEND, so writes
// are positioned at end-of-file. The file is created with permissions joined
// from perm and [LocalFileSystem.DefaultCreatePermissions] when it doesn't
// yet exist.
func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

// OpenReadWriter opens filePath read-write with O_RDWR|O_CREATE.
// The file is created with permissions joined from perm and
// [LocalFileSystem.DefaultCreatePermissions] when it doesn't yet exist.
// Existing content is preserved (no O_TRUNC).
func (local *LocalFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

// Truncate sets filePath to newSize using [os.Truncate].
// Returns [ErrDoesNotExist] if the file is missing and [ErrIsDirectory]
// if filePath is a directory. A no-op is performed when the file already
// has the requested size.
func (local *LocalFileSystem) Truncate(filePath string, newSize int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	info, err := local.Stat(filePath)
	if err != nil {
		return NewErrDoesNotExist(File(filePath))
	}
	if info.IsDir() {
		return NewErrIsDirectory(File(filePath))
	}
	if info.Size() == newSize {
		return nil
	}
	return os.Truncate(filePath, newSize)
}

// CopyFile copies srcFilePath to destFilePath using [io.CopyBuffer].
// If both paths refer to the same inode ([os.SameFile]) the call is a no-op.
// The destination is created with the source file's mode bits and is
// truncated if it already exists.
//
// buf controls the copy buffer:
//   - nil pointer: a one-shot buffer is allocated for this call only
//     (no buffer reuse).
//   - pointer to a nil/empty slice: a fresh buffer of copyBufferSize is
//     allocated and stored back through buf for reuse by later calls.
//   - pointer to a non-empty slice: that slice is used as-is.
//
// ctx is only checked before the copy starts; the copy itself is not cancelable.
func (local *LocalFileSystem) CopyFile(ctx context.Context, srcFilePath string, destFilePath string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if srcFilePath == "" || destFilePath == "" {
		return ErrEmptyPath
	}

	srcFilePath = expandTilde(srcFilePath)
	destFilePath = expandTilde(destFilePath)
	srcStat, _ := os.Stat(srcFilePath)
	destStat, _ := os.Stat(destFilePath)
	if os.SameFile(srcStat, destStat) {
		return nil
	}

	r, err := os.OpenFile(srcFilePath, os.O_RDONLY, 0) //#nosec G304
	if err != nil {
		return wrapOSErr(srcFilePath, err)
	}
	defer r.Close() //#nosec G307

	w, err := os.OpenFile(destFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcStat.Mode().Perm()) //#nosec G304
	if err != nil {
		return wrapOSErr(srcFilePath, err)
	}
	defer w.Close() //#nosec G307

	if buf == nil {
		buf = new([]byte)
	}
	if len(*buf) == 0 {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return fmt.Errorf("LocalFileSystem.CopyFile(%q, %q): error from io.CopyBuffer: %w", srcFilePath, destFilePath, err)
	}
	return nil
}

// Rename renames the file at filePath to newName within the same directory
// using [os.Rename]. newName must be a leaf name without any [Separator]
// (otherwise an error is returned); the new full path is built from
// [filepath.Dir](filePath) and newName and returned to the caller.
// A missing source file yields [ErrDoesNotExist].
func (local *LocalFileSystem) Rename(filePath string, newName string) (newPath string, err error) {
	if filePath == "" || newName == "" {
		return "", ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	if strings.ContainsAny(newName, local.Separator()) {
		return "", fmt.Errorf("newName %#v for File.Rename contains path separator %s", newName, local.Separator())
	}
	if _, e := os.Stat(filePath); e != nil {
		return "", NewErrDoesNotExist(File(filePath))
	}
	newPath = filepath.Join(filepath.Dir(filePath), newName)
	err = os.Rename(filePath, newPath)
	if err != nil {
		return "", err
	}
	return newPath, nil
}

// Move moves filePath to destPath. If filePath is a directory, it is moved
// *into* destPath using the base name of filePath as the new child name.
//
// When filePath and destPath resolve to the same location after cleaning,
// Move returns nil without touching the file, matching the no-op behavior
// of [os.Rename] for same-path renames.
//
// Move first tries [os.Rename], which is atomic but only works within the
// same underlying filesystem. If the OS returns a cross-device error
// (EXDEV on Unix, ERROR_NOT_SAME_DEVICE on Windows) it falls back to
// [CopyRecursive] followed by [os.RemoveAll]. The fallback is not atomic
// and uses [context.Background], so it is not cancelable.
func (local *LocalFileSystem) Move(filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	filePath = filepath.Clean(expandTilde(filePath))
	destPath = filepath.Clean(expandTilde(destPath))
	if filePath == destPath {
		return nil
	}
	info, err := local.Stat(filePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		destPath = filepath.Join(destPath, filepath.Base(filePath))
	}
	err = os.Rename(filePath, destPath)
	if err != nil && isCrossDeviceError(err) {
		// os.Rename does not work across filesystem boundaries,
		// fall back to copy + delete
		err = CopyRecursive(context.Background(), File(filePath), File(destPath))
		if err != nil {
			return err
		}
		return os.RemoveAll(filePath)
	}
	return err
}

// Remove deletes filePath via [os.Remove]. Directories must be empty;
// for recursive removal use [CopyRecursive] / [os.RemoveAll] callers
// at a higher level. OS errors are mapped through [wrapOSErr].
func (local *LocalFileSystem) Remove(filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	return wrapOSErr(filePath, os.Remove(filePath))
}

// Watch registers onEvent for changes to filePath using [fsnotify].
// A single fsnotify.Watcher is lazily created and shared by all watches
// on this [LocalFileSystem]; the first call starts the dispatch goroutine.
// Multiple callbacks per path are supported and stored in a per-path map.
// The returned cancel function deregisters this specific callback and only
// removes the underlying fsnotify watch when the last callback for filePath
// is gone. For every event the watcher dispatches callbacks registered at
// both the event path and its parent directory, so a callback registered
// on a directory automatically receives events for entries inside it.
// Errors and (when configured) raw events are forwarded to
// [LocalFileSystem.WatchErrorLogger] and [LocalFileSystem.WatchEventLogger].
// Panics from a callback are recovered and logged to WatchErrorLogger.
func (local *LocalFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if _, e := os.Stat(filePath); e != nil {
		return nil, NewErrDoesNotExist(File(filePath))
	}
	filePath = expandTilde(filePath)

	local.watcherMtx.Lock()
	defer local.watcherMtx.Unlock()

	if local.watcher == nil {
		local.watcher, err = fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		local.callbacks = make(map[string]map[uint64]func(File, Event), 1)
		go local.watchLoop()
	}

	err = local.watcher.Add(filePath)
	if err != nil {
		return nil, err
	}

	callbackID := local.lastCallbackID
	local.lastCallbackID++

	pathCallbacks := local.callbacks[filePath]
	if pathCallbacks == nil {
		pathCallbacks = make(map[uint64]func(File, Event), 1)
	}
	pathCallbacks[callbackID] = onEvent
	local.callbacks[filePath] = pathCallbacks

	cancel = func() error {
		local.watcherMtx.Lock()
		defer local.watcherMtx.Unlock()

		delete(local.callbacks[filePath], callbackID)
		if len(local.callbacks[filePath]) > 0 {
			return nil
		}
		return local.watcher.Remove(filePath)
	}
	return cancel, nil
}

func (local *LocalFileSystem) watchLoop() {
	for {
		select {
		case event, ok := <-local.watcher.Events:
			if !ok {
				return
			}
			if local.WatchEventLogger != nil {
				local.WatchEventLogger.Printf("watch event: %s", event)
			}

			// Collect callbacks during lock
			local.watcherMtx.RLock()
			var callbacks []func(File, Event)
			for _, callback := range local.callbacks[event.Name] {
				callbacks = append(callbacks, callback)
			}
			// Also check for watches of parent directory
			for _, callback := range local.callbacks[filepath.Dir(event.Name)] {
				callbacks = append(callbacks, callback)
			}
			local.watcherMtx.RUnlock()

			// Call them outside of lock
			for _, callback := range callbacks {
				local.watchEventCallback(event, callback)
			}

		case err, ok := <-local.watcher.Errors:
			if !ok {
				return
			}
			if local.WatchErrorLogger != nil {
				local.WatchErrorLogger.Printf("watch error: %s", err)
			}
		}
	}
}

func (local *LocalFileSystem) watchEventCallback(event fsnotify.Event, callback func(File, Event)) {
	defer func() {
		p := recover()
		if p != nil && local.WatchErrorLogger != nil {
			local.WatchErrorLogger.Printf("watch callback panic: %#v", p)
		}
	}()
	callback(File(event.Name), Event(event.Op))
}

// Close is a no-op for the local file system; there are no
// long-lived resources outside the lazily started fsnotify watcher,
// which is tied to the process lifetime.
func (*LocalFileSystem) Close() error {
	return nil
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
	filePath = expandTilde(filePath)
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
	filePath = expandTilde(filePath)
	if followSymlinks {
		return xattr.Get(filePath, name)
	}
	return xattr.LGet(filePath, name)
}

// SetXAttr sets the value of the named extended attribute via the [xattr]
// package ([xattr.SetWithFlags] when followSymlinks is true, otherwise
// [xattr.LSetWithFlags]). The flags parameter is passed through unchanged
// and accepts values like xattr.XATTR_CREATE and xattr.XATTR_REPLACE.
func (local *LocalFileSystem) SetXAttr(filePath string, name string, data []byte, flags int, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	if followSymlinks {
		return xattr.SetWithFlags(filePath, name, data, flags)
	}
	return xattr.LSetWithFlags(filePath, name, data, flags)
}

// RemoveXAttr removes the named extended attribute via the [xattr]
// package ([xattr.Remove] when followSymlinks is true, [xattr.LRemove] otherwise).
func (local *LocalFileSystem) RemoveXAttr(filePath string, name string, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	if followSymlinks {
		return xattr.Remove(filePath, name)
	}
	return xattr.LRemove(filePath, name)
}
