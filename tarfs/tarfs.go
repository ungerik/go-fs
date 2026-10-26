// Package tarfs implements read-only and write-only file systems
// for tar archives, optionally gzip compressed (.tar.gz, .tgz).
//
// NewReader indexes an existing archive for reading,
// NewWriter creates a new archive that receives files
// until it is closed. The package mirrors zipfs: both use
// the same rules for entry names, duplicates and permissions,
// only zipfs converts entry names that are not UTF-8 and its writer
// rejects the names that are not valid in a zip archive.
//
// Reading:
//   - Entry names are made valid paths inside the archive like
//     archive/zip does for its io/fs view: backslashes become slashes
//     and ".." elements that would leave the root are removed, so
//     `..\..\evil.txt` is read as "evil.txt". No entry can address
//     a path outside of the archive when it is extracted.
//   - A later entry replaces an earlier one with the same path, like
//     extracting the archive would, also when one of them is a file and
//     the other a directory. Directories that are only implied by the
//     paths of other entries exist without an entry of their own.
//   - Regular files and directories are read. A hard link is read as a
//     regular file with the content of the earlier entry it links to.
//     Symbolic links, devices and FIFOs are not exposed.
//   - The permissions of an entry are read from its header, entries
//     without permission bits get DefaultPermissions or DefaultDirPermissions.
//
// Writing:
//   - A path can be written only once: writing a file or directory that
//     already exists in the archive, explicitly or as directory implied by
//     another entry, returns an error wrapping os.ErrExist. Writing below a
//     file returns fs.ErrIsNotDirectory, writing a file over a directory
//     fs.ErrIsDirectory. That way a written archive never has duplicate or
//     conflicting entries.
//   - Paths with a backslash are rejected, so archives stay portable.
//   - Entries get the current time and the passed permissions,
//     or DefaultPermissions and DefaultDirPermissions if none are passed.
package tarfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"
	"sync"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/internal/dirtree"
)

const (
	// Prefix is the URI prefix of tar file systems, followed by the id.
	Prefix = "tar://"
	// Separator is the path separator of tar file systems.
	Separator = "/"

	// DefaultPermissions of files written to an archive
	// and of read entries without permission bits.
	DefaultPermissions = fs.UserAndGroupReadWrite | fs.AllRead

	// DefaultDirPermissions of directories written to an archive
	// and of read directories without permission bits.
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllRead | fs.AllExecute
)

var (
	// Compile-time interface checks
	_ fs.FileSystem                 = new(Reader)
	_ fs.ExistsFileSystem           = new(Reader)
	_ fs.ReadAllFileSystem          = new(Reader)
	_ fs.ListDirRecursiveFileSystem = new(Reader)
	_ fs.FileSystem                 = new(Writer)
	_ fs.WriteFileSystem            = new(Writer)
	_ fs.WriteAllFileSystem         = new(Writer)
	_ fs.TouchFileSystem            = new(Writer)
	_ fs.MakeAllDirsFileSystem      = new(Writer)
)

// gzipMagic are the first bytes of a gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

// isGzipName reports whether the file name has a gzip extension.
func isGzipName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".tgz")
}

///////////////////////////////////////////////////////////////////////////////
// Reader

// Reader is a read-only file system for a tar archive.
type Reader struct {
	paths fsimpl.PathHelper

	mtx     sync.Mutex
	closed  bool
	archive readAtSeeker
	closer  io.Closer
	tree    *dirtree.Tree
}

// readAtSeeker is an archive that is scanned sequentially
// once and then read at the offsets of the entries.
type readAtSeeker interface {
	io.ReadSeeker
	io.ReaderAt
}

// entry is the dirtree.Node.Sys of a file or directory entry.
type entry struct {
	header *tar.Header
	offset int64 // of the data in the archive
}

// Prefix returns the URI prefix of the file system.
func (f *Reader) Prefix() string {
	return f.paths.URIPrefix
}

