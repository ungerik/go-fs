package fstest

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	fs "github.com/ungerik/go-fs"
)

// largeContentSize is larger than 1 MiB and not a multiple
// of any power of two buffer size.
const largeContentSize = 1<<20 + 12345

// largeContent returns n bytes of a deterministic pseudo random
// sequence without short periods, so misplaced chunks are detected.
func largeContent(n int) []byte {
	data := make([]byte, n+8)
	x := uint64(0x9E3779B97F4A7C15)
	for i := 0; i < n; i += 8 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		binary.LittleEndian.PutUint64(data[i:], x)
	}
	return data[:n]
}

// testCanceled checks that every method with a context
// returns an error wrapping context.Canceled for a canceled context,
// and that the write methods don't change anything then.
func (c *conformance) testCanceled(t *testing.T) {
	sample := c.cfg.Seed.sampleFile()

	if !c.cfg.NoDirectories {
		c.run(t, "ListDir", func(t *testing.T) {
			err := c.fs.ListDir(c.canceledContext(), c.path(""), nil, func(*fs.FileInfo) error { return nil })
			checkErrorIs(t, err, context.Canceled, "ListDir with a canceled context")
		})
		c.run(t, "ListDirRecursive", func(t *testing.T) {
			ldrfs, ok := c.fs.(fs.ListDirRecursiveFileSystem)
			if !ok {
				t.Skipf("%T does not implement fs.ListDirRecursiveFileSystem", c.fs)
			}
			err := ldrfs.ListDirRecursive(c.canceledContext(), c.path(""), nil, func(*fs.FileInfo) error { return nil })
			checkErrorIs(t, err, context.Canceled, "ListDirRecursive with a canceled context")
		})
		c.run(t, "File.ListDirMax", func(t *testing.T) {
			_, err := c.file("").ListDirMax(c.canceledContext(), -1)
			checkErrorIs(t, err, context.Canceled, "File.ListDirMax with a canceled context")
		})
	}
	if sample != "" {
		c.run(t, "ReadAll", func(t *testing.T) {
			rafs, ok := c.fs.(fs.ReadAllFileSystem)
			if !ok {
				t.Skipf("%T does not implement fs.ReadAllFileSystem", c.fs)
			}
			_, err := rafs.ReadAll(c.canceledContext(), c.path(sample))
			checkErrorIs(t, err, context.Canceled, "ReadAll(%q) with a canceled context", sample)
		})
		c.run(t, "File.ReadAll", func(t *testing.T) {
			_, err := c.file(sample).ReadAll(c.canceledContext())
			checkErrorIs(t, err, context.Canceled, "File.ReadAll(%q) with a canceled context", sample)
		})
		c.run(t, "File.ContentHash", func(t *testing.T) {
			_, err := c.file(sample).ContentHash(c.canceledContext())
			checkErrorIs(t, err, context.Canceled, "File.ContentHash(%q) with a canceled context", sample)
		})
	}
	if !c.writable || !c.readable {
		return
	}

	// The write checks work on a file with the content "unchanged"
	// that a canceled operation must leave as it is
	const content = "unchanged"
	prepare := func(t *testing.T, name string) fs.File {
		t.Helper()
		c.put(t, "canceled/"+name, content)
		return c.wfile("canceled/" + name)
	}
	unchanged := func(t *testing.T, name, what string) {
		t.Helper()
		c.checkContent(t, "canceled/"+name, content, "%s with a canceled context must not change the file", what)
	}

	_, writeAll := c.fs.(fs.WriteAllFileSystem)
	_, appendFS := c.fs.(fs.AppendFileSystem)
	_, truncate := c.fs.(fs.TruncateFileSystem)
	_, removeAll := c.fs.(fs.RemoveAllFileSystem)
	_, copyFS := c.fs.(fs.CopyFileSystem)
	_, move := c.fs.(fs.MoveFileSystem)
	writeChecks := []struct {
		name        string
		iface       string // optional interface of the method
		implemented bool
		call        func(ctx context.Context, f fs.File) error
	}{
		{"WriteAll", "fs.WriteAllFileSystem", writeAll, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.WriteAllFileSystem).WriteAll(ctx, f.Path(), []byte("canceled"), 0)
		}},
		{"Append", "fs.AppendFileSystem", appendFS, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.AppendFileSystem).Append(ctx, f.Path(), []byte("canceled"), 0)
		}},
		{"Truncate", "fs.TruncateFileSystem", truncate, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.TruncateFileSystem).Truncate(ctx, f.Path(), 2)
		}},
		{"RemoveAll", "fs.RemoveAllFileSystem", removeAll, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.RemoveAllFileSystem).RemoveAll(ctx, f.Path())
		}},
		{"CopyFile", "fs.CopyFileSystem", copyFS, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.CopyFileSystem).CopyFile(ctx, f.Path(), f.Path()+"-copy")
		}},
		{"Move", "fs.MoveFileSystem", move, func(ctx context.Context, f fs.File) error {
			return c.fs.(fs.MoveFileSystem).Move(ctx, f.Path(), f.Path()+"-moved")
		}},
		{"File.WriteAll", "", true, func(ctx context.Context, f fs.File) error {
			return f.WriteAll(ctx, []byte("canceled"))
		}},
		{"File.Append", "", true, func(ctx context.Context, f fs.File) error {
			return f.Append(ctx, []byte("canceled"))
		}},
		{"File.Truncate", "", true, func(ctx context.Context, f fs.File) error {
			return f.Truncate(ctx, 2)
		}},
		{"File.RemoveRecursive", "", true, func(ctx context.Context, f fs.File) error {
			return f.RemoveRecursive(ctx)
		}},
		{"File.Rename", "", true, func(ctx context.Context, f fs.File) error {
			_, err := f.Rename(ctx, f.Name()+"-renamed")
			return err
		}},
		{"File.MoveTo", "", true, func(ctx context.Context, f fs.File) error {
			return f.MoveTo(ctx, f.Dir().Join(f.Name()+"-moved-to"))
		}},
		{"fs.CopyFile", "", true, func(ctx context.Context, f fs.File) error {
			return fs.CopyFile(ctx, f, f.Dir().Join(f.Name()+"-copied"))
		}},
	}
	for _, check := range writeChecks {
		c.run(t, check.name, func(t *testing.T) {
			if !check.implemented {
				t.Skipf("%T does not implement %s", c.fs, check.iface)
			}
			name := strings.ToLower(strings.ReplaceAll(check.name, ".", "-")) + ".txt"
			f := prepare(t, name)
			err := check.call(c.canceledContext(), f)
			checkErrorIs(t, err, context.Canceled, "%s with a canceled context", check.name)
			unchanged(t, name, check.name)
			for _, suffix := range []string{"-copy", "-moved", "-renamed", "-moved-to", "-copied"} {
				c.checkMissing(t, "canceled/"+name+suffix, "%s with a canceled context must not create a file", check.name)
			}
		})
	}
}

