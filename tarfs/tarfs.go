// Package tarfs implements read-only and write-only file systems
// for tar archives, optionally gzip compressed (.tar.gz, .tgz).
//
// NewReader indexes an existing archive for reading,
// NewWriter creates a new archive that receives files
// until it is closed. The package mirrors zipfs.
package tarfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	Prefix    = "tar://"
	Separator = "/"
)

var (
	// DefaultPermissions of files written to an archive
	DefaultPermissions = fs.UserAndGroupReadWrite | fs.AllRead

	// DefaultDirPermissions of directories written to an archive
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllRead | fs.AllExecute

	// Compile-time interface checks
	_ fs.FileSystem                 = new(Reader)
	_ fs.ExistsFileSystem           = new(Reader)
	_ fs.ListDirRecursiveFileSystem = new(Reader)
	_ fs.FileSystem                 = new(Writer)
	_ fs.WriteFileSystem            = new(Writer)
	_ fs.WriteAllFileSystem         = new(Writer)
	_ fs.TouchFileSystem            = new(Writer)
)

// isGzip reports whether the file name has a gzip extension.
func isGzip(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".tgz")
}

// entryName returns the archive entry name of a file system path.
func entryName(filePath string) string {
	return strings.Trim(path.Clean(filePath), Separator)
}

///////////////////////////////////////////////////////////////////////////////
// Reader

// Reader is a read-only file system for a tar archive.
type Reader struct {
	fsimpl.PathHelper

	mtx     sync.Mutex
	closed  bool
	archive io.ReadSeeker
	closer  io.Closer
	tree    *fsimpl.DirTreeNode
	offsets map[string]int64 // data offset of file entries by tree path
}

// NewReader opens a tar archive for reading and registers
// the file system. The archive is scanned once to index its entries;
// the content of a file is read from the archive on demand.
// A gzip compressed archive (.tar.gz, .tgz) is decompressed into
// memory, because gzip streams can't be read at an offset.
func NewReader(file fs.FileReader) (*Reader, error) {
	reader, err := file.OpenReadSeeker()
	if err != nil {
		return nil, err
	}
	var (
		archive io.ReadSeeker = reader
		closer  io.Closer     = reader
	)
	if isGzip(file.Name()) {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.Join(err, reader.Close())
		}
		data, err := io.ReadAll(gz)
		if err != nil {
			return nil, errors.Join(err, reader.Close())
		}
		err = reader.Close()
		if err != nil {
			return nil, err
		}
		archive, closer = bytes.NewReader(data), io.NopCloser(nil)
	}
	tarfs := &Reader{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		archive:    archive,
		closer:     closer,
		tree:       fsimpl.NewDirTree(),
		offsets:    make(map[string]int64),
	}
	err = tarfs.index()
	if err != nil {
		return nil, errors.Join(err, closer.Close())
	}
	fs.Register(tarfs)
	return tarfs, nil
}

// offsetReadSeeker tracks the position of a read seeker.
type offsetReadSeeker struct {
	io.ReadSeeker
	offset int64
}

func (r *offsetReadSeeker) Read(p []byte) (int, error) {
	n, err := r.ReadSeeker.Read(p)
	r.offset += int64(n)
	return n, err
}

func (r *offsetReadSeeker) Seek(offset int64, whence int) (int64, error) {
	pos, err := r.ReadSeeker.Seek(offset, whence)
	if err == nil {
		r.offset = pos
	}
	return pos, err
}

// index scans the archive and builds the directory tree.
// After tar.Reader.Next returned a header, the archive
// position is the offset of the entry's data.
func (f *Reader) index() error {
	_, err := f.archive.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	tracked := &offsetReadSeeker{ReadSeeker: f.archive}
	tr := tar.NewReader(tracked)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tarfs: reading archive: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, Separator))
		switch header.Typeflag {
		case tar.TypeDir:
			err = f.tree.Add(name+Separator, header.ModTime, 0)
		case tar.TypeReg:
			err = f.tree.Add(name, header.ModTime, header.Size)
			f.offsets[name] = tracked.offset
		default:
			continue // links, devices and other entries are not exposed
		}
		if err != nil {
			return err
		}
	}
}

