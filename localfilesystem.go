package fs

import (
	"errors"
	"fmt"
	"io"
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
	DefaultCreatePermissions    Permissions
	DefaultCreateDirPermissions Permissions
}

func wrapLocalErrNotExist(filePath string, err error) error {
	if os.IsNotExist(err) {
		return NewErrDoesNotExist(File(filePath))
	}
	return err
}

func expandTilde(path string) string {
	if len(path) > 0 && path[0] == '~' {
		currentUser, _ := user.Current()
		if currentUser != nil && currentUser.HomeDir != "" {
			return filepath.Join(currentUser.HomeDir, path[1:])
		}
	}
	return path
}

func (local *LocalFileSystem) IsReadOnly() bool {
	return false
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
	for _, pattern := range patterns {
		match, err := filepath.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

func (local *LocalFileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, len(filepath.VolumeName(filePath)), Separator)
}

// Stat returns FileInfo
func (local *LocalFileSystem) Stat(filePath string) FileInfo {
	info, err := os.Stat(filePath)
	if err != nil {
		return FileInfo{}
	}
	return convertFileInfo(filePath, info)
}

func convertFileInfo(filePath string, info os.FileInfo) FileInfo {
	hidden, err := hasFileAttributeHidden(filePath)
	if err != nil {
		// Should not happen, this is why we are logging the error
		fmt.Fprintf(os.Stderr, "hasFileAttributeHidden(%s): %+v\n", filePath, err)
		return FileInfo{}
	}
	name := info.Name()
	mode := info.Mode()
	return FileInfo{
		Name:        name,
		Exists:      true,
		IsDir:       mode.IsDir(),
		IsRegular:   mode.IsRegular(),
		IsHidden:    hidden || len(name) > 0 && name[0] == '.',
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		Permissions: Permissions(mode.Perm()),
	}
}

func (local *LocalFileSystem) IsHidden(filePath string) bool {
	name := filepath.Base(filePath)
	if len(name) > 0 && name[0] == '.' {
		return true
	}
	hidden, err := hasFileAttributeHidden(filePath)
	if err != nil {
		// Should not happen, this is why we are logging the error
		fmt.Fprintf(os.Stderr, "hasFileAttributeHidden(): %s\n", err)
	}
	return hidden
}

func (local *LocalFileSystem) IsSymbolicLink(filePath string) bool {
	info, err := os.Lstat(filePath)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func (local *LocalFileSystem) CreateSymbolicLink(oldFile, newFile File) error {
	oldFs, oldPath := oldFile.ParseRawURI()
	newFs, newPath := newFile.ParseRawURI()
	if oldFs != local || newFs != local {
		return errors.New("LocalFileSystem.CreateSymbolicLink needs LocalFileSystem files")
	}
	return os.Symlink(oldPath, newPath)
}

func (local *LocalFileSystem) ReadSymbolicLink(file File) (linked File, err error) {
	fileFs, filePath := file.ParseRawURI()
	if fileFs != local {
		return "", errors.New("LocalFileSystem.CreateSymbolicLink needs LocalFileSystem files")
	}
	linkedPath, err := os.Readlink(filePath)
	if err != nil {
		return "", err
	}
	return File(linkedPath), nil
}

func (local *LocalFileSystem) ListDirInfo(dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	info := local.Stat(dirPath)
	if !info.Exists {
		return NewErrDoesNotExist(File(dirPath))
	}
	if !info.IsDir {
		return NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer f.Close()

	for eof := false; !eof; {
		osInfos, err := f.Readdir(64)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return err
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
				return err
			}
		}
	}
	return nil
}

func (local *LocalFileSystem) ListDirInfoRecursive(dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	return ListDirInfoRecursiveImpl(local, dirPath, callback, patterns)
}

func (local *LocalFileSystem) ListDirMax(dirPath string, n int, patterns []string) (files []File, err error) {
	info := local.Stat(dirPath)
	if !info.Exists {
		return nil, NewErrDoesNotExist(File(dirPath))
	}
	if !info.IsDir {
		return nil, NewErrIsNotDirectory(File(dirPath))
	}

	f, err := os.Open(dirPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var numFilesToDo int
	if n > 0 {
		files = make([]File, 0, n)
		numFilesToDo = n
	} else {
		numFilesToDo = 64
	}

	for eof := false; !eof && numFilesToDo > 0; {
		names, err := f.Readdirnames(numFilesToDo)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return nil, err
			}
		}

		for _, name := range names {
			match, err := local.MatchAnyPattern(name, patterns)
			if match {
				files = append(files, local.JoinCleanFile(dirPath, name))
			}
			if err != nil {
				return nil, err
			}
		}

		if n > 0 {
			numFilesToDo = n - len(files)
		}
	}

	return files, nil
}