func (c *conformance) testWrite(t *testing.T) {
	c.mkdir(t, "write")

	// OpenWriter must truncate an existing larger file
	long := []byte("0123456789ABCDEFGHIJ")
	short := []byte("xyz")
	shrink := c.wpath("write/overwrite-shrink.txt")
	c.writeFile(t, shrink, long)
	c.writeFile(t, shrink, short)
	checkBytes(t, short, c.readAll(t, shrink), "OpenWriter must truncate previous larger content")

	// Writing in several chunks must append within one writer
	chunked := c.wpath("write/chunked.txt")
	w, err := c.writeFS.OpenWriter(chunked, 0)
	mustNoError(t, err, "OpenWriter(%q)", chunked)
	for _, chunk := range []string{"first ", "second ", "third"} {
		_, err = w.Write([]byte(chunk))
		mustNoError(t, err, "Write chunk %q", chunk)
	}
	mustNoError(t, w.Close(), "Close writer")
	checkBytes(t, []byte("first second third"), c.readAll(t, chunked), "chunked writes must be concatenated")

	// Writing into a sub directory
	subDir := c.wpath("write/dir")
	c.makeDir(t, subDir)
	info, err := c.fs.Stat(subDir)
	mustNoError(t, err, "Stat of a created directory")
	checkTrue(t, info.IsDir, "a created directory must be a directory")
	inner := c.wpath("write/dir/inner.txt")
	c.writeFile(t, inner, []byte("inner"))
	checkBytes(t, []byte("inner"), c.readAll(t, inner), "content of a file in a sub directory")

	// Remove a file and a directory
	mustNoError(t, c.writeFS.Remove(inner), "Remove file")
	_, err = c.fs.Stat(inner)
	checkErrorIs(t, err, os.ErrNotExist, "a removed file must not exist")
	mustNoError(t, c.writeFS.Remove(subDir), "Remove empty directory")
	_, err = c.fs.Stat(subDir)
	checkErrorIs(t, err, os.ErrNotExist, "a removed directory must not exist")

	// OpenReadWriter (native or emulated): read, seek, write,
	// and the result must be visible after Close
	rwPath := c.wpath("write/readwriter.txt")
	c.writeFile(t, rwPath, []byte("Initial content"))
	rw, err := c.fileOf(rwPath).OpenReadWriter()
	mustNoError(t, err, "File.OpenReadWriter")
	buf := make([]byte, 7)
	n, err := rw.Read(buf)
	mustNoError(t, err, "Read")
	checkEqual(t, 7, n, "Read byte count")
	checkBytes(t, []byte("Initial"), buf, "Read must return the existing content")
	pos, err := rw.Seek(0, io.SeekStart)
	mustNoError(t, err, "Seek")
	checkEqual(t, int64(0), pos, "Seek(0, io.SeekStart)")
	n, err = rw.Write([]byte("Updated"))
	mustNoError(t, err, "Write")
	checkEqual(t, 7, n, "Write byte count")
	mustNoError(t, rw.Close(), "Close read writer")
	checkBytes(t, []byte("Updated content"), c.readAll(t, rwPath), "OpenReadWriter must not truncate and must persist writes on Close")
}

