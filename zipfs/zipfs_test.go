package zipfs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ungerik/go-fs"
)

func TestZipFileSystem(t *testing.T) {
	// Create a temporary zip file for testing
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(context.Background()), "tempDir.RemoveRecursive(context.Background()) should not return an error")
	})

	zipFile := tempDir.Join("test.zip")

	// Create a zip file with test content
	t.Run("CreateZipFile", func(t *testing.T) {
		zipWriter, err := NewWriter(zipFile, "")
		require.NoError(t, err, "NewWriter should not error")
		require.NoError(t, fs.Register(zipWriter))

		t.Cleanup(func() {
			assert.NoError(t, zipWriter.Close(), "zipWriter.Close() should not error")
		})

		assert.Equal(t, zipWriter.Prefix(), zipWriter.ID(), "ID is the unique prefix of the archive")

		// Create test directory structure
		testDir := "test"
		err = zipWriter.MakeDir(testDir, 0)
		require.NoError(t, err, "MakeDir should not error")

		// Write some test files
		testFilePath := zipWriter.CleanPath(testDir, "test-file.txt")
		writer, err := zipWriter.OpenWriter(testFilePath, 0)
		require.NoError(t, err, "OpenWriter should not error")

		testContent := []byte("Hello, ZipFileSystem!")
		n, err := writer.Write(testContent)
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, len(testContent), n, "Should write all bytes")

		err = writer.Close()
		require.NoError(t, err, "Close writer should not error")

		// Write another file
		testFile2Path := zipWriter.CleanPath(testDir, "test-file-2.txt")
		writer2, err := zipWriter.OpenWriter(testFile2Path, 0)
		require.NoError(t, err, "OpenWriter should not error for second file")

		testContent2 := []byte("Another test file")
		_, err = writer2.Write(testContent2)
		require.NoError(t, err, "Write should not error for second file")

		err = writer2.Close()
		require.NoError(t, err, "Close writer should not error for second file")
	})

	// Now test reading from the zip file
	t.Run("ReadZipFile", func(t *testing.T) {
		zipReader, err := NewReader(zipFile, "")
		require.NoError(t, err, "NewReader should not error")
		require.NoError(t, fs.Register(zipReader))

		t.Cleanup(func() {
			assert.NoError(t, zipReader.Close(), "zipReader.Close() should not error")
		})

		// Run a subset of FileSystemTests suitable for read-only filesystem
		t.Run("Metadata", func(t *testing.T) {
			readable, writable := zipReader.ReadableWritable()
			assert.True(t, readable, "ZipFileSystem should be readable")
			assert.False(t, writable, "ZipFileSystem should not be writable in read mode")

			assert.Contains(t, zipReader.Name(), "Zip reader filesystem", "Name() should contain 'Zip reader filesystem'")
			assert.True(t, len(zipReader.Prefix()) > 0, "Prefix() should not be empty")

			// A Reader and a Writer of an archive must report their id
			// in the same form, so that neither can be mistaken for the
			// other when file systems are compared by id.
			assert.Equal(t, zipReader.Prefix(), zipReader.ID(), "ID is the unique prefix of the archive")

			rootDir := zipReader.RootDir()
			assert.NotEmpty(t, rootDir, "RootDir() should not be empty")
		})

		t.Run("Stat", func(t *testing.T) {
			testFilePath := zipReader.CleanPath("test", "test-file.txt")
			info, err := zipReader.Stat(testFilePath)
			require.NoError(t, err, "Stat should not error")
			assert.False(t, info.IsDir, "test-file.txt should not be a directory")
			assert.True(t, info.IsRegular, "test-file.txt should be a regular file")
			assert.Greater(t, info.Size, int64(0), "File size should be greater than 0")
		})

		t.Run("Exists", func(t *testing.T) {
			require.NoError(t, zipReader.RootDir().Join("test", "test-file.txt").CheckExists(), "test-file.txt should exist")

			err := zipReader.RootDir().Join("test", "non-existent.txt").CheckExists()
			assert.ErrorIs(t, err, os.ErrNotExist, "non-existent.txt should not exist")
		})

		t.Run("ListDir", func(t *testing.T) {
			var files []*fs.FileInfo
			err := zipReader.ListDir(t.Context(), "test", nil, func(info *fs.FileInfo) error {
				files = append(files, info)
				return nil
			})
			require.NoError(t, err, "ListDir should not error")
			assert.GreaterOrEqual(t, len(files), 2, "Should list at least 2 files")
		})

		t.Run("OpenReader", func(t *testing.T) {
			testFilePath := zipReader.CleanPath("test", "test-file.txt")
			reader, err := zipReader.OpenReader(testFilePath)
			require.NoError(t, err, "OpenReader should not error")
			defer reader.Close()

			content := make([]byte, 100)
			n, err := reader.Read(content)
			if err != nil && err.Error() != "EOF" {
				require.NoError(t, err, "Read should not error (except EOF)")
			}
			assert.Greater(t, n, 0, "Should read some bytes")
			assert.Equal(t, "Hello, ZipFileSystem!", string(content[:n]))
		})

		t.Run("OpenReadWriter_ReadOnly", func(t *testing.T) {
			// A ZIP archive is opened either for reading or for writing,
			// so random access read-write must be rejected as read-only
			// by the fs package based on ReadableWritable.
			_, err := zipReader.RootDir().Join("test", "test-file.txt").OpenReadWriter()
			require.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "OpenReadWriter should error on read-only ZIP")
		})
	})

	t.Run("WriteOnlyZipFile", func(t *testing.T) {
		writeOnlyZipFile := tempDir.Join("write-only.zip")
		zipWriter, err := NewWriter(writeOnlyZipFile, "")
		require.NoError(t, err, "NewWriter should not error")
		require.NoError(t, fs.Register(zipWriter))

		t.Cleanup(func() {
			assert.NoError(t, zipWriter.Close(), "zipWriter.Close() should not error")
		})

		t.Run("OpenReadWriter_WriteOnly", func(t *testing.T) {
			_, err := zipWriter.RootDir().Join("test", "test-file.txt").OpenReadWriter()
			require.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "OpenReadWriter should error on write-only ZIP")
		})
	})
}