func (local *LocalFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return os.Chmod(filePath, os.FileMode(perm))
}

func (local *LocalFileSystem) User(filePath string) string {

	panic("not implemented")
}

func (local *LocalFileSystem) SetUser(filePath string, user string) error {
	panic("not implemented")
}

func (local *LocalFileSystem) Group(filePath string) string {
	panic("not implemented")
}

func (local *LocalFileSystem) SetGroup(filePath string, group string) error {
	panic("not implemented")
}

func (local *LocalFileSystem) Touch(filePath string, perm []Permissions) error {
	if local.Stat(filePath).Exists {
		now := time.Now()
		return os.Chtimes(filePath, now, now)
	}
	return local.WriteAll(filePath, nil, perm)
}

func (local *LocalFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreateDirPermissions)
	return wrapLocalErrNotExist(dirPath, os.MkdirAll(dirPath, os.FileMode(p)))
}

func (local *LocalFileSystem) ReadAll(filePath string) ([]byte, error) {
	data, err := ioutil.ReadFile(filePath)
	return data, wrapLocalErrNotExist(filePath, err)
}

func (local *LocalFileSystem) WriteAll(filePath string, data []byte, perm []Permissions) error {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	return wrapLocalErrNotExist(filePath, ioutil.WriteFile(filePath, data, os.FileMode(p)))
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

func (local *LocalFileSystem) OpenReader(filePath string) (ReadSeekCloser, error) {
	f, err := os.OpenFile(filePath, os.O_RDONLY, 0)
	return f, wrapLocalErrNotExist(filePath, err)
}

func (local *LocalFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
	return f, wrapLocalErrNotExist(filePath, err)
}

func (local *LocalFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, os.FileMode(p))
	return f, wrapLocalErrNotExist(filePath, err)
}

func (local *LocalFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	p := CombinePermissions(perm, Local.DefaultCreatePermissions)
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE, os.FileMode(p))
	return f, wrapLocalErrNotExist(filePath, err)
}

func (local *LocalFileSystem) Watch(filePath string) (<-chan WatchEvent, error) {
	return nil, ErrFileWatchNotSupported
	// events := make(chan WatchEvent, 1)
	// return events
}

func (local *LocalFileSystem) Truncate(filePath string, size int64) error {
	info := local.Stat(filePath)
	if !info.Exists {
		return NewErrDoesNotExist(File(filePath))
	}
	if info.IsDir {
		return NewErrIsDirectory(File(filePath))
	}
	if info.Size <= size {
		return nil
	}
	return os.Truncate(filePath, size)
}

func (local *LocalFileSystem) CopyFile(srcFile string, destFile string, buf *[]byte) error {
	srcStat, _ := os.Stat(srcFile)
	destStat, _ := os.Stat(destFile)
	if os.SameFile(srcStat, destStat) {
		return nil
	}

	r, err := os.OpenFile(srcFile, os.O_RDONLY, 0)
	if err != nil {
		return wrapLocalErrNotExist(srcFile, err)
	}
	defer r.Close()

	w, err := os.OpenFile(destFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcStat.Mode().Perm())
	if err != nil {
		return wrapLocalErrNotExist(srcFile, err)
	}
	defer w.Close()

	if *buf == nil {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return err
	}
	return w.Sync()
}

func (local *LocalFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	if !local.Stat(filePath).Exists {
		return NewErrDoesNotExist(File(filePath))
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	return os.Rename(filePath, newPath)
}

func (local *LocalFileSystem) Move(filePath string, destPath string) error {
	if !local.Stat(filePath).Exists {
		return NewErrDoesNotExist(File(filePath))
	}
	if local.Stat(destPath).IsDir {
		destPath = filepath.Join(destPath, filepath.Base(filePath))
	}
	return os.Rename(filePath, destPath)
}

func (local *LocalFileSystem) Remove(filePath string) error {
	return wrapLocalErrNotExist(filePath, os.Remove(filePath))
}
