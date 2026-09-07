package zipfs

import (
	"context"
	"errors"
	"os"
	"testing"

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
		zipWriter, err := NewWriter(zipFile)
		require.NoError(t, err, "NewWriter should not error")

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
		zipReader, err := NewReader(zipFile)
		require.NoError(t, err, "NewReader should not error")

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
			require.NoError(t, fs.File(zipReader.JoinCleanURI("test", "test-file.txt")).CheckExists(), "test-file.txt should exist")

			err := fs.File(zipReader.JoinCleanURI("test", "non-existent.txt")).CheckExists()
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
			_, err := fs.File(zipReader.JoinCleanURI("test", "test-file.txt")).OpenReadWriter()
			require.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "OpenReadWriter should error on read-only ZIP")
		})
	})

	t.Run("WriteOnlyZipFile", func(t *testing.T) {
		writeOnlyZipFile := tempDir.Join("write-only.zip")
		zipWriter, err := NewWriter(writeOnlyZipFile)
		require.NoError(t, err, "NewWriter should not error")

		t.Cleanup(func() {
			assert.NoError(t, zipWriter.Close(), "zipWriter.Close() should not error")
		})

		t.Run("OpenReadWriter_WriteOnly", func(t *testing.T) {
			_, err := fs.File(zipWriter.JoinCleanURI("test", "test-file.txt")).OpenReadWriter()
			require.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "OpenReadWriter should error on write-only ZIP")
		})
	})
}

func TestZipWriter_SequentialEnforcement(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(context.Background()))
	})

	zipWriter, err := NewWriter(tempDir.Join("seq.zip"))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, zipWriter.Close()) })

	assert.Contains(t, zipWriter.Name(), "Zip writer filesystem", "writer mode Name()")

	// Opening a second writer while the first is still open must fail, because
	// archive/zip can only write to the most recently created entry.
	w1, err := zipWriter.OpenWriter("a.txt", 0)
	require.NoError(t, err)
	_, err = zipWriter.OpenWriter("b.txt", 0)
	require.Error(t, err, "opening a second writer before closing the first must fail")

	// A write to the first (still active) writer succeeds; after closing it a
	// further write must fail rather than silently corrupt the archive.
	_, err = w1.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, w1.Close())
	_, err = w1.Write([]byte("more"))
	require.Error(t, err, "write to a closed entry writer must fail")

	// After closing the first, a second writer can be opened sequentially.
	w2, err := zipWriter.OpenWriter("b.txt", 0)
	require.NoError(t, err)
	_, err = w2.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, w2.Close())

	// Writing to a writer that was superseded by a newer one must fail.
	w3, err := zipWriter.OpenWriter("c.txt", 0)
	require.NoError(t, err)
	require.NoError(t, w3.Close())
	w4, err := zipWriter.OpenWriter("d.txt", 0)
	require.NoError(t, err)
	_, err = w3.Write([]byte("stale")) // w3 already closed -> closed error
	require.Error(t, err)
	require.NoError(t, w4.Close())
}

func TestZipWriter_MakeDirReadOnlyErrors(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(context.Background()))
	})

	zipFile := tempDir.Join("ro.zip")
	zipWriter, err := NewWriter(zipFile)
	require.NoError(t, err)
	w, err := zipWriter.OpenWriter("f.txt", 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, zipWriter.Close())

	zipReader, err := NewReader(zipFile)
	require.NoError(t, err)
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

	zipWriter, err := NewWriter(tempDir.Join("writeonly.zip"))
	require.NoError(t, err)

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
