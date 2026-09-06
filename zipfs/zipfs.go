package zipfs

import (
	"archive/zip"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

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
	fsimpl.PathHelper

	closer    io.Closer // will be nil after Close()
	zipReader *zip.Reader
	zipWriter *zip.Writer

	// mtx guards closer and activeWriter for writer-mode archives.
	// archive/zip.Writer is not safe for concurrent use and only allows
	// writing to the most recently created entry.
	mtx          sync.Mutex
	activeWriter *zipEntryWriter
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
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		closer:     fileReader,
		zipReader:  zipReader,
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
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	zipfs = &ZipFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		closer:     zipWriter,
		zipWriter:  zipWriter,
	}
	fs.Register(zipfs)
	return zipfs, err
}

func (f *ZipFileSystem) ReadableWritable() (readable, writable bool) {
	return f.zipReader != nil, f.zipWriter != nil
}

func (f *ZipFileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *ZipFileSystem) ID() string {
	return f.URIPrefix
}

func (f *ZipFileSystem) Name() string {
	if f.zipWriter != nil {
		return "Zip writer filesystem " + path.Base(f.URIPrefix)
	}
	return "Zip reader filesystem " + path.Base(f.URIPrefix)
}

// String implements the fmt.Stringer interface.
func (f *ZipFileSystem) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

func (f *ZipFileSystem) File(filePath string) fs.File {
	return f.JoinCleanFile(filePath)
}

func (f *ZipFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.JoinCleanURI(uriParts...))
}

// checkClosed returns an fs.ErrFileSystemClosed error if the archive was closed.
func (f *ZipFileSystem) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	return nil
}

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

func (f *ZipFileSystem) stat(filePath string, zipFile *zip.File, isDir bool) (*fs.FileInfo, error) {
	if zipFile == nil {
		return nil, fs.NewErrDoesNotExist(f.File(filePath))
	}

	name := path.Base(filePath)
	size := int64(zipFile.UncompressedSize64) //#nosec G115 -- int64 limit will not be exceeded in real world use cases
	if isDir {
		size = 0
	}
	return &fs.FileInfo{
		File:        f.File(filePath),
		Name:        name,
		Exists:      true,
		IsDir:       isDir,
		IsRegular:   !isDir,
		IsHidden:    len(name) > 0 && name[0] == '.',
		Size:        size,
		Modified:    zipFile.Modified,
		Permissions: fs.AllRead,
	}, nil
}

func (f *ZipFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if f.zipReader == nil {
		return nil, fs.ErrWriteOnlyFileSystem
	}
	zipFile, isDir := f.findFile(filePath)
	return f.stat(filePath, zipFile, isDir)
}

func (f *ZipFileSystem) Exists(filePath string) (bool, error) {
	if f.zipReader == nil {
		return false, nil
	}
	zipFile, _ := f.findFile(filePath)
	return zipFile != nil, nil
}

func (f *ZipFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}

	// Normalize directory path
	dirPath = strings.TrimPrefix(dirPath, Separator)
	if dirPath != "" && !strings.HasSuffix(dirPath, Separator) {
		dirPath += Separator
	}

	// Track seen entries to avoid duplicates
	seen := make(map[string]bool)

	for _, zipFile := range f.zipReader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Check if file is in the requested directory
		if !strings.HasPrefix(zipFile.Name, dirPath) {
			continue
		}

		// Get relative path from dirPath
		relativePath := strings.TrimPrefix(zipFile.Name, dirPath)
		if relativePath == "" {
			continue
		}

		// Check if this is a direct child (no more separators)
		parts := strings.Split(relativePath, Separator)
		name := parts[0]
		if name == "" {
			continue
		}

		// Skip if already seen
		if seen[name] {
			continue
		}
		seen[name] = true

		// Determine if it's a directory or file
		isDir := len(parts) > 1 || strings.HasSuffix(zipFile.Name, Separator)

		// Check pattern match
		matched, err := f.MatchAnyPattern(name, patterns)
		if err != nil {
			return err
		}
		if !matched {
			continue
		}

		// Create FileInfo
		size := int64(zipFile.UncompressedSize64) //#nosec G115 -- int64 limit will not be exceeded in real world use cases
		if isDir {
			size = 0
		}
		info := &fs.FileInfo{
			File:        f.JoinCleanFile(dirPath, name),
			Name:        name,
			Exists:      true,
			IsDir:       isDir,
			IsRegular:   !isDir,
			IsHidden:    len(name) > 0 && name[0] == '.',
			Size:        size,
			Modified:    zipFile.Modified,
			Permissions: fs.AllRead,
		}

		err = callback(info)
		if err != nil {
			return err
		}
	}

	return nil
}

