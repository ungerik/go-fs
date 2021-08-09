package zipfs

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"strings"
	"time"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix for the ZipFileSystem
	Prefix = "zip://"

	// Separator used in ZipFileSystem paths
	Separator = "/"
)

var (
	// Make sure ZipFileSystem implements fs.FileSystem
	_ fs.FileSystem = new(ZipFileSystem)
)

// ZipFileSystem
type ZipFileSystem struct {
	fs.ReadOnlyBase

	prefix string

	closer    io.Closer
	zipReader *zip.Reader
	zipWriter *zip.Writer
}

func NewReaderFileSystem(file fs.File) (zipfs *ZipFileSystem, err error) {
	fileReader, err := file.OpenReadSeeker()
	if err != nil {
		return nil, err
	}
	zipReader, err := zip.NewReader(fileReader, file.Size())
	if err != nil {
		return nil, err
	}
	zipfs = &ZipFileSystem{
		prefix:    Prefix + fsimpl.RandomString(),
		closer:    fileReader,
		zipReader: zipReader,
	}
	fs.Register(zipfs)
	return zipfs, err
}

func NewWriterFileSystem(file fs.File) (zipfs *ZipFileSystem, err error) {
	fileWriter, err := file.OpenWriter()
	if err != nil {
		return nil, err
	}
	zipWriter := zip.NewWriter(fileWriter)
	zipfs = &ZipFileSystem{
		prefix:    Prefix + fsimpl.RandomString(),
		closer:    zipWriter,
		zipWriter: zipWriter,
	}
	fs.Register(zipfs)
	return zipfs, err
}

func (zipfs *ZipFileSystem) Close() error {
	fs.Unregister(zipfs)
	return zipfs.closer.Close()
}

func (zipfs *ZipFileSystem) IsReadOnly() bool {
	return zipfs.zipReader != nil
}

func (zipfs *ZipFileSystem) IsWriteOnly() bool {
	return zipfs.zipWriter != nil
}

func (zipfs *ZipFileSystem) Root() fs.File {
	return fs.File(zipfs.prefix + Separator)
}

func (zipfs *ZipFileSystem) ID() (string, error) {
	return zipfs.prefix, nil
}

// Prefix for the ZipFileSystem
func (zipfs *ZipFileSystem) Prefix() string {
	return zipfs.prefix
}

func (zipfs *ZipFileSystem) Name() string {
	return "Zip reader filesystem " + path.Base(zipfs.prefix)
}

// String implements the fmt.Stringer interface.
func (zipfs *ZipFileSystem) String() string {
	return zipfs.Name() + " with prefix " + zipfs.Prefix()
}

func (zipfs *ZipFileSystem) File(filePath string) fs.File {
	return zipfs.JoinCleanFile(filePath)
}

func (zipfs *ZipFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(zipfs.prefix + zipfs.JoinCleanPath(uriParts...))
}

func (zipfs *ZipFileSystem) URL(cleanPath string) string {
	return zipfs.prefix + cleanPath
}

func (zipfs *ZipFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, zipfs.prefix, Separator)
}

func (zipfs *ZipFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, zipfs.prefix, Separator)
}

func (*ZipFileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*ZipFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (*ZipFileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, 0, Separator)
}

func (*ZipFileSystem) VolumeName(filePath string) string {
	return ""
}