// testLargeContent round trips content larger than 1 MiB,
// which is larger than the buffers of most implementations.
func (c *conformance) testLargeContent(t *testing.T) {
	content := largeContent(largeContentSize)
	size := int64(len(content))
	c.mkdir(t, "large")
	path := c.wpath("large/content.bin")
	file := c.fileOf(path)

	// Write in odd sized chunks
	w, err := c.writeFS.OpenWriter(path, 0)
	mustNoError(t, err, "OpenWriter(%q)", path)
	for chunk := range slices.Chunk(content, 64<<10-1) {
		n, err := w.Write(chunk)
		mustNoError(t, err, "Write of a %d bytes chunk", len(chunk))
		mustEqual(t, len(chunk), n, "Write must write the whole chunk")
	}
	mustNoError(t, w.Close(), "Close writer of %q", path)
	info, err := c.fs.Stat(path)
	mustNoError(t, err, "Stat(%q)", path)
	checkEqual(t, size, info.Size, "Stat(%q).Size", path)

	c.run(t, "OpenReader", func(t *testing.T) {
		data, err := c.tryReadAll(path)
		if checkNoError(t, err, "reading %q with OpenReader", path) {
			checkBytes(t, content, data, "OpenReader content")
		}
	})

	c.run(t, "ReadAll", func(t *testing.T) {
		if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
			data, err := rafs.ReadAll(c.ctx, path)
			if checkNoError(t, err, "ReadAll(%q)", path) {
				checkBytes(t, content, data, "ReadAll content")
			}
		}
		data, err := file.ReadAll(c.ctx)
		if checkNoError(t, err, "File.ReadAll(%q)", path) {
			checkBytes(t, content, data, "File.ReadAll content")
		}
	})

	c.run(t, "OpenReadSeeker", func(t *testing.T) {
		rs, err := file.OpenReadSeeker()
		mustNoError(t, err, "File.OpenReadSeeker(%q)", path)
		defer rs.Close()

		// ReadAt at the start, across a 64 KiB boundary,
		// in the middle and across the end
		for _, at := range []struct{ off, n int64 }{
			{0, 100},
			{64<<10 - 100, 4096},
			{size / 2, 1000},
			{size - 500, 1000},
		} {
			buf := make([]byte, at.n)
			n, err := rs.ReadAt(buf, at.off)
			want := content[at.off:min(at.off+at.n, size)]
			if at.off+at.n > size {
				checkErrorIs(t, err, io.EOF, "ReadAt(%d bytes, %d) across the end", at.n, at.off)
			} else {
				checkNoError(t, err, "ReadAt(%d bytes, %d)", at.n, at.off)
			}
			checkEqual(t, len(want), n, "ReadAt(%d bytes, %d) byte count", at.n, at.off)
			checkBytes(t, want, buf[:n], "ReadAt(%d bytes, %d) content", at.n, at.off)
		}

		// Seek relative to the start, the end and the current position
		for _, s := range []struct {
			offset int64
			whence int
			want   int64
		}{
			{size / 3, io.SeekStart, size / 3},
			{-1000, io.SeekEnd, size - 1000},
			{-(size / 2), io.SeekCurrent, size - size/2},
		} {
			if s.whence == io.SeekCurrent {
				_, err = rs.Seek(size, io.SeekStart)
				mustNoError(t, err, "Seek(%d, io.SeekStart)", size)
			}
			pos, err := rs.Seek(s.offset, s.whence)
			if !checkNoError(t, err, "Seek(%d, %d)", s.offset, s.whence) {
				continue
			}
			checkEqual(t, s.want, pos, "Seek(%d, %d) position", s.offset, s.whence)
			data, err := io.ReadAll(rs)
			if checkNoError(t, err, "reading after Seek(%d, %d)", s.offset, s.whence) {
				checkBytes(t, content[s.want:], data, "content after Seek(%d, %d)", s.offset, s.whence)
			}
		}
		pos, err := rs.Seek(0, io.SeekCurrent)
		if checkNoError(t, err, "Seek(0, io.SeekCurrent) at the end") {
			checkEqual(t, size, pos, "Seek(0, io.SeekCurrent) at the end")
		}
	})

	c.run(t, "PastEOF", func(t *testing.T) {
		rs, err := file.OpenReadSeeker()
		mustNoError(t, err, "File.OpenReadSeeker(%q)", path)
		defer rs.Close()
		c.checkPastEOF(t, rs, size, "File.OpenReadSeeker")
	})

	c.run(t, "CopyFile", func(t *testing.T) {
		dest := c.wfile("large/copy.bin")
		mustNoError(t, fs.CopyFile(c.ctx, file, dest), "fs.CopyFile of large content")
		data, err := c.tryReadAll(dest.Path())
		if checkNoError(t, err, "reading the copy") {
			checkBytes(t, content, data, "fs.CopyFile content")
		}
	})
}

// writeErr opens a writer for filePath, writes data and closes it,
// returning the first error of the steps.
func (c *conformance) writeErr(filePath string, data []byte) error {
	w, err := c.writeFS.OpenWriter(filePath, 0)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return errors.Join(err, w.Close())
}