// Separator returns the path separator of the file system.
func (f *Reader) Separator() string {
	return f.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (f *Reader) CleanPath(uriParts ...string) string {
	return f.paths.CleanPath(uriParts...)
}

// NewReader opens a tar archive for reading with the URI prefix
// "tar://" + id, a random id is used if id is empty.
// The file system is not registered, pass it to fs.Register
// to access its files via their URIs and Close it after use.
//
// The archive is scanned once to index its entries; the content
// of a file is read from the archive on demand. A gzip compressed
// archive is recognized by its content, independent of the file name,
// and decompressed into memory, because gzip streams can't be read
// at an offset. See the package documentation for how entries are read.
func NewReader(file fs.FileReader, id string) (*Reader, error) {
	if id == "" {
		id = fsimpl.RandomString()
	}
	reader, err := file.OpenReadSeeker()
	if err != nil {
		return nil, err
	}
	var (
		archive readAtSeeker = reader
		closer  io.Closer    = reader
	)
	compressed, err := hasGzipMagic(reader)
	if err != nil {
		return nil, errors.Join(err, reader.Close())
	}
	if compressed {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.Join(err, reader.Close())
		}
		data, err := io.ReadAll(gz)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("tarfs: decompressing %s: %w", file.Name(), err), reader.Close())
		}
		err = reader.Close()
		if err != nil {
			return nil, err
		}
		archive, closer = bytes.NewReader(data), io.NopCloser(nil)
	}
	tarfs := &Reader{
		paths:   fsimpl.PathHelper{URIPrefix: Prefix + id, Rooted: true},
		tree:    dirtree.New(),
		archive: archive,
		closer:  closer,
	}
	err = tarfs.index()
	if err != nil {
		return nil, errors.Join(err, closer.Close())
	}
	return tarfs, nil
}

// hasGzipMagic reports whether r starts with the gzip magic bytes
// and seeks r back to the start.
func hasGzipMagic(r io.ReadSeeker) (bool, error) {
	magic := make([]byte, len(gzipMagic))
	n, err := io.ReadFull(r, magic)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	_, err = r.Seek(0, io.SeekStart)
	if err != nil {
		return false, err
	}
	return bytes.Equal(magic[:n], gzipMagic), nil
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
//
// Add only fails for unusable entries, like a file named after
// the root, which are skipped, so that one bad entry doesn't
// make the whole archive unreadable.
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
		name := dirtree.CleanName(header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			_, _ = f.tree.Add(name, true, 0, header.ModTime, &entry{header: header})
		case tar.TypeReg:
			_, _ = f.tree.Add(name, false, header.Size, header.ModTime, &entry{header: header, offset: tracked.offset})
		case tar.TypeLink:
			// A hard link is the same file as the earlier entry it links to
			if target := f.tree.Lookup(dirtree.CleanName(header.Linkname)); target != nil && !target.IsDir {
				_, _ = f.tree.Add(name, false, target.Size, target.Modified, target.Sys)
			}
		}
		// Symbolic links, devices and other entries are not exposed
	}
}

// ReadableWritable returns true for readable and false for writable,
// because an archive that is being read can't be written to.
func (f *Reader) ReadableWritable() (readable, writable bool) {
	return true, false
}

