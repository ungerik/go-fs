// Package zipfs implements read-only and write-only file systems
// for ZIP archives.
//
// NewReader opens an existing archive for reading; it is a
// fs.StdFileSystem over the io/fs.FS of archive/zip.Reader.
// NewWriter creates a new archive that receives files
// until it is closed. Because archive/zip writes entries
// sequentially, only one file of a writer file system can be
// open for writing at a time.
package zipfs

import (
	"archive/zip"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of zipfs URIs, followed by a random id
	Prefix = "zip://"

	// Separator used in zipfs paths
	Separator = "/"
)

var (
	_ fs.FileSystem      = new(Reader)
	_ fs.FileSystem      = new(Writer)
	_ fs.WriteFileSystem = new(Writer)
	_ fs.TouchFileSystem = new(Writer)
)

///////////////////////////////////////////////////////////////////////////////
// Reader

// Reader is a read-only file system for a ZIP archive.
// It is a fs.StdFileSystem over the io/fs.FS of archive/zip.Reader,
// which synthesizes the directories implied by the entry names.
type Reader struct {
	*fs.StdFileSystem

	closer io.Closer
}

// NewReader opens a ZIP archive for reading
// and registers the file system.
func NewReader(file fs.FileReader) (*Reader, error) {
	fileReader, err := file.OpenReadSeeker()
	if err != nil {
		return nil, err
	}
	zipReader, err := zip.NewReader(fileReader, file.Size())
	if err != nil {
		return nil, errors.Join(err, fileReader.Close())
	}
	id := fsimpl.RandomString()
	zipfs := &Reader{
		StdFileSystem: fs.NewStdFileSystemWithPrefix(zipReader, Prefix+id, "Zip reader filesystem "+id),
		closer:        fileReader,
	}
	fs.Register(zipfs)
	return zipfs, nil
}

// Close unregisters the file system and closes the archive file.
func (f *Reader) Close() error {
	if f.closer == nil {
		return nil
	}
	closer := f.closer
	f.closer = nil
	return errors.Join(f.StdFileSystem.Close(), closer.Close())
}

///////////////////////////////////////////////////////////////////////////////
// Writer

// Writer is a write-only file system that writes a ZIP archive.
type Writer struct {
	fsimpl.PathHelper

	// mtx guards the fields below. archive/zip.Writer is not safe for
	// concurrent use and only allows writing to the most recently
	// created entry.
	mtx          sync.Mutex
	zipWriter    *zip.Writer
	fileWriter   io.WriteCloser // nil after Close
	activeWriter *zipEntryWriter
}

// NewWriter creates a ZIP archive for writing
// and registers the file system.
func NewWriter(file fs.File) (*Writer, error) {
	fileWriter, err := file.OpenWriter()
	if err != nil {
		return nil, err
	}
	zipWriter := zip.NewWriter(fileWriter)
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	zipfs := &Writer{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + fsimpl.RandomString(), Rooted: true},
		zipWriter:  zipWriter,
		fileWriter: fileWriter,
	}
	fs.Register(zipfs)
	return zipfs, nil
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
	return "Zip writer filesystem " + path.Base(f.URIPrefix)
}

func (f *Writer) String() string {
	return f.Name() + " with prefix " + f.Prefix()
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

// checkNoOpenWriterLocked returns an error if a previous OpenWriter entry is
// still open. archive/zip only allows writing to the most recently created
// entry, so a new entry must not be created until the previous writer is
// closed. f.mtx must be held.
func (f *Writer) checkNoOpenWriterLocked() error {
	if f.activeWriter != nil && !f.activeWriter.closed {
		return fmt.Errorf("%s: previous zip entry writer must be closed before opening another (zip entries are written sequentially)", f.Name())
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

// Touch writes an empty entry.
func (f *Writer) Touch(filePath string, perm fs.Permissions) error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if err := f.checkClosedLocked(); err != nil {
		return err
	}
	if err := f.checkNoOpenWriterLocked(); err != nil {
		return err
	}
	// Create writes the (empty) entry header; it is finalized when the next
	// entry is created or the archive is closed. No writer is handed out.
	_, err := f.zipWriter.Create(strings.TrimPrefix(filePath, Separator))
	return err
}

// MakeDir is a no-op: ZIP directories are implicit and created
// from the path of any file written below them.
func (f *Writer) MakeDir(dirPath string, perm fs.Permissions) error {
	return f.checkClosed()
}

// OpenWriter creates an entry and returns its writer,
// which must be closed before the next entry is created.
func (f *Writer) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if err := f.checkClosedLocked(); err != nil {
		return nil, err
	}
	if err := f.checkNoOpenWriterLocked(); err != nil {
		return nil, err
	}
	writer, err := f.zipWriter.Create(strings.TrimPrefix(filePath, Separator))
	if err != nil {
		return nil, err
	}
	w := &zipEntryWriter{zipfs: f, w: writer}
	f.activeWriter = w
	return w, nil
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
func (f *Writer) Close() error {
	f.mtx.Lock()
	if f.fileWriter == nil {
		f.mtx.Unlock()
		return nil // already closed
	}
	fileWriter := f.fileWriter
	f.fileWriter = nil
	f.activeWriter = nil
	f.mtx.Unlock()

	fs.Unregister(f)
	// Close of the zip.Writer finalizes the central directory and flushes
	// any pending entry; closing the file releases the handle (required
	// to remove the file on Windows).
	return errors.Join(f.zipWriter.Close(), fileWriter.Close())
}

// zipEntryWriter is the io.WriteCloser returned by OpenWriter. An
// archive/zip.Writer only allows writing to the most recently created entry,
// and that entry is finalized when the next entry is created or the archive is
// closed. This wrapper enforces the contract: writes fail once the entry has
// been closed or superseded by a newer OpenWriter or Touch call, turning
// what used to be silent archive corruption into a clear error.
type zipEntryWriter struct {
	zipfs  *Writer
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