func (c *conformance) testWriteErrors(t *testing.T) {
	// The directory dir with the file dir/file.txt
	setup := func(t *testing.T, name string) (dir string) {
		t.Helper()
		dir = "write-errors/" + name
		c.put(t, dir+"/file.txt", "keep")
		return dir
	}
	intact := func(t *testing.T, dir, what string) {
		t.Helper()
		c.checkIsDir(t, dir, "%s must keep the directory", what)
		c.checkContent(t, dir+"/file.txt", "keep", "%s must keep the content of the directory", what)
	}

	c.run(t, "MakeDirExisting", func(t *testing.T) {
		dir := setup(t, "make-dir-existing")
		err := c.writeFS.MakeDir(c.wpath(dir), 0)
		checkErrorIs(t, err, os.ErrExist, "MakeDir on an existing directory")
		err = c.writeFS.MakeDir(c.wpath(dir+"/file.txt"), 0)
		checkErrorIs(t, err, os.ErrExist, "MakeDir on an existing file")
		intact(t, dir, "MakeDir on an existing path")
	})

	c.run(t, "RemoveMissing", func(t *testing.T) {
		c.ensureWork(t)
		missing := c.wpath("write-errors-does-not-exist.txt")
		checkErrorIs(t, c.writeFS.Remove(missing), os.ErrNotExist, "Remove of a missing file")
		checkErrorIs(t, c.fileOf(missing).Remove(), os.ErrNotExist, "File.Remove of a missing file")
	})

	c.run(t, "RemoveNonEmptyDir", func(t *testing.T) {
		dir := setup(t, "remove-non-empty")
		err := c.writeFS.Remove(c.wpath(dir))
		checkErrorIs(t, err, fs.ErrDirectoryNotEmpty{}, "Remove of a non-empty directory")
		intact(t, dir, "Remove of a non-empty directory")
		err = c.wfile(dir).Remove()
		checkErrorIs(t, err, fs.ErrDirectoryNotEmpty{}, "File.Remove of a non-empty directory")
		intact(t, dir, "File.Remove of a non-empty directory")
	})

	c.run(t, "IsDirectory", func(t *testing.T) {
		// Opening a writer for a directory may fail or the writing or the closing,
		// but either must report fs.ErrIsDirectory and keep the directory
		dir := setup(t, "is-directory")
		err := c.writeErr(c.wpath(dir), []byte("data"))
		checkErrorIs(t, err, fs.ErrIsDirectory{}, "OpenWriter, Write and Close for a directory")
		intact(t, dir, "OpenWriter for a directory")
		if wafs, ok := c.fs.(fs.WriteAllFileSystem); ok {
			err = wafs.WriteAll(c.ctx, c.wpath(dir), []byte("data"), 0)
			checkErrorIs(t, err, fs.ErrIsDirectory{}, "WriteAll to a directory")
			intact(t, dir, "WriteAll to a directory")
		}
		err = c.wfile(dir).WriteAll(c.ctx, []byte("data"))
		checkErrorIs(t, err, fs.ErrIsDirectory{}, "File.WriteAll to a directory")
		intact(t, dir, "File.WriteAll to a directory")
	})

	c.run(t, "MissingParent", func(t *testing.T) {
		c.ensureWork(t)
		path := c.wpath("write-errors-no-such-dir/file.txt")
		err := c.writeErr(path, []byte("data"))
		if err == nil {
			// Object stores have no directories and may allow this
			mustNoError(t, c.writeFS.Remove(path), "Remove(%q)", path)
			return
		}
		checkErrorIs(t, err, os.ErrNotExist, "OpenWriter into a missing directory")
	})
}