// RootDir returns the root directory of the archive.
func (f *Reader) RootDir() fs.File {
	return fs.File(f.paths.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system,
// which is unique per opened archive.
func (f *Reader) ID() string {
	return f.paths.URIPrefix
}

// Name returns "Tar reader filesystem".
func (f *Reader) Name() string {
	return "Tar reader filesystem"
}

// String returns the name of the file system and its prefix.
func (f *Reader) String() string {
	return f.Name() + " with prefix " + f.paths.URIPrefix
}

func (f *Reader) file(filePath string) fs.File {
	return fs.File(f.paths.JoinCleanURI(filePath))
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
// Its Sys is the *tar.Header of the entry, or nil for implied directories.
func (f *Reader) nodeInfo(node *dirtree.Node) *fs.FileInfo {
	var (
		perm   fs.Permissions
		header *tar.Header
	)
	if e, ok := node.Sys.(*entry); ok {
		header = e.header
		perm = fs.PermissionsFromFileMode(header.FileInfo().Mode())
	}
	if node.IsDir {
		perm = perm.OrDefault(DefaultDirPermissions)
	} else {
		perm = perm.OrDefault(DefaultPermissions)
	}
	if node.Path == "" {
		return &fs.FileInfo{
			File:        f.RootDir(),
			Name:        Separator,
			Exists:      true,
			IsDir:       true,
			Modified:    node.Modified,
			Permissions: perm,
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
		Permissions: perm,
	}
	if header != nil {
		info.Sys = header
	}
	return info
}

// lookup returns the tree node of a path or an error wrapping os.ErrNotExist.
func (f *Reader) lookup(filePath string) (*dirtree.Node, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	node := f.tree.Lookup(filePath)
	if node == nil {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	return node, nil
}

// lookupFile returns the tree node of a file or an error
// wrapping os.ErrNotExist or fs.ErrIsDirectory.
func (f *Reader) lookupFile(filePath string) (*dirtree.Node, *entry, error) {
	node, err := f.lookup(filePath)
	if err != nil {
		return nil, nil, err
	}
	e, ok := node.Sys.(*entry)
	if node.IsDir || !ok {
		return nil, nil, fs.NewErrIsDirectory(f.file(filePath))
	}
	return node, e, nil
}

// Stat returns the FileInfo of an entry of the archive.
func (f *Reader) Stat(filePath string) (*fs.FileInfo, error) {
	node, err := f.lookup(filePath)
	if err != nil {
		return nil, err
	}
	return f.nodeInfo(node), nil
}

// Exists reports if the archive contains the path.
func (f *Reader) Exists(filePath string) (bool, error) {
	if err := f.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	return f.tree.Lookup(filePath) != nil, nil
}

// ListDir calls the callback for every entry in the directory that
// matches any of the patterns, or for all entries if no patterns are passed.
func (f *Reader) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive lists all files (not directories) below dirPath.
func (f *Reader) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, true)
}

func (f *Reader) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error, recursive bool) error {
	dir, err := f.lookup(dirPath)
	if err != nil {
		return err
	}
	if !dir.IsDir {
		return fs.NewErrIsNotDirectory(f.file(dirPath))
	}
	return dirtree.List(ctx, dir, patterns, recursive, func(node *dirtree.Node) error {
		return callback(f.nodeInfo(node))
	})
}

// OpenReader returns a reader for the content of a file entry, which
// reads from the archive on demand. The reader implements io/fs.File,
// io.ReaderAt and io.Seeker.
func (f *Reader) OpenReader(filePath string) (io.ReadCloser, error) {
	node, e, err := f.lookupFile(filePath)
	if err != nil {
		return nil, err
	}
	return &entryReader{
		SectionReader: io.NewSectionReader(f.archive, e.offset, node.Size),
		info:          f.nodeInfo(node).StdFileInfo(),
	}, nil
}

// ReadAll reads the content of a file entry.
func (f *Reader) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	node, e, err := f.lookupFile(filePath)
	if err != nil {
		return nil, err
	}
	data := make([]byte, node.Size)
	n, err := f.archive.ReadAt(data, e.offset)
	if n == len(data) {
		err = nil // io.EOF at the end of the archive
	}
	if err != nil {
		return nil, fmt.Errorf("tarfs: reading %s: %w", f.file(filePath), err)
	}
	return data, nil
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

// entryReader reads the data of an entry from the archive.
type entryReader struct {
	*io.SectionReader
	info   iofs.FileInfo
	closed bool
}

// Stat returns the FileInfo of the entry, implementing io/fs.File.
func (r *entryReader) Stat() (iofs.FileInfo, error) {
	return r.info, nil
}

// Read reads the data of the entry.
func (r *entryReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, iofs.ErrClosed
	}
	return r.SectionReader.Read(p)
}

// ReadAt reads the data of the entry at an offset.
func (r *entryReader) ReadAt(p []byte, off int64) (int, error) {
	if r.closed {
		return 0, iofs.ErrClosed
	}
	return r.SectionReader.ReadAt(p, off)
}

// Close marks the reader as closed, the archive stays open.
func (r *entryReader) Close() error {
	r.closed = true
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Writer

// Writer is a write-only file system that writes a tar archive.
// See the package documentation for the rules of written entries.
type Writer struct {
	paths fsimpl.PathHelper

	mtx        sync.Mutex
	closed     bool
	tree       *dirtree.Tree // the entries written so far
	tarWriter  *tar.Writer
	gzipWriter *gzip.Writer // nil for an uncompressed archive
	fileWriter io.WriteCloser
}

// Prefix returns the URI prefix of the file system.
func (f *Writer) Prefix() string {
	return f.paths.URIPrefix
}

// Separator returns the path separator of the file system.
func (f *Writer) Separator() string {
	return f.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (f *Writer) CleanPath(uriParts ...string) string {
	return f.paths.CleanPath(uriParts...)
}

// NewWriter creates a tar archive for writing with the URI prefix
// "tar://" + id, a random id is used if id is empty.
// The file system is not registered, pass it to fs.Register
// to access its files via their URIs. Close it to finish the archive.
//
// The archive is gzip compressed if the file name has a .gz or .tgz
// extension. Files are buffered in memory until their writer is closed,
// because tar needs the size before the content.
func NewWriter(file fs.File, id string) (*Writer, error) {
	if id == "" {
		id = fsimpl.RandomString()
	}
	fileWriter, err := file.OpenWriter()
	if err != nil {
		return nil, err
	}
	tarfs := &Writer{
		paths:      fsimpl.PathHelper{URIPrefix: Prefix + id, Rooted: true},
		tree:       dirtree.New(),
		fileWriter: fileWriter,
	}
	var w io.Writer = fileWriter
	if isGzipName(file.Name()) {
		tarfs.gzipWriter = gzip.NewWriter(fileWriter)
		w = tarfs.gzipWriter
	}
	tarfs.tarWriter = tar.NewWriter(w)
	return tarfs, nil
}

// ReadableWritable returns false for readable and true for writable,
// because an archive that is being written can't be read.
func (f *Writer) ReadableWritable() (readable, writable bool) {
	return false, true
}

// RootDir returns the root directory of the archive.
func (f *Writer) RootDir() fs.File {
	return fs.File(f.paths.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system,
// which is unique per created archive.
func (f *Writer) ID() string {
	return f.paths.URIPrefix
}

// Name returns "Tar writer filesystem".
func (f *Writer) Name() string {
	return "Tar writer filesystem"
}

// String returns the name of the file system and its prefix.
func (f *Writer) String() string {
	return f.Name() + " with prefix " + f.paths.URIPrefix
}

func (f *Writer) file(filePath string) fs.File {
	return fs.File(f.paths.JoinCleanURI(filePath))
}

func (f *Writer) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.checkClosedLocked()
}

func (f *Writer) checkClosedLocked() error {
	if f.closed {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// checkNewLocked returns an error if an entry at filePath can't
// be added to the archive, see the package documentation.
// f.mtx must be held.
func (f *Writer) checkNewLocked(filePath string, isDir bool) error {
	if err := f.checkClosedLocked(); err != nil {
		return err
	}
	node, isAncestor, err := f.tree.Conflict(filePath)
	switch {
	case err != nil:
		return fmt.Errorf("tarfs: %w", err)
	case node == nil:
		return nil
	case isAncestor:
		return fs.NewErrIsNotDirectory(f.file(node.Path))
	case node.IsDir && !isDir:
		return fs.NewErrIsDirectory(f.file(filePath))
	default:
		return fs.NewErrAlreadyExists(f.file(filePath))
	}
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

// writeFile writes a complete file entry.
func (f *Writer) writeFile(filePath string, data []byte, perm fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if err := f.checkNewLocked(filePath, false); err != nil {
		return err
	}
	now := time.Now()
	err := f.tarWriter.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     dirtree.CleanName(filePath),
		Size:     int64(len(data)),
		Mode:     int64(perm.OrDefault(DefaultPermissions)),
		ModTime:  now,
	})
	if err != nil {
		return err
	}
	_, err = f.tarWriter.Write(data)
	if err != nil {
		return err
	}
	_, err = f.tree.Add(filePath, false, int64(len(data)), now, nil)
	return err
}

// writeDirLocked writes a directory entry. f.mtx must be held.
// dirPath must be validated with checkNewLocked or dirtree.Elements,
// then dirtree.CleanName returns its elements joined with slashes.
func (f *Writer) writeDirLocked(dirPath string, perm fs.Permissions) error {
	now := time.Now()
	err := f.tarWriter.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     dirtree.CleanName(dirPath) + Separator,
		Mode:     int64(perm.OrDefault(DefaultDirPermissions)),
		ModTime:  now,
	})
	if err != nil {
		return err
	}
	_, err = f.tree.Add(dirPath, true, 0, now, nil)
	return err
}

// WriteAll writes a file entry to the archive.
func (f *Writer) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.writeFile(filePath, data, perm)
}

// OpenWriter returns a writer that buffers the file in memory
// and writes it as archive entry on Close.
// The writer implements fs.WriteAborter, Abort discards
// the buffered file without writing an entry.
func (f *Writer) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	f.mtx.Lock()
	err := f.checkNewLocked(filePath, false)
	f.mtx.Unlock()
	if err != nil {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.writeFile(filePath, data, perm)
	}), nil
}

