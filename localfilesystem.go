package fs

import (
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const LocalPrefix = "file://"

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

func (local *LocalFileSystem) IsReadOnly() bool {
	return false
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

func (local *LocalFileSystem) File(uri ...string) File {
	return File(filepath.Clean(filepath.Join(uri...)))
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

func (local *LocalFileSystem) CleanPath(uri ...string) string {
	return filepath.Clean(strings.TrimPrefix(filepath.Join(uri...), LocalPrefix))
}

func (local *LocalFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, LocalPrefix)
	filePath = strings.TrimPrefix(filePath, local.Seperator())
	return strings.Split(filePath, local.Seperator())
}

func (local *LocalFileSystem) Seperator() string {
	return string(filepath.Separator)
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

func (local *LocalFileSystem) FileName(filePath string) string {
	return filepath.Base(filePath)
}

func (local *LocalFileSystem) Ext(filePath string) string {
	return filepath.Ext(filePath)
}

func (local *LocalFileSystem) Dir(filePath string) string {
	return filepath.Dir(filePath)
}

// Stat returns FileInfo
func (local *LocalFileSystem) Stat(filePath string) FileInfo {
	info, err := os.Stat(filePath)
	if err != nil {
		return FileInfo{}
	}
	return FileInfo{
		Exists:      true,
		IsDir:       info.Mode().IsDir(),
		IsRegular:   info.Mode().IsRegular(),
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		Permissions: Permissions(info.Mode().Perm()),
	}
}

func (local *LocalFileSystem) ListDir(dirPath string, callback func(File) error, patterns []string) error {
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
		names, err := f.Readdirnames(64)
		if err != nil {
			eof = (err == io.EOF)
			if !eof {
				return err
			}
		}

		for _, name := range names {
			match, err := local.MatchAnyPattern(name, patterns)
			if match {
				err = callback(local.File(dirPath, name))
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (local *LocalFileSystem) ListDirRecursive(dirPath string, callback func(File) error, patterns []string) error {
	return ListDirRecursiveImpl(local, dirPath, callback, patterns)
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
				files = append(files, local.File(dirPath, name))
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
		destPath = filepath.Join(destPath, local.FileName(filePath))
	}
	return os.Rename(filePath, destPath)
}

func (local *LocalFileSystem) Remove(filePath string) error {
	return wrapLocalErrNotExist(filePath, os.Remove(filePath))
}
