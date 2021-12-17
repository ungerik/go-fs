package fs

import (
	"context"
	"fmt"
	"io"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
func CopyFile(src FileReader, dest File, perm ...Permissions) error {
	var buf []byte
	return CopyFileBuf(src, dest, &buf, perm...)
}

// CopyFileBuf copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// An non nil pointer to a []byte variable must be passed for buf.
// If that variable holds a non zero length byte slice, then this slice will be used as buffer,
// else a byte slice will be allocated and assigned to the variable.
// Use this function to re-use buffers between CopyFileBuf calls.
func CopyFileBuf(src FileReader, dest File, buf *[]byte, perm ...Permissions) error {
	return CopyFileBufContext(context.Background(), src, dest, buf, perm...)
}

// CopyFileBufContext copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// An non nil pointer to a []byte variable must be passed for buf.
// If that variable holds a non zero length byte slice, then this slice will be used as buffer,
// else a byte slice will be allocated and assigned to the variable.
// Use this function to re-use buffers between CopyFileBufContext calls.
func CopyFileBufContext(ctx context.Context, src FileReader, dest File, buf *[]byte, perm ...Permissions) error {
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

	srcFile, srcIsFile := src.(File)
	if srcIsFile {
		// Use same file system copy if possible
		fs := srcFile.FileSystem()
		if fs == dest.FileSystem() {
			return fs.CopyFile(ctx, srcFile.Path(), dest.Path(), buf)
		}
	} else if srcMemFile, ok := src.(*MemFile); ok {
		// Don't use io.CopyBuffer in case of MemFile
		return dest.WriteAll(srcMemFile.FileData, perm...)
	}

	r, err := src.OpenReader()
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open src reader: %w", err)
	}
	defer r.Close()

	if len(perm) == 0 && srcIsFile {
		perm = []Permissions{srcFile.Permissions()}
	}
	w, err := dest.OpenWriter(perm...)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open dest writer: %w", err)
	}
	defer w.Close()

	if len(*buf) == 0 {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: error from io.CopyBuffer: %w", err)
	}
	return nil
}

// CopyRecursive can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursive(src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(context.Background(), src, dest, patterns, &buf)
}

// CopyRecursiveContext can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursiveContext(ctx context.Context, src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(ctx, src, dest, patterns, &buf)
}

func copyRecursive(ctx context.Context, src, dest File, patterns []string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !src.IsDir() {
		// Just copy one file
		return CopyFileBufContext(ctx, src, dest, buf)
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
