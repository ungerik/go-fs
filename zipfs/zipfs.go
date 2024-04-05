package zipfs

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"strings"

	"github.com/ungerik/go-fs"
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
	prefix    string
	closer    io.Closer // will be nil after Close()
	zipReader *zip.Reader
	zipWriter *zip.Writer
}

func NewReaderFileSystem(file fs.FileReader) (zipfs *ZipFileSystem, err error) {
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

func (f *ZipFileSystem) ReadableWritable() (readable, writable bool) {
	return f.zipReader != nil, f.zipWriter != nil
}

func (f *ZipFileSystem) RootDir() fs.File {
	return fs.File(f.prefix + Separator)
}

func (f *ZipFileSystem) ID() (string, error) {
	return f.prefix, nil
}

// Prefix for the ZipFileSystem
func (f *ZipFileSystem) Prefix() string {
	return f.prefix
}

func (f *ZipFileSystem) Name() string {
	return "Zip reader filesystem " + path.Base(f.prefix)
}

// String implements the fmt.Stringer interface.
func (f *ZipFileSystem) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

func (f *ZipFileSystem) File(filePath string) fs.File {
	return f.JoinCleanFile(filePath)
}

func (f *ZipFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

func (f *ZipFileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *ZipFileSystem) CleanPathFromURI(uri string) string {
	return path.Clean(strings.TrimPrefix(uri, f.prefix))
}

func (f *ZipFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, f.prefix, Separator)
}

func (f *ZipFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.prefix, Separator)
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

func (*ZipFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (f *ZipFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (f *ZipFileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

// func (f *ZipFileSystem) findFile(filePath string) *zip.File {
// 	filePath = strings.TrimPrefix(filePath, Separator)
// 	for _, zipFile := range f.zipReader.File {
// 		if zipFile.Name == filePath {
// 			return zipFile
// 		}
// 	}
// 	return nil
// }

// func (f *ZipFileSystem) findDir(filePath string) bool {
// 	filePath = strings.TrimPrefix(filePath, Separator)
// 	if !strings.HasSuffix(filePath, Separator) {
// 		filePath += Separator
// 	}
// 	for _, zipFile := range f.zipReader.File {
// 		if strings.HasPrefix(zipFile.Name, filePath) {
// 			return true
// 		}
// 	}
// 	return false
// }

func (f *ZipFileSystem) findFile(filePath string) (zipFile *zip.File, isDir bool) {
	if f.closer == nil {
		return nil, false
	}
	filePath = strings.TrimPrefix(filePath, Separator)
	for _, zipFile := range f.zipReader.File {
		if zipFile.Name == filePath {
			return zipFile, false
		}
	}
	if !strings.HasSuffix(filePath, Separator) {
		filePath += Separator
	}
	for _, zipFile := range f.zipReader.File {
		if strings.HasPrefix(zipFile.Name, filePath) {
			return zipFile, true
		}
	}
	return nil, false
}

func (f *ZipFileSystem) stat(filePath string, zipFile *zip.File, isDir bool) (iofs.FileInfo, error) {
	if zipFile == nil {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
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
		Modified:    zipFile.Modified,
		Permissions: fs.AllRead,
	}
	return info.StdFileInfo(), nil
}

func (f *ZipFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	if f.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}
	zipFile, isDir := f.findFile(filePath)
	return f.stat(filePath, zipFile, isDir)
}

func (f *ZipFileSystem) Exists(filePath string) bool {
	if f.zipReader == nil {
		return false
	}
	zipFile, _ := f.findFile(filePath)
	return zipFile != nil
}

func (f *ZipFileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (f *ZipFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (f *ZipFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	if f.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	panic("TODO")
}

func (f *ZipFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	rootNode := &node{
		FileInfo: &fs.FileInfo{
			IsDir: true,
		},
		children: make(map[string]*node),
	}
	for _, file := range f.zipReader.File {
		currentDir := rootNode
		parts := strings.Split(file.Name, Separator)
		lastIndex := len(parts) - 1
		for i := 0; i < lastIndex; i++ {
			currentDir = currentDir.addChildDir(parts[i], file.Modified)
		}
		currentDir.addChildFile(parts[lastIndex], file.Modified, int64(file.UncompressedSize64))
	}

	if dirPath != "" && dirPath != "." && dirPath != Separator {
		// parts := sipfs.SplitPath(dirPath)
		panic("TODO set rootNode to dirPath")
	}

	if !rootNode.IsDir {
		return fs.NewErrIsNotDirectory(f.File(dirPath))
	}

	var listRecursive func(parent *node) error
	listRecursive = func(parent *node) error {
		for _, child := range parent.sortedChildren() {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			err := callback(child.FileInfo)
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

func (f *ZipFileSystem) OpenReader(filePath string) (iofs.File, error) {
	if f.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}
	if f.closer == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	zipFile, isDir := f.findFile(filePath)
	if isDir {
		return nil, fs.NewErrIsDirectory(f.File(filePath))
	}
	if zipFile == nil {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}
	file, err := zipFile.Open()
	if err != nil {
		return nil, err
	}
	return file.(iofs.File), nil
}

func (f *ZipFileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if f.zipWriter == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	filePath = strings.TrimPrefix(filePath, Separator)
	_, err := f.zipWriter.Create(filePath)
	return err
}

func (f *ZipFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	return nil
}

func (f *ZipFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if f.zipWriter == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}
	if f.closer == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	filePath = strings.TrimPrefix(filePath, Separator)

	writer, err := f.zipWriter.Create(filePath)
	if err != nil {
		return nil, err
	}

	return nopCloser{writer}, nil
}

func (f *ZipFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if f.zipWriter == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}
	if f.closer == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	panic("TODO buffered impl")

	// return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
}

func (f *ZipFileSystem) Remove(filePath string) error {
	return fs.NewErrUnsupported(f, "Remove")
}

func (f *ZipFileSystem) Close() error {
	if f.closer == nil {
		return nil // already closed
	}
	fs.Unregister(f)
	err := f.closer.Close()
	f.closer = nil
	return err
}

type nopCloser struct {
	io.Writer
}

func (w nopCloser) Close() error {
	return nil
}