// TestZipWriter_ConcurrentWriters verifies that several entry writers can
// be open at the same time, because each file is buffered until its
// writer is closed, and that writes after Close fail instead of being
// dropped or corrupting the archive.
func TestZipWriter_ConcurrentWriters(t *testing.T) {
	ctx := t.Context()
	zipFile := newMemFS(t).RootDir().Join("concurrent.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	assert.Contains(t, zipWriter.Name(), "Zip writer filesystem", "writer mode Name()")

	w1, err := zipWriter.OpenWriter("a.txt", 0)
	require.NoError(t, err)
	w2, err := zipWriter.OpenWriter("b.txt", 0)
	require.NoError(t, err)
	_, err = w1.Write([]byte("hello"))
	require.NoError(t, err)
	_, err = w2.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, w2.Close())
	_, err = w1.Write([]byte(" again"))
	require.NoError(t, err, "the first writer is not superseded by the second")
	require.NoError(t, w1.Close())

	_, err = w1.Write([]byte("more"))
	assert.ErrorIs(t, err, os.ErrClosed, "write to a closed entry writer")
	require.NoError(t, w1.Close(), "Close is idempotent")
	require.NoError(t, zipWriter.Close())

	reader := openReader(t, zipFile)
	for name, want := range map[string]string{"a.txt": "hello again", "b.txt": "world"} {
		data, err := reader.RootDir().Join(name).ReadAllString(ctx)
		require.NoError(t, err)
		assert.Equal(t, want, data, name)
	}
}