func (c *conformance) testOptionalWrite(t *testing.T) {
	c.run(t, "WriteAll", func(t *testing.T) {
		wafs, ok := c.fs.(fs.WriteAllFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.WriteAllFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		path := c.wpath("optional/writeall.txt")
		mustNoError(t, wafs.WriteAll(c.ctx, path, []byte("0123456789ABCDEFGHIJ"), 0), "WriteAll long")
		mustNoError(t, wafs.WriteAll(c.ctx, path, []byte("xyz"), 0), "WriteAll short")
		checkBytes(t, []byte("xyz"), c.readAll(t, path), "WriteAll must truncate previous larger content")
	})

	c.run(t, "Append", func(t *testing.T) {
		afs, ok := c.fs.(fs.AppendFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.AppendFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		path := c.wpath("optional/append.txt")
		mustNoError(t, afs.Append(c.ctx, path, []byte("first\n"), 0), "Append to a new file")
		mustNoError(t, afs.Append(c.ctx, path, []byte("second\n"), 0), "Append to an existing file")
		checkBytes(t, []byte("first\nsecond\n"), c.readAll(t, path), "Append content")
	})

	c.run(t, "OpenAppendWriter", func(t *testing.T) {
		awfs, ok := c.fs.(fs.AppendWriterFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.AppendWriterFileSystem", c.fs)
		}
		c.put(t, "optional/append-writer.txt", "existing ")
		path := c.wpath("optional/append-writer.txt")
		w, err := awfs.OpenAppendWriter(path, 0)
		mustNoError(t, err, "OpenAppendWriter")
		_, err = w.Write([]byte("appended"))
		mustNoError(t, err, "Write to append writer")
		mustNoError(t, w.Close(), "Close append writer")
		checkBytes(t, []byte("existing appended"), c.readAll(t, path), "OpenAppendWriter content")
	})

	c.run(t, "OpenReadWriter", func(t *testing.T) {
		rwfs, ok := c.fs.(fs.ReadWriterFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.ReadWriterFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		path := c.wpath("optional/native-readwriter.txt")
		rw, err := rwfs.OpenReadWriter(path, 0)
		mustNoError(t, err, "OpenReadWriter must create a missing file")
		_, err = rw.Write([]byte("created"))
		mustNoError(t, err, "Write to read writer")
		mustNoError(t, rw.Close(), "Close read writer")
		checkBytes(t, []byte("created"), c.readAll(t, path), "OpenReadWriter created file content")
	})

	c.run(t, "Truncate", func(t *testing.T) {
		tfs, ok := c.fs.(fs.TruncateFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.TruncateFileSystem", c.fs)
		}
		c.put(t, "optional/truncate.txt", "Hello, World!")
		path := c.wpath("optional/truncate.txt")
		mustNoError(t, tfs.Truncate(c.ctx, path, 5), "Truncate to smaller size")
		checkBytes(t, []byte("Hello"), c.readAll(t, path), "Truncate to smaller size content")
		mustNoError(t, tfs.Truncate(c.ctx, path, 8), "Truncate to larger size")
		checkBytes(t, []byte("Hello\x00\x00\x00"), c.readAll(t, path), "Truncate to larger size must pad with zeros")
	})

	c.run(t, "Touch", func(t *testing.T) {
		tfs, ok := c.fs.(fs.TouchFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.TouchFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		path := c.wpath("optional/touch.txt")
		mustNoError(t, tfs.Touch(path, 0), "Touch a new file")
		info, err := c.fs.Stat(path)
		mustNoError(t, err, "Stat of a touched file")
		checkFalse(t, info.IsDir, "a touched file must not be a directory")
		checkEqual(t, int64(0), info.Size, "a touched new file must be empty")

		c.put(t, "optional/touch-existing.txt", "keep me")
		err = tfs.Touch(c.wpath("optional/touch-existing.txt"), 0)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Logf("Touch of an existing file is unsupported: %v", err)
		} else {
			checkNoError(t, err, "Touch an existing file")
		}
		c.checkContent(t, "optional/touch-existing.txt", "keep me", "Touch must not modify the content of an existing file")
	})

	c.run(t, "Exists", func(t *testing.T) {
		efs, ok := c.fs.(fs.ExistsFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.ExistsFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		path := c.wpath("optional/exists.txt")
		exists, err := efs.Exists(path)
		mustNoError(t, err, "Exists before creation")
		checkFalse(t, exists, "Exists before creation")
		c.writeFile(t, path, []byte("x"))
		exists, err = efs.Exists(path)
		mustNoError(t, err, "Exists after creation")
		checkTrue(t, exists, "Exists after creation")
		mustNoError(t, c.writeFS.Remove(path), "Remove(%q)", path)
		exists, err = efs.Exists(path)
		mustNoError(t, err, "Exists after removal")
		checkFalse(t, exists, "Exists after removal")
	})

	c.run(t, "MakeAllDirs", func(t *testing.T) {
		mafs, ok := c.fs.(fs.MakeAllDirsFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.MakeAllDirsFileSystem", c.fs)
		}
		c.mkdir(t, "optional")
		nested := c.wpath("optional/all/dirs/nested")
		mustNoError(t, mafs.MakeAllDirs(nested, 0), "MakeAllDirs")
		info, err := c.fs.Stat(nested)
		mustNoError(t, err, "Stat of the nested directory")
		checkTrue(t, info.IsDir, "the nested path must be a directory")
		checkNoError(t, mafs.MakeAllDirs(nested, 0), "MakeAllDirs on an existing directory must not fail")
	})

	c.run(t, "RemoveAll", func(t *testing.T) {
		rafs, ok := c.fs.(fs.RemoveAllFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.RemoveAllFileSystem", c.fs)
		}
		c.put(t, "optional/remove-all/sub/file.txt", "x")
		c.put(t, "optional/remove-all/file.txt", "y")
		root := c.wpath("optional/remove-all")
		mustNoError(t, rafs.RemoveAll(c.ctx, root), "RemoveAll")
		_, err := c.fs.Stat(root)
		checkErrorIs(t, err, os.ErrNotExist, "RemoveAll must remove the directory tree")
		checkNoError(t, rafs.RemoveAll(c.ctx, root), "RemoveAll of a missing path must not fail")
		c.put(t, "optional/remove-all-file.txt", "z")
		single := c.wpath("optional/remove-all-file.txt")
		mustNoError(t, rafs.RemoveAll(c.ctx, single), "RemoveAll of a file")
		_, err = c.fs.Stat(single)
		checkErrorIs(t, err, os.ErrNotExist, "RemoveAll must remove a file")
	})

	c.run(t, "SymbolicLink", c.testSymbolicLink)

	c.run(t, "XAttr", func(t *testing.T) {
		xfs, ok := c.fs.(fs.XAttrFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.XAttrFileSystem", c.fs)
		}
		c.put(t, "optional/xattr.txt", "xattr")
		path := c.wpath("optional/xattr.txt")
		err := xfs.SetXAttr(path, "user.gofs-test", []byte("value"), 0, true)
		if err != nil {
			t.Skipf("extended attributes not available: %v", err)
		}
		names, err := xfs.ListXAttr(path, true)
		mustNoError(t, err, "ListXAttr")
		checkTrue(t, slices.Contains(names, "user.gofs-test"), "ListXAttr %q must contain the set attribute", names)
		value, err := xfs.GetXAttr(path, "user.gofs-test", true)
		mustNoError(t, err, "GetXAttr")
		checkBytes(t, []byte("value"), value, "GetXAttr")

		// The flags fail with errors that every file system wraps the same way
		err = xfs.SetXAttr(path, "user.gofs-test", []byte("created"), fs.XAttrCreate, true)
		checkErrorIs(t, err, os.ErrExist, "SetXAttr with XAttrCreate of an existing attribute")
		err = xfs.SetXAttr(path, "user.gofs-test", []byte("replaced"), fs.XAttrReplace, true)
		if checkNoError(t, err, "SetXAttr with XAttrReplace of an existing attribute") {
			value, err = xfs.GetXAttr(path, "user.gofs-test", true)
			mustNoError(t, err, "GetXAttr after XAttrReplace")
			checkBytes(t, []byte("replaced"), value, "GetXAttr after XAttrReplace")
		}
		err = xfs.SetXAttr(path, "user.gofs-missing", []byte("x"), fs.XAttrReplace, true)
		checkErrorIs(t, err, os.ErrNotExist, "SetXAttr with XAttrReplace of a missing attribute")
		_, err = xfs.GetXAttr(path, "user.gofs-missing", true)
		checkErrorIs(t, err, os.ErrNotExist, "GetXAttr of a missing attribute")
		err = xfs.RemoveXAttr(path, "user.gofs-missing", true)
		checkErrorIs(t, err, os.ErrNotExist, "RemoveXAttr of a missing attribute")
		err = xfs.SetXAttr(path, "user.gofs-created", []byte("created"), fs.XAttrCreate, true)
		if checkNoError(t, err, "SetXAttr with XAttrCreate of a missing attribute") {
			value, err = xfs.GetXAttr(path, "user.gofs-created", true)
			mustNoError(t, err, "GetXAttr after XAttrCreate")
			checkBytes(t, []byte("created"), value, "GetXAttr after XAttrCreate")
			checkNoError(t, xfs.RemoveXAttr(path, "user.gofs-created", true), "RemoveXAttr of the created attribute")
		}

		mustNoError(t, xfs.RemoveXAttr(path, "user.gofs-test", true), "RemoveXAttr")
		names, err = xfs.ListXAttr(path, true)
		mustNoError(t, err, "ListXAttr after RemoveXAttr")
		checkFalse(t, slices.Contains(names, "user.gofs-test"), "ListXAttr %q must not contain the removed attribute", names)
	})

	c.run(t, "Permissions", func(t *testing.T) {
		pfs, ok := c.fs.(fs.PermissionsFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.PermissionsFileSystem", c.fs)
		}
		c.put(t, "optional/perm.txt", "perm")
		path := c.wpath("optional/perm.txt")
		want := fs.UserRead | fs.UserWrite | fs.GroupRead
		err := pfs.SetPermissions(path, want)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("SetPermissions unsupported: %v", err)
		}
		mustNoError(t, err, "SetPermissions")
		info, err := c.fs.Stat(path)
		mustNoError(t, err, "Stat after SetPermissions")
		mask := c.cfg.PermissionMask
		if mask == 0 {
			mask = fs.AllReadWrite | fs.AllExecute
		}
		if isLocal(c.fs) && runtime.GOOS == "windows" {
			// os.Chmod on Windows only toggles the read-only attribute
			mask = fs.UserWrite
		}
		checkEqual(t, want&mask, info.Permissions&mask, "permissions after SetPermissions (masked with %o)", mask)
	})
}

// testSymbolicLink checks a fs.SymbolicLinkFileSystem with a link to a file.
func (c *conformance) testSymbolicLink(t *testing.T) {
	slfs, ok := c.fs.(fs.SymbolicLinkFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.SymbolicLinkFileSystem", c.fs)
	}
	const content = "link target"
	c.put(t, "symlink/target.txt", content)
	target := c.wpath("symlink/target.txt")
	link := c.wpath("symlink/link.txt")
	err := slfs.CreateSymbolicLink(target, link)
	if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, os.ErrPermission) {
		t.Skipf("symbolic links not available: %v", err)
	}
	mustNoError(t, err, "CreateSymbolicLink")

	linkInfo, err := slfs.Lstat(link)
	if checkNoError(t, err, "Lstat(link)") {
		checkTrue(t, linkInfo.IsSymlink, "Lstat(link).IsSymlink")
	}
	targetInfo, err := slfs.Lstat(target)
	if checkNoError(t, err, "Lstat(target)") {
		checkFalse(t, targetInfo.IsSymlink, "Lstat(target).IsSymlink")
	}
	got, err := slfs.ReadSymbolicLink(link)
	if checkNoError(t, err, "ReadSymbolicLink") {
		checkEqual(t, target, got, "ReadSymbolicLink must return the target as stored")
	}
	c.checkContent(t, "symlink/link.txt", content, "reading through the link")
	info, err := c.fs.Stat(link)
	if checkNoError(t, err, "Stat(link)") {
		checkTrue(t, info.IsSymlink, "Stat(link).IsSymlink")
		checkEqual(t, int64(len(content)), info.Size, "Stat(link) must report the target size")
	}

	// The File API
	checkTrue(t, c.fileOf(link).IsSymbolicLink(), "File.IsSymbolicLink(link)")
	checkFalse(t, c.fileOf(target).IsSymbolicLink(), "File.IsSymbolicLink(target)")
	fileInfo, err := c.fileOf(link).Lstat()
	if checkNoError(t, err, "File.Lstat(link)") {
		checkTrue(t, fileInfo.IsSymlink, "File.Lstat(link).IsSymlink")
	}

	// ListDir describes the link itself
	listed := map[string]bool{}
	err = c.fs.ListDir(c.ctx, c.wpath("symlink"), nil, func(info *fs.FileInfo) error {
		listed[info.Name] = info.IsSymlink
		return nil
	})
	mustNoError(t, err, "ListDir of the directory with the link")
	isSymlink, found := listed["link.txt"]
	if checkTrue(t, found, "ListDir must list the link, listed %v", listed) {
		checkTrue(t, isSymlink, "the ListDir FileInfo of a link must have IsSymlink set")
	}
	checkFalse(t, listed["target.txt"], "the ListDir FileInfo of the link target must not have IsSymlink set")
}

// testAbort checks writers implementing fs.WriteAborter:
// Abort must leave no file or changes behind and Close after Abort returns nil.
func (c *conformance) testAbort(t *testing.T) {
	type openFunc func(path string) (io.WriteCloser, error)
	openWriter := func(path string) (io.WriteCloser, error) { return c.writeFS.OpenWriter(path, 0) }
	openAppendWriter := func(path string) (io.WriteCloser, error) {
		return c.fs.(fs.AppendWriterFileSystem).OpenAppendWriter(path, 0)
	}
	openFileAppendWriter := func(path string) (io.WriteCloser, error) { return c.fileOf(path).OpenAppendWriter() }

	abort := func(t *testing.T, open openFunc, path, what string) {
		t.Helper()
		w, err := open(path)
		mustNoError(t, err, "%s(%q)", what, path)
		aborter, ok := w.(fs.WriteAborter)
		if !ok {
			_ = w.Close()
			t.Skipf("the writer %T of %s does not implement fs.WriteAborter", w, what)
		}
		_, err = w.Write([]byte("aborted data"))
		checkNoError(t, err, "Write to the writer of %s", what)
		checkNoError(t, aborter.Abort(), "Abort of the writer of %s", what)
		checkNoError(t, w.Close(), "Close after Abort of the writer of %s must be a no-op", what)
	}
	// The sub-tests NewFile and ExistingFile of the writers of open
	writer := func(name string, open openFunc) {
		c.run(t, name, func(t *testing.T) {
			c.run(t, "NewFile", func(t *testing.T) {
				rel := "abort/" + strings.ToLower(name) + "-new.txt"
				c.mkdir(t, "abort")
				abort(t, open, c.wpath(rel), name)
				c.checkMissing(t, rel, "Abort of the writer of %s must not leave a file behind", name)
			})
			c.run(t, "ExistingFile", func(t *testing.T) {
				rel := "abort/" + strings.ToLower(name) + "-existing.txt"
				c.put(t, rel, "existing content")
				abort(t, open, c.wpath(rel), name)
				c.checkContent(t, rel, "existing content", "Abort of the writer of %s must not change an existing file", name)
			})
		})
	}

	writer("OpenWriter", openWriter)
	if _, ok := c.fs.(fs.AppendWriterFileSystem); ok {
		writer("OpenAppendWriter", openAppendWriter)
	}
	writer("File.OpenAppendWriter", openFileAppendWriter)
}

// testHighLevelWrite exercises the write paths of the File API,
// which use the generic emulations for file systems without the
// corresponding optional interfaces.
func (c *conformance) testHighLevelWrite(t *testing.T) {
	c.mkdir(t, "high-level")
	dir := c.wfile("high-level")

	// WriteAll must truncate
	f := dir.Join("file-writeall.txt")
	mustNoError(t, f.WriteAll(c.ctx, []byte("0123456789ABCDEFGHIJ")), "File.WriteAll long")
	mustNoError(t, f.WriteAll(c.ctx, []byte("xyz")), "File.WriteAll short")
	got, err := f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz"), got, "File.WriteAll must truncate previous larger content")
	checkTrue(t, f.IsWritable(), "File.IsWritable of an existing file")
	checkTrue(t, dir.Join("new-file.txt").IsWritable(), "File.IsWritable of a new file in an existing directory")

	// Append and AppendString
	mustNoError(t, f.Append(c.ctx, []byte("-appended")), "File.Append")
	mustNoError(t, f.AppendString(c.ctx, "-string"), "File.AppendString")
	got, err = f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz-appended-string"), got, "File.Append content")

	// OpenAppendWriter
	w, err := f.OpenAppendWriter()
	mustNoError(t, err, "File.OpenAppendWriter")
	_, err = w.Write([]byte("-writer"))
	mustNoError(t, err, "Write to the append writer")
	mustNoError(t, w.Close(), "Close of the append writer")
	got, err = f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz-appended-string-writer"), got, "File.OpenAppendWriter content")

	// Append to a new file
	newAppend := dir.Join("file-append-new.txt")
	mustNoError(t, newAppend.Append(c.ctx, []byte("new")), "File.Append to a new file")
	got, err = newAppend.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("new"), got, "File.Append to a new file content")

	// Truncate
	mustNoError(t, f.Truncate(c.ctx, 3), "File.Truncate shrink")
	got, err = f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz"), got, "File.Truncate shrink content")
	mustNoError(t, f.Truncate(c.ctx, 5), "File.Truncate grow")
	got, err = f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz\x00\x00"), got, "File.Truncate grow content")

	// Touch
	touched := dir.Join("file-touch.txt")
	mustNoError(t, touched.Touch(), "File.Touch new file")
	checkTrue(t, touched.Exists(), "a touched file must exist")
	checkEqual(t, int64(0), touched.Size(), "a touched file must be empty")
	err = f.Touch()
	if err != nil {
		checkErrorIs(t, err, errors.ErrUnsupported, "File.Touch of an existing file may only fail with ErrUnsupported")
	}
	got, err = f.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("xyz\x00\x00"), got, "File.Touch must never modify the content")

	// MakeDir and MakeAllDirs
	sub := dir.Join("file-dir")
	mustNoError(t, sub.MakeDir(), "File.MakeDir")
	checkTrue(t, sub.IsDir(), "File.MakeDir must create a directory")
	checkNoError(t, sub.MakeDir(), "File.MakeDir on an existing directory must not fail")
	checkErrorIs(t, f.MakeDir(), fs.ErrIsNotDirectory{}, "File.MakeDir on an existing file")
	nested := dir.Join("file-all", "dirs", "nested")
	mustNoError(t, nested.MakeAllDirs(), "File.MakeAllDirs")
	checkTrue(t, nested.IsDir(), "File.MakeAllDirs must create the directories")
	checkTrue(t, nested.IsEmptyDir(), "a new directory must be empty")
	checkNoError(t, nested.MakeAllDirs(), "File.MakeAllDirs on an existing directory must not fail")

	// ReadFrom
	target := dir.Join("file-readfrom.txt")
	n, err := target.ReadFrom(strings.NewReader("from reader"))
	mustNoError(t, err, "File.ReadFrom")
	checkEqual(t, int64(len("from reader")), n, "File.ReadFrom byte count")
	got, err = target.ReadAll(c.ctx)
	mustNoError(t, err, "File.ReadAll")
	checkBytes(t, []byte("from reader"), got, "File.ReadFrom content")

	// JSON and XML round trips
	type doc struct {
		Name  string `json:"name" xml:"name"`
		Count int    `json:"count" xml:"count"`
	}
	jsonFile := dir.Join("file.json")
	mustNoError(t, jsonFile.WriteJSON(c.ctx, doc{Name: "go-fs", Count: 3}), "File.WriteJSON")
	var decoded doc
	mustNoError(t, jsonFile.ReadJSON(c.ctx, &decoded), "File.ReadJSON")
	checkEqual(t, doc{Name: "go-fs", Count: 3}, decoded, "JSON round trip")
	xmlFile := dir.Join("file.xml")
	mustNoError(t, xmlFile.WriteXML(c.ctx, doc{Name: "go-fs", Count: 3}), "File.WriteXML")
	decoded = doc{}
	mustNoError(t, xmlFile.ReadXML(c.ctx, &decoded), "File.ReadXML")
	checkEqual(t, doc{Name: "go-fs", Count: 3}, decoded, "XML round trip")

	// CopyRecursive
	c.put(t, "high-level/tree/child.txt", "dir-content")
	c.put(t, "high-level/tree/sub/leaf.txt", "leaf")
	tree := dir.Join("tree")
	copiedTree := dir.Join("copied-tree")
	mustNoError(t, fs.CopyRecursive(c.ctx, tree, copiedTree), "fs.CopyRecursive")
	c.checkContent(t, "high-level/copied-tree/child.txt", "dir-content", "fs.CopyRecursive")
	c.checkContent(t, "high-level/copied-tree/sub/leaf.txt", "leaf", "fs.CopyRecursive")

	// Remove variants
	mustNoError(t, target.Remove(), "File.Remove")
	checkFalse(t, target.Exists(), "a removed file must not exist")
	checkErrorIs(t, target.Remove(), os.ErrNotExist, "File.Remove of a missing file")
	mustNoError(t, copiedTree.RemoveRecursive(c.ctx), "File.RemoveRecursive")
	checkFalse(t, copiedTree.Exists(), "a recursively removed directory must not exist")
	checkNoError(t, copiedTree.RemoveRecursive(c.ctx), "File.RemoveRecursive of a missing path must not fail")
	mustNoError(t, tree.Join("sub").RemoveDirContentsRecursive(c.ctx), "File.RemoveDirContentsRecursive")
	checkTrue(t, tree.Join("sub").IsEmptyDir(), "a directory must be empty after RemoveDirContentsRecursive")

	// Cross file system copy and move into an in-memory file system
	memFS, err := fs.NewMemFileSystem(nil)
	mustNoError(t, err, "fs.NewMemFileSystem")
	mustNoError(t, fs.Register(memFS), "fs.Register")
	t.Cleanup(func() { _ = memFS.Close() })
	memFile := memFS.RootDir().Join("copied.txt")
	mustNoError(t, fs.CopyFile(c.ctx, f, memFile), "fs.CopyFile to another file system")
	got, err = memFile.ReadAll(c.ctx)
	mustNoError(t, err, "reading the copy in the other file system")
	checkBytes(t, []byte("xyz\x00\x00"), got, "cross file system copy content")
	identical, err := fs.IdenticalFileContents(c.ctx, f, memFile)
	mustNoError(t, err, "fs.IdenticalFileContents")
	checkTrue(t, identical, "IdenticalFileContents across file systems")
	memDir := memFS.RootDir().Join("moved-dir")
	mustNoError(t, tree.MoveTo(c.ctx, memDir), "File.MoveTo to another file system")
	checkFalse(t, tree.Exists(), "File.MoveTo to another file system must remove the source")
	checkTrue(t, memDir.IsDir(), "File.MoveTo to another file system must create the destination")
	got, err = memDir.Join("child.txt").ReadAll(c.ctx)
	if checkNoError(t, err, "reading a moved file in the other file system") {
		checkBytes(t, []byte("dir-content"), got, "File.MoveTo to another file system content")
	}
}