func (f *ZipFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.zipReader == nil {
		return fs.ErrWriteOnlyFileSystem
	}
	if err := f.checkClosed(); err != nil {
		return err
	}

	root := newDirTreeRoot()
	for _, file := range f.zipReader.File {
		err := root.add(file.Name, file.Modified, int64(file.UncompressedSize64)) //#nosec G115 -- int64 limit will not be exceeded in real world use cases
		if err != nil {
			return err
		}
	}

	dir := root.lookup(dirPath)
	if dir == nil {
		return fs.NewErrDoesNotExist(f.File(dirPath))
	}
	if !dir.isDir {
		return fs.NewErrIsNotDirectory(f.File(dirPath))
	}

	var listFiles func(parent *dirTreeNode) error
	listFiles = func(parent *dirTreeNode) error {
		for _, child := range parent.sortedChildren() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if child.isDir {
				err := listFiles(child)
				if err != nil {
					return err
				}
				continue
			}
			matched, err := f.MatchAnyPattern(child.name, patterns)
			if err != nil {
				return err
			}
			if !matched {
				continue
			}
			err = callback(&fs.FileInfo{
				File:        f.JoinCleanFile(child.path),
				Name:        child.name,
				Exists:      true,
				IsRegular:   true,
				IsHidden:    strings.HasPrefix(child.name, "."),
				Size:        child.size,
				Modified:    child.modified,
				Permissions: fs.AllRead,
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	return listFiles(dir)
}

func (f *ZipFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
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
	return zipFile.Open()
}

func (f *ZipFileSystem) Touch(filePath string, perm fs.Permissions) error {
	if f.zipWriter == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}

	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	if err := f.checkNoOpenWriterLocked(); err != nil {
		return err
	}

	filePath = strings.TrimPrefix(filePath, Separator)
	// Create writes the (empty) entry header; it is finalized when the next
	// entry is created or the archive is closed. No writer is handed out.
	_, err := f.zipWriter.Create(filePath)
	return err
}

// MakeDir is a no-op for writer-mode archives: ZIP directories are implicit
// and created automatically from the path of any file written below them.
// It returns an error for read-only or closed archives.
func (f *ZipFileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if f.zipWriter == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}
	if f.closer == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	return nil
}

func (f *ZipFileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if f.zipWriter == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrReadOnlyFileSystem)
	}

	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closer == nil {
		return nil, fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	if err := f.checkNoOpenWriterLocked(); err != nil {
		return nil, err
	}

	filePath = strings.TrimPrefix(filePath, Separator)

	writer, err := f.zipWriter.Create(filePath)
	if err != nil {
		return nil, err
	}

	w := &zipEntryWriter{zipfs: f, w: writer}
	f.activeWriter = w
	return w, nil
}

// checkNoOpenWriterLocked returns an error if a previous OpenWriter entry is
// still open. archive/zip only allows writing to the most recently created
// entry, so a new entry must not be created until the previous writer is
// closed. f.mtx must be held.
func (f *ZipFileSystem) checkNoOpenWriterLocked() error {
	if f.activeWriter != nil && !f.activeWriter.closed {
		return fmt.Errorf("%s: previous zip entry writer must be closed before opening another (zip entries are written sequentially)", f.Name())
	}
	return nil
}

// Remove is not possible for ZIP archives: entries can't be removed
// from an archive that is read, nor from one that is being written.
func (f *ZipFileSystem) Remove(filePath string) error {
	if f.zipReader != nil {
		return fmt.Errorf("%s: %w (entries can't be removed from a ZIP archive)", f.Name(), fs.ErrReadOnlyFileSystem)
	}
	return fs.NewErrUnsupported(f, "Remove")
}

func (f *ZipFileSystem) Close() error {
	f.mtx.Lock()
	if f.closer == nil {
		f.mtx.Unlock()
		return nil // already closed
	}
	closer := f.closer
	f.closer = nil
	f.activeWriter = nil
	f.mtx.Unlock()

	fs.Unregister(f)
	// For writer-mode archives closer is the zip.Writer, whose Close finalizes
	// the central directory and flushes any pending entry.
	return closer.Close()
}

// zipEntryWriter is the io.WriteCloser returned by OpenWriter. An
// archive/zip.Writer only allows writing to the most recently created entry,
// and that entry is finalized when the next entry is created or the archive is
// closed. This wrapper enforces the contract: writes fail once the entry has
// been closed or superseded by a newer OpenWriter/Touch/MakeDir call, turning
// what used to be silent archive corruption into a clear error.
type zipEntryWriter struct {
	zipfs  *ZipFileSystem
	w      io.Writer
	closed bool
}

func (e *zipEntryWriter) Write(p []byte) (int, error) {
	e.zipfs.mtx.Lock()
	defer e.zipfs.mtx.Unlock()
	if e.closed {
		return 0, fmt.Errorf("%s: write to closed zip entry writer", e.zipfs.Name())
	}
	if e.zipfs.activeWriter != e {
		return 0, fmt.Errorf("%s: write to superseded zip entry writer (zip entries must be written and closed sequentially)", e.zipfs.Name())
	}
	return e.w.Write(p)
}

func (e *zipEntryWriter) Close() error {
	e.zipfs.mtx.Lock()
	defer e.zipfs.mtx.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.zipfs.activeWriter == e {
		e.zipfs.activeWriter = nil
	}
	return nil
}