// newMemFS returns a registered MemFileSystem that is closed with the test.
func newMemFS(t *testing.T) *fs.MemFileSystem {
	t.Helper()
	memFS, err := fs.NewMemFileSystem(nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(memFS))
	t.Cleanup(func() { _ = memFS.Close() })
	return memFS
}

// openReader returns a registered Reader for the archive
// that is closed with the test.
func openReader(t *testing.T, archive fs.FileReader) *Reader {
	t.Helper()
	reader, err := NewReader(archive, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(reader))
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

// makeZip returns a ZIP archive with the entries in the passed
// order, written directly with archive/zip like a foreign tool would.
func makeZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		require.NoError(t, err)
		_, err = io.WriteString(w, e[1])
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// zipEntryNames returns the entry names of a ZIP archive in their order.
func zipEntryNames(t *testing.T, zipFile fs.File) []string {
	t.Helper()
	data, err := zipFile.ReadAll(t.Context())
	require.NoError(t, err)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	return names
}

// TestZipWriter_WriteOnce verifies that a path can be written only once.
// A second write of the same path used to create a duplicate entry,
// so the stale first content was read back and ListDir failed with
// "duplicate entries".
func TestZipWriter_WriteOnce(t *testing.T) {
	ctx := t.Context()
	zipFile := newMemFS(t).RootDir().Join("once.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipWriter))
	f := zipWriter.RootDir().Join("a.txt")

	require.NoError(t, f.WriteAll(ctx, []byte("first")))
	assert.ErrorIs(t, f.WriteAll(ctx, []byte("second")), os.ErrExist, "WriteAll of a written file")
	assert.ErrorIs(t, zipWriter.Touch("/a.txt", 0), os.ErrExist, "Touch of a written file")

	require.NoError(t, zipWriter.MakeDir("/dir", 0))
	assert.ErrorIs(t, zipWriter.MakeDir("/dir", 0), os.ErrExist, "MakeDir of a written directory")
	assert.ErrorIs(t, zipWriter.MakeDir("/", 0), os.ErrExist, "MakeDir of the root")
	assert.ErrorIs(t, zipWriter.Touch("/dir", 0), fs.ErrIsDirectory{}, "a file over a directory")
	assert.ErrorIs(t, zipWriter.Touch("/a.txt/below", 0), fs.ErrIsNotDirectory{}, "a file below a file")
	assert.ErrorIs(t, zipWriter.Touch(`/back\slash.txt`, 0), os.ErrInvalid, "a backslash would be a separator when read")
	require.NoError(t, zipWriter.RootDir().Join("emptydir").MakeDir())
	require.NoError(t, zipWriter.Close())

	assert.Equal(t, []string{"a.txt", "dir/", "emptydir/"}, zipEntryNames(t, zipFile))
	reader := openReader(t, zipFile)
	data, err := reader.RootDir().Join("a.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "first", data)
	assert.True(t, reader.RootDir().Join("emptydir").IsDir(), "an empty directory is kept")
	names, err := reader.RootDir().ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Len(t, names, 3)
}

// TestZipWriter_InvalidNames is a regression test: the writer wrote names
// that are not valid in a zip archive, and a name longer than 65535 bytes,
// which archive/zip adds to its central directory before rejecting it,
// made the whole archive unreadable although Close returned no error.
func TestZipWriter_InvalidNames(t *testing.T) {
	zipFile := newMemFS(t).RootDir().Join("names.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	require.NoError(t, zipWriter.Touch("/before.txt", 0))

	longName := "/" + strings.Repeat("a", math.MaxUint16)
	for _, name := range []string{"/a\x00b.txt", "/a\nb.txt", "/evil\u202etxt.exe", "/dir/\xff.txt", "/C:x.txt", longName + "a"} {
		assert.ErrorIs(t, zipWriter.Touch(name, 0), os.ErrInvalid, "Touch %.20q", name)
		_, err := zipWriter.OpenWriter(name, 0)
		assert.ErrorIs(t, err, os.ErrInvalid, "OpenWriter %.20q", name)
		assert.ErrorIs(t, zipWriter.MakeDir(name, 0), os.ErrInvalid, "MakeDir %.20q", name)
		assert.ErrorIs(t, zipWriter.MakeAllDirs(name+"/sub", 0), os.ErrInvalid, "MakeAllDirs %.20q", name)
	}
	assert.ErrorIs(t, zipWriter.MakeDir(longName, 0), os.ErrInvalid, "the slash of a directory entry is too much")
	assert.ErrorIs(t, zipWriter.MakeAllDirs(longName, 0), os.ErrInvalid, "MakeAllDirs counts the slash too")
	require.NoError(t, zipWriter.Touch(longName, 0), "a file name of the maximum length")
	require.NoError(t, zipWriter.Touch("/after.txt", 0))
	require.NoError(t, zipWriter.Close())

	assert.Equal(t, []string{"before.txt", longName[1:], "after.txt"}, zipEntryNames(t, zipFile))
	assert.True(t, openReader(t, zipFile).RootDir().Join("after.txt").Exists(), "the archive is readable")
}

// TestZipReader_CP437Names is a regression test: the reader used the CP-437
// bytes of an entry name without the UTF-8 flag, like old Windows tools write
// umlauts, as path, so the file couldn't be copied into a zipfs.Writer,
// which rejects names that are not valid UTF-8.
func TestZipReader_CP437Names(t *testing.T) {
	ctx := t.Context()
	memFS := newMemFS(t)
	archive := memFS.RootDir().Join("legacy.zip")
	require.NoError(t, archive.WriteAll(ctx, makeZip(t, [2]string{"dir/M\x81ller.pdf", "pdf"})))
	reader := openReader(t, archive)

	file := reader.RootDir().Join("dir", "Müller.pdf")
	data, err := file.ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "pdf", data)

	copied := memFS.RootDir().Join("copied.zip")
	zipWriter, err := NewWriter(copied, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipWriter))
	require.NoError(t, fs.CopyRecursive(ctx, reader.RootDir(), zipWriter.RootDir()))
	require.NoError(t, zipWriter.Close())
	assert.Equal(t, []string{"dir/", "dir/Müller.pdf"}, zipEntryNames(t, copied))
}

// TestZipWriter_ValidName is a regression test: a copy or move into a
// zipfs.Writer failed at the first name that the writer rejects, like a
// name with a colon, and left the files copied until then in the archive.
func TestZipWriter_ValidName(t *testing.T) {
	ctx := t.Context()
	memFS := newMemFS(t)
	src := memFS.RootDir().Join("src")
	require.NoError(t, src.MakeDir())
	for _, name := range []string{"a.txt", "b:c.txt", "z.txt"} {
		require.NoError(t, src.Join(name).WriteAll(ctx, []byte(name)))
	}
	zipFile := memFS.RootDir().Join("copy.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipWriter))

	assert.NoError(t, zipWriter.ValidName("a.txt"))
	for _, name := range []string{"b:c.txt", `a\b.txt`, "a\x00b", "evil\u202etxt.exe", "\xff.txt"} {
		assert.ErrorIs(t, zipWriter.ValidName(name), os.ErrInvalid, "%q", name)
	}

	assert.ErrorIs(t, fs.CopyRecursive(ctx, src, zipWriter.RootDir().Join("copy")), os.ErrInvalid)
	assert.ErrorIs(t, fs.Move(ctx, src, zipWriter.RootDir().Join("moved")), os.ErrInvalid)
	assert.True(t, src.Join("a.txt").Exists(), "the source of a failed move is kept")
	require.NoError(t, zipWriter.Close())
	assert.Empty(t, zipEntryNames(t, zipFile), "nothing was written before the failures")
}

// TestZipWriter_Abort verifies that an aborted entry writer writes no entry.
func TestZipWriter_Abort(t *testing.T) {
	zipFile := newMemFS(t).RootDir().Join("abort.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)

	w, err := zipWriter.OpenWriter("/aborted.txt", 0)
	require.NoError(t, err)
	aborter, ok := w.(fs.WriteAborter)
	require.True(t, ok, "entry writers implement fs.WriteAborter")
	_, err = w.Write([]byte("partial"))
	require.NoError(t, err)
	require.NoError(t, aborter.Abort())
	require.NoError(t, w.Close(), "Close after Abort is a no-op")

	require.NoError(t, zipWriter.Touch("/kept.txt", 0))
	require.NoError(t, zipWriter.Close())
	assert.Equal(t, []string{"kept.txt"}, zipEntryNames(t, zipFile))
}

// TestZipWriter_EntryMetadata verifies that written entries get the
// current time and the passed permissions. They used to get the
// MS-DOS zero time 1979-11-30 and ignore the permissions.
func TestZipWriter_EntryMetadata(t *testing.T) {
	zipFile := newMemFS(t).RootDir().Join("meta.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	before := time.Now().Add(-2 * time.Second)
	w, err := zipWriter.OpenWriter("/exec.sh", 0o750)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, zipWriter.Touch("/default.txt", 0))
	require.NoError(t, zipWriter.MakeDir("/private", 0o700))
	require.NoError(t, zipWriter.MakeAllDirs("/implied/deeper", 0))
	require.NoError(t, zipWriter.Close())

	reader := openReader(t, zipFile)
	for path, perm := range map[string]fs.Permissions{
		"/exec.sh":        0o750,
		"/default.txt":    DefaultPermissions,
		"/private":        0o700,
		"/implied/deeper": DefaultDirPermissions,
		"/implied":        DefaultDirPermissions,
	} {
		info, err := reader.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, perm, info.Permissions, "permissions of %s", path)
		assert.True(t, info.Modified.After(before), "modification time of %s is the time of writing: %s", path, info.Modified)
	}
	info, err := reader.Stat("/exec.sh")
	require.NoError(t, err)
	_, ok := info.Sys.(*zip.FileHeader)
	assert.True(t, ok, "Sys is the zip.FileHeader")
}

// TestZipReader_Concurrent verifies that the files of a Reader can be
// listed and read concurrently, run it with -race.
func TestZipReader_Concurrent(t *testing.T) {
	reader := openReader(t, fs.NewMemFile("concurrent.zip", makeZip(t,
		[2]string{"a.txt", "a"},
		[2]string{"dir/b.txt", "bb"},
	)))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			names, err := reader.RootDir().ListDirRecursiveMax(t.Context(), -1)
			assert.NoError(t, err)
			assert.Len(t, names, 2)
			data, err := reader.RootDir().Join("dir", "b.txt").ReadAllString(t.Context())
			assert.NoError(t, err)
			assert.Equal(t, "bb", data)
		})
	}
	wg.Wait()
}

