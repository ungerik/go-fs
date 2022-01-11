package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

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
	currentUser, _ := user.Current()
	if currentUser == nil || currentUser.HomeDir == "" {
		return path
	}
	return filepath.Join(currentUser.HomeDir, path[1:])
}

func (local *LocalFileSystem) IsReadOnly() bool {
	return false
}

func (local *LocalFileSystem) IsWriteOnly() bool {
	return false
}

func (local *LocalFileSystem) Root() File {
	return localRoot
}

func (local *LocalFileSystem) ID() (string, error) {
	return "/", nil // TODO something more meaningfull like platform dependend the ID of the actual file system
}

func (local *LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (local *LocalFileSystem) Name() string {
	return "local file system"
}

// String implements the fmt.Stringer interface.
func (local *LocalFileSystem) String() string {
	return local.Name() + " with prefix " + local.Prefix()
}

func (local *LocalFileSystem) JoinCleanFile(uri ...string) File {
	return File(local.JoinCleanPath(uri...))
}

func (local *LocalFileSystem) IsAbsPath(filePath string) bool {
	return filepath.IsAbs(filePath)
}

func (local *LocalFileSystem) AbsPath(filePath string) string {
	filePath = expandTilde(filePath)
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		panic(err)
	}
	return absPath
}

func (local *LocalFileSystem) URL(cleanPath string) string {
	return LocalPrefix + filepath.ToSlash(local.AbsPath(cleanPath))
}

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

func (local *LocalFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, LocalPrefix)
	filePath = expandTilde(filePath)
	filePath = strings.TrimPrefix(filePath, Separator)
	filePath = strings.TrimSuffix(filePath, Separator)
	return strings.Split(filePath, Separator)
}

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

func (local *LocalFileSystem) DirAndName(filePath string) (dir, name string) {
	filePath = expandTilde(filePath)
	return fsimpl.DirAndName(filePath, len(filepath.VolumeName(filePath)), Separator)
}

func (local *LocalFileSystem) VolumeName(filePath string) string {
	filePath = expandTilde(filePath)
	return filepath.VolumeName(filePath)
}

func (local *LocalFileSystem) Stat(filePath string) (os.FileInfo, error) {
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

func (local *LocalFileSystem) Exists(filePath string) bool {
	_, err := os.Stat(expandTilde(filePath))
	return err == nil
}

func convertFileInfo(filePath string, info os.FileInfo) FileInfo {
	filePath = expandTilde(filePath)
	hidden, err := hasLocalFileAttributeHidden(filePath)
	if err != nil {
		// Should not happen, this is why we are logging the error
		fmt.Fprintf(os.Stderr, "hasLocalFileAttributeHidden(%s): %+v\n", filePath, err)
		return FileInfo{}
	}
	name := info.Name()
	return NewFileInfo(info, hidden || len(name) > 0 && name[0] == '.')
}

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

func (local *LocalFileSystem) IsSymbolicLink(filePath string) bool {
	filePath = expandTilde(filePath)
	info, err := os.Lstat(filePath)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func (local *LocalFileSystem) CreateSymbolicLink(oldFile, newFile File) error {
	if oldFile == "" || newFile == "" {
		return ErrEmptyPath
	}
	oldFs, oldPath := oldFile.ParseRawURI()
	newFs, newPath := newFile.ParseRawURI()
	oldPath = expandTilde(oldPath)
	newPath = expandTilde(newPath)
	if oldFs != local || newFs != local {
		return errors.New("LocalFileSystem.CreateSymbolicLink needs LocalFileSystem files")
	}
	return os.Symlink(oldPath, newPath)
}

func (local *LocalFileSystem) ReadSymbolicLink(file File) (linked File, err error) {
	if file == "" {
		return "", ErrEmptyPath
	}
	fileFs, filePath := file.ParseRawURI()
	if fileFs != local {
		return "", errors.New("LocalFileSystem.CreateSymbolicLink needs LocalFileSystem files")
	}
	filePath = expandTilde(filePath)
	linkedPath, err := os.Readlink(filePath)
	if err != nil {
		return "", fmt.Errorf("LocalFileSystem.ReadSymbolicLink(%q): error reading link: %w", file, err)
	}
	return File(linkedPath), nil
}

func (local *LocalFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	dirPath = expandTilde(dirPath)
	info, err := local.Stat(dirPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath) //#nosec G304
	if err != nil {
		return fmt.Errorf("LocalFileSystem.ListDirInfo(%q): error opening directory: %w", dirPath, err)
	}
	defer f.Close() //#nosec G307

	for eof := false; !eof; {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		osInfos, err := f.Readdir(64)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return fmt.Errorf("LocalFileSystem.ListDirInfo(%q): error reading directory: %w", dirPath, err)
			}
		}

		for _, osInfo := range osInfos {
			name := osInfo.Name()
			match, err := local.MatchAnyPattern(name, patterns)
			if match {
				file := local.JoinCleanFile(dirPath, name)
				info := convertFileInfo(string(file), osInfo)
				err = callback(file, info)
			}
			if err != nil {
				return fmt.Errorf("LocalFileSystem.ListDirInfo(%q): %w", dirPath, err)
			}
		}
	}
	return nil
}

