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

func (local *LocalFileSystem) RootDir() File {
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
	filePath = strings.Trim(filePath, Separator)
	if filePath == "" {
		return nil
	}
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

func (local *LocalFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	filePath = expandTilde(filePath)
	return fsimpl.SplitDirAndName(filePath, len(filepath.VolumeName(filePath)), Separator)
}

func (local *LocalFileSystem) VolumeName(filePath string) string {
	filePath = expandTilde(filePath)
	return filepath.VolumeName(filePath)
}

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
		return "", fmt.Errorf("LocalFileSystem.ReadSymbolicLink(%#v): error reading link: %w", file, err)
	}
	return File(linkedPath), nil
}

func (local *LocalFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) (err error) {
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

func (local *LocalFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	dirPath = filepath.Clean(dirPath)
	dirPath = expandTilde(dirPath)
	return ListDirInfoRecursiveImpl(ctx, local, dirPath, callback, patterns)
}

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
	if _, e := os.Stat(filePath); e == nil {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	}
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_CREATE, p.FileMode(false))
	if err != nil {
		return err
	}
	return f.Close()
}

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

func (local *LocalFileSystem) OpenReader(filePath string) (ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	f, err := os.OpenFile(filePath, os.O_RDONLY, 0) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

func (local *LocalFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	filePath = expandTilde(filePath)
	p := JoinPermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, p.FileMode(false)) //#nosec G304
	return f, wrapOSErr(filePath, err)
}

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

	if len(*buf) == 0 {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return fmt.Errorf("LocalFileSystem.CopyFile(%q, %q): error from io.CopyBuffer: %w", srcFilePath, destFilePath, err)
	}
	return nil
}

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