func (f *Reader) ReadableWritable() (readable, writable bool) {
	return true, false
}

func (f *Reader) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *Reader) ID() string {
	return f.URIPrefix
}

func (f *Reader) Name() string {
	return "Tar reader filesystem"
}

func (f *Reader) String() string {
	return f.Name() + " with prefix " + f.URIPrefix
}

func (f *Reader) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}

func (f *Reader) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// nodeInfo returns the FileInfo of a tree node.
func (f *Reader) nodeInfo(node *fsimpl.DirTreeNode) *fs.FileInfo {
	if node.Path == "" {
		return &fs.FileInfo{
			File:        f.RootDir(),
			Name:        Separator,
			Exists:      true,
			IsDir:       true,
			Permissions: DefaultDirPermissions,
		}
	}
	info := &fs.FileInfo{
		File:        f.file(node.Path),
		Name:        node.Name,
		Exists:      true,
		IsDir:       node.IsDir,
		IsRegular:   !node.IsDir,
		IsHidden:    strings.HasPrefix(node.Name, "."),
		Size:        node.Size,
		Modified:    node.Modified,
		Permissions: DefaultPermissions,
	}
	if node.IsDir {
		info.Permissions = DefaultDirPermissions
	}
	return info
}

// lookup returns the tree node of a path or an error wrapping os.ErrNotExist.
func (f *Reader) lookup(filePath string) (*fsimpl.DirTreeNode, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	node := f.tree.Lookup(entryName(filePath))
	if node == nil {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	return node, nil
}

func (f *Reader) Stat(filePath string) (*fs.FileInfo, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	node, err := f.lookup(filePath)
	if err != nil {
		return nil, err
	}
	return f.nodeInfo(node), nil
}

func (f *Reader) Exists(filePath string) (bool, error) {
	if err := f.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	return f.tree.Lookup(entryName(filePath)) != nil, nil
}

func (f *Reader) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive lists all files (not directories) below dirPath.
func (f *Reader) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, true)
}

func (f *Reader) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error, recursive bool) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	dir, err := f.lookup(dirPath)
	if err != nil {
		return err
	}
	if !dir.IsDir {
		return fs.NewErrIsNotDirectory(f.file(dirPath))
	}
	var list func(parent *fsimpl.DirTreeNode) error
	list = func(parent *fsimpl.DirTreeNode) error {
		for _, child := range parent.SortedChildren() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if recursive && child.IsDir {
				if err := list(child); err != nil {
					return err
				}
				continue
			}
			match, err := fsimpl.MatchAnyPattern(child.Name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if err := callback(f.nodeInfo(child)); err != nil {
				return err
			}
		}
		return nil
	}
	return list(dir)
}

// OpenReader reads the file content from the archive into memory
// and returns a reader that implements io/fs.File.
func (f *Reader) OpenReader(filePath string) (io.ReadCloser, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	node, err := f.lookup(filePath)
	if err != nil {
		return nil, err
	}
	if node.IsDir {
		return nil, fs.NewErrIsDirectory(f.file(filePath))
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil, fs.ErrFileSystemClosed
	}
	_, err = f.archive.Seek(f.offsets[node.Path], io.SeekStart)
	if err != nil {
		return nil, err
	}
	data := make([]byte, node.Size)
	_, err = io.ReadFull(f.archive, data)
	if err != nil {
		return nil, fmt.Errorf("tarfs: reading %s: %w", f.file(filePath), err)
	}
	return fsimpl.NewReadonlyFileBuffer(data, f.nodeInfo(node).StdFileInfo()), nil
}

// Close closes the archive file and unregisters
// the file system. Close is idempotent.
func (f *Reader) Close() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	fs.Unregister(f)
	return f.closer.Close()
}