// TestZipReader_SameRulesAsTarfs verifies that the reader cleans unsafe
// names and lets a later entry replace an earlier one, like tarfs.
func TestZipReader_SameRulesAsTarfs(t *testing.T) {
	ctx := t.Context()
	reader := openReader(t, fs.NewMemFile("foreign.zip", makeZip(t,
		[2]string{"a.txt", "stale"},
		[2]string{"a.txt", "replaced"},
		[2]string{`..\..\evil.txt`, "pwned"},
		[2]string{"f", "file"},
		[2]string{"f/below.txt", "below"},
	)))
	root := reader.RootDir()
	data, err := root.Join("a.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "replaced", data, "the later entry wins")
	assert.Equal(t, int64(len("replaced")), root.Join("a.txt").Size())
	assert.True(t, root.Join("evil.txt").Exists(), "the unsafe name is cleaned")
	assert.True(t, root.Join("f").IsDir(), "the later entry f/below.txt replaces the file f")

	names, err := root.ListDirMax(ctx, -1)
	require.NoError(t, err, "no duplicate entries error")
	assert.Equal(t, []fs.File{root.Join("f"), root.Join("a.txt"), root.Join("evil.txt")}, names)

	_, err = root.Join("f").OpenReader()
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenReader of a directory")
	_, err = root.Join("missing").OpenReader()
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestZipWriter_MakeDirReadOnlyErrors(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(context.Background()))
	})

	zipFile := tempDir.Join("ro.zip")
	zipWriter, err := NewWriter(zipFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipWriter))
	w, err := zipWriter.OpenWriter("f.txt", 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, zipWriter.Close())

	zipReader, err := NewReader(zipFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipReader))
	t.Cleanup(func() { assert.NoError(t, zipReader.Close()) })

	// Writes to a read-only archive must report read-only, not silently succeed.
	err = zipReader.RootDir().Join("somedir").MakeDir()
	require.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	err = zipReader.RootDir().Join("f.txt").Remove()
	require.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
}

