package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/cloudsoda/go-smb2"

	"github.com/ungerik/go-fs"
)

// file is an open SMB file handle with the io semantics of os.File:
// ReadAt returns io.EOF when it reads less than len(b), Seek rejects a
// resulting negative offset, and after Close of the file system the
// methods return errors wrapping fs.ErrFileSystemClosed.
type file struct {
	*smb2.File
	fsys *fileSystem
}

var (
	_ fs.ReadAtSeekCloser    = new(file)
	_ fs.ReadWriteSeekCloser = new(file)
)

// err maps the context error of a handle whose file system was closed.
func (f *file) err(err error) error {
	if err != nil && f.fsys.closed.Load() && errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", fs.ErrFileSystemClosed, err)
	}
	return err
}

func (f *file) Read(b []byte) (int, error) {
	n, err := f.File.Read(b)
	if err == io.EOF {
		return n, err
	}
	return n, f.err(err)
}

// ReadAt reads len(b) bytes at off and returns io.EOF
// if it reached the end of the file before.
func (f *file) ReadAt(b []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(b, off)
	if err == nil && n < len(b) {
		return n, io.EOF
	}
	if err == io.EOF {
		return n, err
	}
	return n, f.err(err)
}

func (f *file) Write(b []byte) (int, error) {
	n, err := f.File.Write(b)
	return n, f.err(err)
}

func (f *file) WriteAt(b []byte, off int64) (int, error) {
	n, err := f.File.WriteAt(b, off)
	return n, f.err(err)
}

// Seek sets the offset for the next Read or Write.
// A resulting negative offset is an error like for os.File.
func (f *file) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		current, err := f.File.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, f.err(err)
		}
		base = current
	case io.SeekEnd:
		info, err := f.File.Stat()
		if err != nil {
			return 0, f.err(err)
		}
		base = info.Size()
	default:
		return 0, &os.PathError{Op: "seek", Path: f.Name(), Err: os.ErrInvalid}
	}
	if base+offset < 0 {
		return 0, &os.PathError{Op: "seek", Path: f.Name(), Err: os.ErrInvalid}
	}
	pos, err := f.File.Seek(base+offset, io.SeekStart)
	return pos, f.err(err)
}

// Close closes the handle. After Close of the file system,
// whose session is gone with all its handles, it returns nil.
func (f *file) Close() error {
	err := f.File.Close()
	if err != nil && f.fsys.closed.Load() {
		return nil
	}
	return err
}