// Touch writes an empty file entry.
// It returns an error wrapping os.ErrExist if the file was already written.
func (f *Writer) Touch(filePath string, perm fs.Permissions) error {
	return f.writeFile(filePath, nil, perm)
}

// MakeDir writes a directory entry.
// It returns an error wrapping os.ErrExist if the directory
// was already written or is implied by a written entry.
func (f *Writer) MakeDir(dirPath string, perm fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if err := f.checkNewLocked(dirPath, true); err != nil {
		return err
	}
	return f.writeDirLocked(dirPath, perm)
}

// MakeAllDirs writes a directory entry for dirPath and each of its
// parent directories that doesn't exist yet in the archive.
// Existing directories are no error.
func (f *Writer) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	elements, err := dirtree.Elements(dirPath)
	if err != nil {
		return fmt.Errorf("tarfs: %w", err)
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if err := f.checkClosedLocked(); err != nil {
		return err
	}
	for i := range elements {
		dir := strings.Join(elements[:i+1], Separator)
		node := f.tree.Lookup(dir)
		switch {
		case node == nil:
			if err := f.writeDirLocked(dir, perm); err != nil {
				return err
			}
		case !node.IsDir:
			return fs.NewErrIsNotDirectory(f.file(dir))
		}
	}
	return nil
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