// TestZipWriter_ReadRejected verifies that the read methods of a
// Writer report fs.ErrWriteOnlyFileSystem while the archive is
// open and fs.ErrFileSystemClosed after it was closed. archive/zip has
// no way to read back what was written, so returning empty results
// instead of an error would silently hide data.
func TestZipWriter_ReadRejected(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(context.Background()))
	})
	ctx := t.Context()

	zipWriter, err := NewWriter(tempDir.Join("writeonly.zip"), "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(zipWriter))

	// Touch writes an empty entry without handing out a writer
	require.NoError(t, zipWriter.Touch("/empty.txt", 0))

	_, err = zipWriter.Stat("/empty.txt")
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem)
	_, err = zipWriter.OpenReader("/empty.txt")
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem)
	err = zipWriter.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem)

	// Entries can't be taken back out of an archive being written
	err = zipWriter.Remove("/empty.txt")
	assert.ErrorIs(t, err, errors.ErrUnsupported)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipWriter.Close(), "Close must be idempotent")

	_, err = zipWriter.Stat("/empty.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = zipWriter.OpenReader("/empty.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = zipWriter.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, zipWriter.Touch("/other.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, zipWriter.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, zipWriter.Remove("/empty.txt"), fs.ErrFileSystemClosed)
	_, err = zipWriter.OpenWriter("/other.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// TestZipReader_Symlinks verifies that symbolic link entries are not
// exposed, like tarfs and fs.UnzipToMemFiles skip them. The reader
// used to list a link as regular file whose content was the link target.
func TestZipReader_Symlinks(t *testing.T) {
	ctx := t.Context()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addEntry := func(name string, mode os.FileMode, content string) {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(mode)
		w, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = io.WriteString(w, content)
		require.NoError(t, err)
	}
	addEntry("target.txt", 0o644, "target content")
	addEntry("link.txt", os.ModeSymlink|0o777, "target.txt")
	addEntry("dir/", os.ModeDir|0o755, "")
	addEntry("dir/link-to-parent", os.ModeSymlink|0o777, "..")
	addEntry("fifo", os.ModeNamedPipe|0o644, "")
	require.NoError(t, zw.Close())

	reader := openReader(t, fs.NewMemFile("links.zip", buf.Bytes()))
	root := reader.RootDir()
	for _, name := range []string{"link.txt", "dir/link-to-parent", "fifo"} {
		assert.False(t, root.Join(name).Exists(), "%s must not be exposed", name)
		_, err := root.Join(name).Stat()
		assert.ErrorIs(t, err, os.ErrNotExist, "Stat of %s", name)
		_, err = root.Join(name).ReadAll(ctx)
		assert.ErrorIs(t, err, os.ErrNotExist, "ReadAll of %s", name)
	}
	data, err := root.Join("target.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "target content", data, "regular files are read")
	assert.True(t, root.Join("dir").IsDir(), "directories are read")

	files, err := root.ListDirRecursiveMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{root.Join("target.txt")}, files, "only the regular file is listed")
}