///////////////////////////////////////////////////////////////////////////////
// Writer

// Writer is a write-only file system that writes a tar archive.
type Writer struct {
	fsimpl.PathHelper

	mtx        sync.Mutex
	closed     bool
	tarWriter  *tar.Writer
	gzipWriter *gzip.Writer // nil for an uncompressed archive
	fileWriter io.WriteCloser
}

// NewWriter creates a tar archive for writing and registers
// the file system. The archive is gzip compressed if the file name has
// a .gz or .tgz extension. Files are buffered in memory until their
// writer is closed, because tar needs the size before the content.
func NewWriter(file fs.File) (*Writer, error) {
	fileWriter, err := file.OpenWriter()
	if err != nil {
		return nil, err
	}
	tarfs := &Writer{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		fileWriter: fileWriter,
	}
	var w io.Writer = fileWriter
	if isGzip(file.Name()) {
		tarfs.gzipWriter = gzip.NewWriter(fileWriter)
		w = tarfs.gzipWriter
	}
	tarfs.tarWriter = tar.NewWriter(w)
	fs.Register(tarfs)
	return tarfs, nil
}

func (f *Writer) ReadableWritable() (readable, writable bool) {
	return false, true
}

func (f *Writer) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *Writer) ID() string {
	return f.URIPrefix
}

func (f *Writer) Name() string {
	return "Tar writer filesystem"
}

func (f *Writer) String() string {
	return f.Name() + " with prefix " + f.URIPrefix
}

func (f *Writer) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// Stat is not possible for an archive that is being written.
func (f *Writer) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	return nil, fs.ErrWriteOnlyFileSystem
}

// ListDir is not possible for an archive that is being written.
func (f *Writer) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	return fs.ErrWriteOnlyFileSystem
}

// OpenReader is not possible for an archive that is being written.
func (f *Writer) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	return nil, fs.ErrWriteOnlyFileSystem
}

// writeEntry writes a complete archive entry.
func (f *Writer) writeEntry(header *tar.Header, data []byte) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fs.ErrFileSystemClosed
	}
	err := f.tarWriter.WriteHeader(header)
	if err != nil {
		return err
	}
	_, err = f.tarWriter.Write(data)
	return err
}

func fileHeader(filePath string, size int64, perm fs.Permissions) *tar.Header {
	return &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     entryName(filePath),
		Size:     size,
		Mode:     int64(perm.OrDefault(DefaultPermissions)),
		ModTime:  time.Now(),
	}
}

// WriteAll writes a file entry to the archive.
func (f *Writer) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.writeEntry(fileHeader(filePath, int64(len(data)), perm), data)
}

// OpenWriter returns a writer that buffers the file in memory
// and writes it as archive entry on Close.
func (f *Writer) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.writeEntry(fileHeader(filePath, int64(len(data)), perm), data)
	}), nil
}

// Touch writes an empty file entry.
func (f *Writer) Touch(filePath string, perm fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	return f.writeEntry(fileHeader(filePath, 0, perm), nil)
}

// MakeDir writes a directory entry.
func (f *Writer) MakeDir(dirPath string, perm fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if err := f.checkClosed(); err != nil {
		return err
	}
	name := entryName(dirPath)
	if name == "" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	return f.writeEntry(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name + Separator,
		Mode:     int64(perm.OrDefault(DefaultDirPermissions)),
		ModTime:  time.Now(),
	}, nil)
}

// Remove is not possible: entries can't be removed from an archive that is being written.
func (f *Writer) Remove(filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	return fs.NewErrUnsupported(f, "Remove")
}

// Close finishes the archive, closes the underlying file
// and unregisters the file system. Close is idempotent.
func (f *Writer) Close() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	fs.Unregister(f)
	err := f.tarWriter.Close()
	if f.gzipWriter != nil {
		err = errors.Join(err, f.gzipWriter.Close())
	}
	return errors.Join(err, f.fileWriter.Close())
}