func (local *LocalFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	dirPath = expandTilde(dirPath)
	return ListDirInfoRecursiveImpl(ctx, local, dirPath, callback, patterns)
}

func (local *LocalFileSystem) ListDirMax(ctx context.Context, dirPath string, n int, patterns []string) (files []File, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if dirPath == "" {
		return nil, ErrEmptyPath
	}

	dirPath = expandTilde(dirPath)
	info, err := local.Stat(dirPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath) //#nosec G304
	if err != nil {
		return nil, fmt.Errorf("LocalFileSystem.ListDirMax(%q): error opening directory: %w", dirPath, err)
	}
	defer f.Close() //#nosec G307

	var numFilesToDo int
	if n > 0 {
		files = make([]File, 0, n)
		numFilesToDo = n
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
				return nil, fmt.Errorf("LocalFileSystem.ListDirMax(%q): error reading directory: %w", dirPath, err)
			}
		}

		for _, name := range names {
			match, err := local.MatchAnyPattern(name, patterns)
			if match {
				files = append(files, local.JoinCleanFile(dirPath, name))
			}
			if err != nil {
				return nil, fmt.Errorf("LocalFileSystem.ListDirMax(%q): %w", dirPath, err)
			}
		}

		if n > 0 {
			numFilesToDo = n - len(files)
		}
	}

	return files, nil
}

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

func (local *LocalFileSystem) User(filePath string) string {
	filePath = expandTilde(filePath)

	panic("not implemented")
}

func (local *LocalFileSystem) SetUser(filePath string, user string) error {
	filePath = expandTilde(filePath)

	panic("not implemented")
}

func (local *LocalFileSystem) Group(filePath string) string {
	filePath = expandTilde(filePath)

	panic("not implemented")
}

func (local *LocalFileSystem) SetGroup(filePath string, group string) error {
	filePath = expandTilde(filePath)

	panic("not implemented")
}

func (local *LocalFileSystem) Touch(filePath string, perm []Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}

	filePath = expandTilde(filePath)
	if local.Exists(filePath) {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	}
	return local.WriteAll(filePath, nil, perm)
}

func (local *LocalFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	dirPath = expandTilde(dirPath)
	p := CombinePermissions(perm, Local.DefaultCreateDirPermissions) | extraDirPermissions
	err := wrapOSErr(dirPath, os.Mkdir(dirPath, p.FileMode(true)))
	if err != nil {
		return err
	}

	if extraDirPermissions != 0 && p&OthersWrite != 0 {
		// On Linux need additional chmod because os.Mkdir does not set OthersWrite bit
		err = os.Chmod(dirPath, p.FileMode(true))
		if err != nil {
			return fmt.Errorf("LocalFileSystem.MakeDir(%q): can't chmod to %0o: %w", dirPath, p, err)
		}
	}

	return nil
}

func (local *LocalFileSystem) ReadAll(filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	data, err := ioutil.ReadFile(filePath) //#nosec G304
	return data, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) WriteAll(filePath string, data []byte, perm []Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return wrapOSErr(filePath, ioutil.WriteFile(filePath, data, p.FileMode(false)))
}

func (local *LocalFileSystem) Append(filePath string, data []byte, perm []Permissions) error {
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

func (local *LocalFileSystem) OpenReader(filePath string) (fs.File, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	f, err := os.OpenFile(filePath, os.O_RDONLY, 0) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	return nil, fmt.Errorf("LocalFileSystem.Watch: %w", ErrNotSupported)
	// events := make(chan WatchEvent, 1)
	// return events
}

func (local *LocalFileSystem) Truncate(filePath string, size int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	info, err := local.Stat(filePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return NewErrIsDirectory(File(filePath))
	}
	if info.Size() <= size {
		return nil
	}
	return os.Truncate(filePath, size)
}

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

	if *buf == nil {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return fmt.Errorf("LocalFileSystem.CopyFile(%q, %q): error from io.CopyBuffer: %w", srcFilePath, destFilePath, err)
	}
	return nil
}

func (local *LocalFileSystem) Rename(filePath string, newName string) error {
	if filePath == "" || newName == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	if !local.Exists(filePath) {
		return NewErrDoesNotExist(File(filePath))
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	return os.Rename(filePath, newPath)
}

func (local *LocalFileSystem) Move(filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	destPath = expandTilde(destPath)
	info, err := local.Stat(filePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		destPath = filepath.Join(destPath, filepath.Base(filePath))
	}
	return os.Rename(filePath, destPath)
}

func (local *LocalFileSystem) Remove(filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	return wrapOSErr(filePath, os.Remove(filePath))
}
