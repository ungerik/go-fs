// Package zipfs implements read-only and write-only file systems
// for ZIP archives.
//
// NewReader opens an existing archive for reading,
// NewWriter creates a new archive that receives files
// until it is closed. The package mirrors tarfs: both use
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
//   - A name without the UTF-8 flag of the zip format is taken from
//     its Info-ZIP Unicode Path extra field if it has one that only
//     differs in the characters that the stored name can't hold, its
//     ASCII parts must be the same, else a name that is not valid UTF-8
//     is converted to UTF-8 from CP-437, the encoding of names without
//     the flag. The *zip.FileHeader of FileInfo.Sys keeps the stored name.
//   - A later entry replaces an earlier one with the same path, like
//     extracting the archive would, also when one of them is a file and
//     the other a directory. Directories that are only implied by the
//     paths of other entries exist without an entry of their own.
//   - Regular files and directories are read.
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
//     So are cleaned paths that are not valid in a zip archive, see
//     fs.Zip: names that are not valid UTF-8, contain a control or
//     bidirectional formatting character or a colon, and names longer
//     than 65535 bytes. All of them are errors wrapping os.ErrInvalid.
//     Names that only Windows can't extract are not checked.
//     The Writer implements fs.ValidateNameFileSystem, so fs.CopyFile,
//     fs.CopyRecursive and fs.Move into the archive fail before they
//     write anything.
//   - Entries get the current time and the passed permissions,
//     or DefaultPermissions and DefaultDirPermissions if none are passed.
//   - A file is buffered in memory until its writer is closed, then it is
//     compressed into the archive. So several files can be written at the
//     same time, and Abort of the writer discards a file without an entry.
package zipfs

import (
	"archive/zip"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/internal/dirtree"
	"github.com/ungerik/go-fs/internal/zipname"
)

const (
	// Prefix of zipfs URIs, followed by the id
	Prefix = "zip://"

	// Separator used in zipfs paths
	Separator = "/"

	// DefaultPermissions of files written to an archive
	// and of read entries without permission bits.
	DefaultPermissions = fs.UserAndGroupReadWrite | fs.AllRead

	// DefaultDirPermissions of directories written to an archive
	// and of read directories without permission bits.
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllRead | fs.AllExecute
)

var (
	_ fs.FileSystem                 = new(Reader)
	_ fs.ExistsFileSystem           = new(Reader)
	_ fs.ReadAllFileSystem          = new(Reader)
	_ fs.ListDirRecursiveFileSystem = new(Reader)
	_ fs.FileSystem                 = new(Writer)
	_ fs.WriteFileSystem            = new(Writer)
	_ fs.TouchFileSystem            = new(Writer)
	_ fs.MakeAllDirsFileSystem      = new(Writer)
	_ fs.ValidateNameFileSystem     = new(Writer)
)

///////////////////////////////////////////////////////////////////////////////
// Reader

// Reader is a read-only file system for a ZIP archive.
// See the package documentation for how entries are read.
type Reader struct {
	paths fsimpl.PathHelper

	mtx    sync.Mutex
	closed bool
	closer io.Closer
	tree   *dirtree.Tree // the dirtree.Node.Sys of an entry is its *zip.File
}

// NewReader opens a ZIP archive for reading with the URI prefix
// "zip://" + id, a random id is used if id is empty.
// The file system is not registered, pass it to fs.Register
// to access its files via their URIs and Close it after use.
//
// The archive is opened with FileReader.OpenReadSeeker, so it is
// read in place if the file system of the archive supports random
// access, else it is read into memory. Entries are decompressed on demand.
func NewReader(file fs.FileReader, id string) (*Reader, error) {
	fileReader, err := file.OpenReadSeeker()
	if err != nil {
		return nil, err
	}
	size, err := fileReader.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, errors.Join(err, fileReader.Close())
	}
	zipReader, err := zip.NewReader(fileReader, size)
	if err != nil {
		return nil, errors.Join(err, fileReader.Close())
	}
	if id == "" {
		id = fsimpl.RandomString()
	}
	zipfs := &Reader{
		paths:  fsimpl.PathHelper{URIPrefix: Prefix + id, Rooted: true},
		tree:   dirtree.New(),
		closer: fileReader,
	}
	for _, zipFile := range zipReader.File {
		// Add only fails for unusable entries, like a file named after
		// the root, which are skipped, so that one bad entry doesn't
		// make the whole archive unreadable.
		name := zipname.Decode(&zipFile.FileHeader)
		isDir := strings.HasSuffix(name, "/")
		if !isDir && !zipFile.Mode().IsRegular() {
			// Symbolic links, devices and other entries are not exposed
			continue
		}
		size := int64(zipFile.UncompressedSize64) //#nosec G115 -- sizes beyond int64 can't be read anyway
		if isDir {
			size = 0
		}
		_, _ = zipfs.tree.Add(dirtree.CleanName(name), isDir, size, zipFile.Modified, zipFile)
	}
	return zipfs, nil
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