func (zipfs *ZipFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (zipfs *ZipFileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

// func (zipfs *ZipFileSystem) findFile(filePath string) *zip.File {
// 	filePath = strings.TrimPrefix(filePath, Separator)
// 	for _, zipFile := range zipfs.zipReader.File {
// 		if zipFile.Name == filePath {
// 			return zipFile
// 		}
// 	}
// 	return nil
// }

// func (zipfs *ZipFileSystem) findDir(filePath string) bool {
// 	filePath = strings.TrimPrefix(filePath, Separator)
// 	if !strings.HasSuffix(filePath, Separator) {
// 		filePath += Separator
// 	}
// 	for _, zipFile := range zipfs.zipReader.File {
// 		if strings.HasPrefix(zipFile.Name, filePath) {
// 			return true
// 		}
// 	}
// 	return false
// }

func (zipfs *ZipFileSystem) findFile(filePath string) (zipFile *zip.File, isDir bool) {
	filePath = strings.TrimPrefix(filePath, Separator)
	for _, zipFile := range zipfs.zipReader.File {
		if zipFile.Name == filePath {
			return zipFile, false
		}
	}
	if !strings.HasSuffix(filePath, Separator) {
		filePath += Separator
	}
	for _, zipFile := range zipfs.zipReader.File {
		if strings.HasPrefix(zipFile.Name, filePath) {
			return zipFile, true
		}
	}
	return nil, false
}

func (zipfs *ZipFileSystem) stat(filePath string, zipFile *zip.File, isDir bool) (os.FileInfo, error) {
	if zipFile == nil {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}

	name := path.Base(filePath)
	size := int64(zipFile.UncompressedSize64)
	if isDir {
		size = 0
	}
	info := &fs.FileInfo{
		Name:        name,
		Exists:      true,
		IsDir:       isDir,
		IsRegular:   true,
		IsHidden:    len(name) > 0 && name[0] == '.',
		Size:        size,
		ModTime:     zipFile.ModTime(),
		Permissions: fs.AllRead,
	}
	return info.OSFileInfo(), nil
}

func (zipfs *ZipFileSystem) Stat(filePath string) (os.FileInfo, error) {
	if zipfs.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}
	zipFile, isDir := zipfs.findFile(filePath)
	return zipfs.stat(filePath, zipFile, isDir)
}

func (zipfs *ZipFileSystem) Exists(filePath string) bool {
	if zipfs.zipReader == nil {
		return false
	}
	zipFile, _ := zipfs.findFile(filePath)
	return zipFile != nil
}

func (zipfs *ZipFileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (zipfs *ZipFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (zipfs *ZipFileSystem) IsEmpty(filePath string) bool {
	if zipfs.zipReader == nil {
		return true
	}
	panic("not implemented")
}

func (*ZipFileSystem) ListDir(dirPath string, listDirs bool, patterns []string, onDirEntry func(fs.DirEntry) error) error {
	return fs.ErrNotSupported
}

func (zipfs *ZipFileSystem) ListDirRecursive(dirPath string, listDirs bool, patterns []string, onDirEntry func(dir string, entry fs.DirEntry) error) error {
	if zipfs.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}

	// Files within the Zip have paths without leading slash,
	// so remove it for comparing paths
	dirPath = strings.TrimPrefix(dirPath, "/")

	for _, file := range zipfs.zipReader.File {
		if file.Name == "" || !strings.HasPrefix(file.Name, dirPath) {
			continue
		}

		isDir := file.Name[len(file.Name)-1] == '/'
		if isDir && !listDirs {
			continue
		}

		var (
			dir      = "/"
			filename = file.Name
		)
		if isDir {
			filename = filename[:len(filename)-1]
		}
		if sl := strings.LastIndexByte(file.Name, '/'); sl != -1 {
			dir += file.Name[:sl]
			filename = file.Name[sl+1:]
		}
		// Ignore non sane filenames just to be safe
		if filename == "" || filename == "." || filename == ".." {
			continue
		}

		match, err := zipfs.MatchAnyPattern(filename, patterns)
		if err != nil {
			return fmt.Errorf("ZipFileSystem.ListDirRecursive(%q): error matching name pattern: %w", dirPath, err)
		}
		if !match {
			continue
		}

		err = onDirEntry(dir, dirEntry{&file.FileHeader, filename})
		if err != nil {
			return err
		}
	}
	return nil
}

// dirEntry implements io/fs.DirEntry and io/fs.FileInfo
type dirEntry struct {
	header *zip.FileHeader
	name   string
}

func (e dirEntry) Name() string                 { return e.name }
func (e dirEntry) IsDir() bool                  { return e.header.Name[len(e.header.Name)-1] == '/' }
func (e dirEntry) Type() iofs.FileMode          { return e.Type() }
func (e dirEntry) Info() (iofs.FileInfo, error) { return e, nil }
func (e dirEntry) Size() int64                  { return int64(e.header.UncompressedSize64) }
func (e dirEntry) Mode() iofs.FileMode          { return e.header.Mode() }
func (e dirEntry) ModTime() time.Time           { return e.header.ModTime() }
func (e dirEntry) Sys() interface{}             { return e.header }

func (*ZipFileSystem) User(filePath string) string {
	return ""
}

func (*ZipFileSystem) Group(filePath string) string {
	return ""
}

func (zipfs *ZipFileSystem) ReadAll(filePath string) ([]byte, error) {
	file, err := zipfs.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return io.ReadAll(file)
}

func (zipfs *ZipFileSystem) OpenReader(filePath string) (iofs.File, error) {
	if zipfs.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}

	zipFile, isDir := zipfs.findFile(filePath)
	if isDir {
		return nil, fs.NewErrIsDirectory(zipfs.File(filePath))
	}
	if zipFile == nil {
		return nil, fs.NewErrDoesNotExist(zipfs.File(filePath))
	}
	f, err := zipFile.Open()
	if err != nil {
		return nil, err
	}
	return f.(iofs.File), nil
}

func (zipfs *ZipFileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return nil
}

func (zipfs *ZipFileSystem) SetUser(filePath string, user string) error {
	return nil
}

func (zipfs *ZipFileSystem) SetGroup(filePath string, group string) error {
	return nil
}

func (zipfs *ZipFileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if zipfs.zipWriter == nil {
		return fs.ErrReadOnlyFileSystem
	}
	filePath = strings.TrimPrefix(filePath, Separator)
	_, err := zipfs.zipWriter.Create(filePath)
	return err
}

func (zipfs *ZipFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	return nil
}

func (zipfs *ZipFileSystem) WriteAll(filePath string, data []byte, perm []fs.Permissions) error {
	writer, err := zipfs.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()

	_, err = writer.Write(data)
	return err
}

func (zipfs *ZipFileSystem) Append(filePath string, data []byte, perm []fs.Permissions) error {
	writer, err := zipfs.OpenAppendWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()

	_, err = writer.Write(data)
	return err
}

func (zipfs *ZipFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	if zipfs.zipWriter == nil {
		return nil, fs.ErrReadOnlyFileSystem
	}

	filePath = strings.TrimPrefix(filePath, Separator)

	writer, err := zipfs.zipWriter.Create(filePath)
	if err != nil {
		return nil, err
	}

	return nopCloser{writer}, nil
}

func (zipfs *ZipFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	return zipfs.OpenWriter(filePath, perm)
}

func (zipfs *ZipFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if zipfs.zipWriter == nil {
		return nil, fs.ErrReadOnlyFileSystem
	}

	panic("TODO buffered impl")

	// return nil, fs.ErrReadOnlyFileSystem
}

func (zipfs *ZipFileSystem) Watch(filePath string) (<-chan fs.WatchEvent, error) {
	return nil, fmt.Errorf("ZipFileSystem: %w", fs.ErrNotSupported)
}

func (zipfs *ZipFileSystem) Truncate(filePath string, size int64) error {
	return errors.New("ZipFileSystem.Truncate not supported")
}

func (zipfs *ZipFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return errors.New("ZipFileSystem.CopyFile not supported")
}

func (zipfs *ZipFileSystem) Rename(filePath string, newName string) error {
	return errors.New("ZipFileSystem.Rename not supported")
}

func (zipfs *ZipFileSystem) Move(filePath string, destPath string) error {
	return errors.New("ZipFileSystem.Move not supported")
}

func (zipfs *ZipFileSystem) Remove(filePath string) error {
	return errors.New("ZipFileSystem.Remove not supported")
}

type nopCloser struct {
	io.Writer
}

func (w nopCloser) Close() error {
	return nil
}
