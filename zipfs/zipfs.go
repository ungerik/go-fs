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
	return info.StdFileInfo(), nil
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

func (zipfs *ZipFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	if zipfs.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}

	panic("not implemented")
}

func (zipfs *ZipFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if zipfs.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}

	rootNode := &node{
		FileInfo: fs.FileInfo{
			IsDir: true,
		},
		children: make(map[string]*node),
	}
	for _, file := range zipfs.zipReader.File {
		currentDir := rootNode
		parts := strings.Split(file.Name, Separator)
		lastIndex := len(parts) - 1
		for i := 0; i < lastIndex; i++ {
			currentDir = currentDir.addChildDir(parts[i], file.ModTime())
		}
		currentDir.addChildFile(parts[lastIndex], file.ModTime(), int64(file.UncompressedSize64))
	}

	if dirPath != "" && dirPath != "." && dirPath != Separator {
		// parts := sipfs.SplitPath(dirPath)
		panic("TODO set rootNode to dirPath")
	}

	if !rootNode.IsDir {
		return fs.NewErrIsNotDirectory(zipfs.File(dirPath))
	}

	var listRecursive func(parent *node) error
	listRecursive = func(parent *node) error {
		for _, child := range parent.sortedChildren() {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			file := zipfs.File(child.filePath)
			err := callback(file, child.FileInfo)
			if err != nil {
				return err
			}
			if child.IsDir {
				err = listRecursive(child)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}

	return listRecursive(rootNode)
}

func (zipfs *ZipFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []fs.File, err error) {
	if zipfs.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}

	return fs.ListDirMaxImpl(ctx, max, func(ctx context.Context, callback func(fs.File) error) error {
		return zipfs.ListDirInfo(ctx, dirPath, fs.FileCallback(callback).FileInfoCallback, patterns)
	})
}

func (*ZipFileSystem) User(filePath string) string {
	return ""
}

func (*ZipFileSystem) Group(filePath string) string {
	return ""
}

func (zipfs *ZipFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	file, err := zipfs.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return fs.ReadAllContext(ctx, file)
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

func (zipfs *ZipFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	writer, err := zipfs.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()

	_, err = writer.Write(data)
	return err
}

func (zipfs *ZipFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
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

func (*ZipFileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	return nil, fmt.Errorf("%w: ZipFileSystem.Watch", errors.ErrUnsupported)
}

func (*ZipFileSystem) Truncate(filePath string, size int64) error {
	return fmt.Errorf("%w: ZipFileSystem.Truncate", errors.ErrUnsupported)
}

func (*ZipFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return fmt.Errorf("%w: ZipFileSystem.CopyFile", errors.ErrUnsupported)
}

func (*ZipFileSystem) Rename(filePath string, newName string) error {
	return fmt.Errorf("%w: ZipFileSystem.Rename", errors.ErrUnsupported)
}

func (*ZipFileSystem) Move(filePath string, destPath string) error {
	return fmt.Errorf("%w: ZipFileSystem.Move", errors.ErrUnsupported)
}

func (*ZipFileSystem) Remove(filePath string) error {
	return fmt.Errorf("%w: ZipFileSystem.Remove", errors.ErrUnsupported)
}

type nopCloser struct {
	io.Writer
}

func (w nopCloser) Close() error {
	return nil
}
