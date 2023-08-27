package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
func CopyFile(ctx context.Context, src FileReader, dest File, perm ...Permissions) error {
	var buf []byte
	return CopyFileBuf(ctx, src, dest, &buf, perm...)
}

// CopyFileBuf copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// An pointer to a []byte variable must be passed for buf.
// If that variable holds a non zero length byte slice then this slice will be used as buffer,
// else a byte slice will be allocated and assigned to the variable.
// Use this function to re-use buffers between CopyFileBuf calls.
func CopyFileBuf(ctx context.Context, src FileReader, dest File, buf *[]byte, perm ...Permissions) error {
	if buf == nil {
		panic("CopyFileBuf: buf is nil") // not a file system error
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Handle directories
	if dest.IsDir() {
		dest = dest.Join(src.Name())
	} else {
		err := dest.Dir().MakeDir()
		if err != nil {
			return fmt.Errorf("CopyFileBuf: can't make directory %q: %w", dest.Dir(), err)
		}
	}

	switch f := src.(type) {
	case File:
		// Use same file system copy if possible
		if fs := f.FileSystem(); fs == dest.FileSystem() {
			if copyFS, ok := fs.(CopyFileSystem); ok {
				return copyFS.CopyFile(ctx, f.Path(), dest.Path(), buf)
			}
		}
		// Else use at least same permissions
		if len(perm) == 0 {
			perm = []Permissions{f.Permissions()}
		}
	case MemFile:
		// Don't use io.CopyBuffer in case of MemFile
		return dest.WriteAllContext(ctx, f.FileData, perm...)
	}

	r, err := src.OpenReader()
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open src reader: %w", err)
	}
	defer r.Close()

	w, err := dest.OpenWriter(perm...)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open dest writer: %w", err)
	}
	defer w.Close()

	if len(*buf) == 0 {
		*buf = make([]byte, copyBufferSize)
	}
	err = copyBuffer(ctx, w, r, *buf)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: error from io.CopyBuffer: %w", err)
	}
	return nil
}

func copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (err error) {
	for err = ctx.Err(); err == nil; {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errors.New("invalid write result")
				}
			}
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return nil
			}
			return er
		}
	}
	return err
}

// CopyRecursive can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursive(ctx context.Context, src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(ctx, src, dest, patterns, &buf)
}

func copyRecursive(ctx context.Context, src, dest File, patterns []string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !src.IsDir() {
		// Just copy one file
		return CopyFileBuf(ctx, src, dest, buf)
	}

	if dest.Exists() && !dest.IsDir() {
		return fmt.Errorf("Can't copy a directory (%s) over a file (%s)", src.URL(), dest.URL())
	}

	// TODO better check
	if !dest.Exists() {
		err := dest.MakeDir()
		if err != nil {
			return fmt.Errorf("copyRecursive: can't make dest dir %q: %w", dest, err)
		}
	}

	// Copy directories recursive
	return src.ListDirContext(ctx, func(file File) error {
		return copyRecursive(ctx, file, dest.Join(file.Name()), patterns, buf)
	}, patterns...)
}