// Name returns "Zip reader filesystem" and the id of the archive.
func (f *Reader) Name() string {
	return "Zip reader filesystem " + path.Base(f.paths.URIPrefix)
}

// String returns the name of the file system and its prefix.
func (f *Reader) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

func (f *Reader) file(filePath string) fs.File {
	return fs.File(f.paths.JoinCleanURI(filePath))
}

func (f *Reader) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
	}
	return nil
}

// nodeInfo returns the FileInfo of a tree node.
// Its Sys is the *zip.FileHeader of the entry, or nil for implied directories.
func (f *Reader) nodeInfo(node *dirtree.Node) *fs.FileInfo {
	var perm fs.Permissions
	zipFile, _ := node.Sys.(*zip.File)
	if zipFile != nil {
		perm = fs.PermissionsFromFileMode(zipFile.Mode())
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
	if zipFile != nil {
		info.Sys = &zipFile.FileHeader
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

// OpenReader returns a reader that decompresses the content
// of a file entry on demand. The reader implements io/fs.File.
func (f *Reader) OpenReader(filePath string) (io.ReadCloser, error) {
	node, err := f.lookup(filePath)
	if err != nil {
		return nil, err
	}
	zipFile, _ := node.Sys.(*zip.File)
	if node.IsDir || zipFile == nil {
		return nil, fs.NewErrIsDirectory(f.file(filePath))
	}
	r, err := zipFile.Open()
	if err != nil {
		return nil, fmt.Errorf("zipfs: opening %s: %w", f.file(filePath), err)
	}
	return &entryReader{ReadCloser: r, info: f.nodeInfo(node).StdFileInfo()}, nil
}

// ReadAll decompresses the content of a file entry.
func (f *Reader) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := f.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	data, err := fs.ReadAll(ctx, r)
	return data, errors.Join(err, r.Close())
}

// Close unregisters the file system and closes the archive file.
// Close is idempotent.
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

// entryReader decompresses the content of an entry.
type entryReader struct {
	io.ReadCloser
	info iofs.FileInfo
}

// Stat returns the FileInfo of the entry, implementing io/fs.File.
func (r *entryReader) Stat() (iofs.FileInfo, error) {
	return r.info, nil
}

///////////////////////////////////////////////////////////////////////////////
// Writer

// Writer is a write-only file system that writes a ZIP archive.
// See the package documentation for the rules of written entries.
type Writer struct {
	paths fsimpl.PathHelper

	// mtx guards the fields below,
	// archive/zip.Writer is not safe for concurrent use.
	mtx        sync.Mutex
	tree       *dirtree.Tree // the entries written so far
	zipWriter  *zip.Writer
	fileWriter io.WriteCloser // nil after Close
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

// NewWriter creates a ZIP archive for writing with the URI prefix
// "zip://" + id, a random id is used if id is empty.
// The file system is not registered, pass it to fs.Register
// to access its files via their URIs. Close it to finish the archive.
func NewWriter(file fs.File, id string) (*Writer, error) {
	if id == "" {
		id = fsimpl.RandomString()
	}
	fileWriter, err := file.OpenWriter()
	if err != nil {
		return nil, err
	}
	zipWriter := zip.NewWriter(fileWriter)
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	zipfs := &Writer{
		paths:      fsimpl.PathHelper{URIPrefix: Prefix + id, Rooted: true},
		tree:       dirtree.New(),
		zipWriter:  zipWriter,
		fileWriter: fileWriter,
	}
	return zipfs, nil
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

// Name returns "Zip writer filesystem" and the id of the archive.
func (f *Writer) Name() string {
	return "Zip writer filesystem " + path.Base(f.paths.URIPrefix)
}

// String returns the name of the file system and its prefix.
func (f *Writer) String() string {
	return f.Name() + " with prefix " + f.Prefix()
}

func (f *Writer) file(filePath string) fs.File {
	return fs.File(f.paths.JoinCleanURI(filePath))
}

// checkClosed returns fs.ErrFileSystemClosed if the archive was closed.
func (f *Writer) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.checkClosedLocked()
}

func (f *Writer) checkClosedLocked() error {
	if f.fileWriter == nil {
		return fmt.Errorf("%s %w", f.Name(), fs.ErrFileSystemClosed)
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
	// archive/zip adds a name that is too long to the central
	// directory before it rejects it, so it must never get one
	if err := zipname.Check(dirtree.CleanName(filePath), isDir); err != nil {
		return fmt.Errorf("zipfs: %w", err)
	}
	node, isAncestor, err := f.tree.Conflict(filePath)
	switch {
	case err != nil:
		return fmt.Errorf("zipfs: %w", err)
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

// ValidateName returns an error wrapping os.ErrInvalid if name, the name
// of a directory entry, can't be written to the archive: if it contains
// a backslash or is not valid in a zip archive, see fs.Zip.
// fs.CopyFile, fs.CopyRecursive and fs.Move check the names they write
// with it, so they fail before they write anything, also for the parent
// directories of a new path, whose names MakeAllDirs checks first.
// The length of a whole path is checked when it is written.
func (f *Writer) ValidateName(name string) error {
	if _, err := dirtree.Elements(name); err != nil {
		return fmt.Errorf("zipfs: %w", err)
	}
	if err := zipname.Check(name, false); err != nil {
		return fmt.Errorf("zipfs: %w", err)
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
	header := &zip.FileHeader{
		Name:     dirtree.CleanName(filePath),
		Method:   zip.Deflate,
		Modified: time.Now(),
	}
	header.SetMode(perm.OrDefault(DefaultPermissions).FileMode(false))
	w, err := f.zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	if err != nil {
		return err
	}
	_, err = f.tree.Add(filePath, false, int64(len(data)), header.Modified, nil)
	return err
}

// writeDirLocked writes a directory entry. f.mtx must be held.
// dirPath must be validated with checkNewLocked, or with dirtree.Elements
// and zipname.Check like in MakeAllDirs, then dirtree.CleanName
// returns its elements joined with slashes.
func (f *Writer) writeDirLocked(dirPath string, perm fs.Permissions) error {
	header := &zip.FileHeader{
		Name:     dirtree.CleanName(dirPath) + Separator,
		Modified: time.Now(),
	}
	header.SetMode(perm.OrDefault(DefaultDirPermissions).FileMode(true))
	_, err := f.zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = f.tree.Add(dirPath, true, 0, header.Modified, nil)
	return err
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
		return fmt.Errorf("zipfs: %w", err)
	}
	// The parent directories are shorter and have no other characters
	err = zipname.Check(strings.Join(elements, Separator), true)
	if err != nil {
		return fmt.Errorf("zipfs: %w", err)
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

// Remove is not possible: entries can't be removed from an archive that is being written.
func (f *Writer) Remove(filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	return fs.NewErrUnsupported(f, "Remove")
}

// Close finalizes the archive, closes the underlying file
// and unregisters the file system. Close is idempotent.
// Files whose writers are still open are not written.
func (f *Writer) Close() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.fileWriter == nil {
		return nil // already closed
	}
	fileWriter := f.fileWriter
	f.fileWriter = nil

	fs.Unregister(f)
	// Close of the zip.Writer writes the central directory;
	// closing the file releases the handle (required
	// to remove the file on Windows).
	return errors.Join(f.zipWriter.Close(), fileWriter.Close())
}
