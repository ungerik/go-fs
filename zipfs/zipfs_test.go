package zipfs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ungerik/go-fs"
)

func TestZipFileSystem(t *testing.T) {
	// Create a temporary zip file for testing
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, tempDir.RemoveRecursive(), "tempDir.RemoveRecursive() should not return an error")
	})

	zipFile := tempDir.Join("test.zip")

	// Create a zip file with test content
	t.Run("CreateZipFile", func(t *testing.T) {
		zipWriter, err := NewWriterFileSystem(zipFile)
		require.NoError(t, err, "NewWriterFileSystem should not error")

		t.Cleanup(func() {
			assert.NoError(t, zipWriter.Close(), "zipWriter.Close() should not error")
		})

		// Create test directory structure
		testDir := "test"
		err = zipWriter.MakeDir(testDir, nil)
		require.NoError(t, err, "MakeDir should not error")

		// Write some test files
		testFilePath := zipWriter.JoinCleanPath(testDir, "test-file.txt")
		writer, err := zipWriter.OpenWriter(testFilePath, nil)
		require.NoError(t, err, "OpenWriter should not error")

		testContent := []byte("Hello, ZipFileSystem!")
		n, err := writer.Write(testContent)
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, len(testContent), n, "Should write all bytes")

		err = writer.Close()
		require.NoError(t, err, "Close writer should not error")

		// Write another file
		testFile2Path := zipWriter.JoinCleanPath(testDir, "test-file-2.txt")
		writer2, err := zipWriter.OpenWriter(testFile2Path, nil)
		require.NoError(t, err, "OpenWriter should not error for second file")

		testContent2 := []byte("Another test file")
		_, err = writer2.Write(testContent2)
		require.NoError(t, err, "Write should not error for second file")

		err = writer2.Close()
		require.NoError(t, err, "Close writer should not error for second file")
	})

	// Now test reading from the zip file
	t.Run("ReadZipFile", func(t *testing.T) {
		zipReader, err := NewReaderFileSystem(zipFile)
		require.NoError(t, err, "NewReaderFileSystem should not error")

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

			id, err := zipReader.ID()
			require.NoError(t, err, "ID() should not error")
			assert.NotEmpty(t, id, "ID() should not be empty")

			rootDir := zipReader.RootDir()
			assert.NotEmpty(t, rootDir, "RootDir() should not be empty")
		})

		t.Run("Stat", func(t *testing.T) {
			testFilePath := zipReader.JoinCleanPath("test", "test-file.txt")
			info, err := zipReader.Stat(testFilePath)
			require.NoError(t, err, "Stat should not error")
			assert.False(t, info.IsDir(), "test-file.txt should not be a directory")
			assert.Greater(t, info.Size(), int64(0), "File size should be greater than 0")
		})

		t.Run("Exists", func(t *testing.T) {
			testFilePath := zipReader.JoinCleanPath("test", "test-file.txt")
			assert.True(t, zipReader.Exists(testFilePath), "test-file.txt should exist")

			nonExistentPath := zipReader.JoinCleanPath("test", "non-existent.txt")
			assert.False(t, zipReader.Exists(nonExistentPath), "non-existent.txt should not exist")
		})

		t.Run("ListDirInfo", func(t *testing.T) {
			var files []*fs.FileInfo
			err := zipReader.ListDirInfo(context.Background(), "test", func(info *fs.FileInfo) error {
				files = append(files, info)
				return nil
			}, nil)
			require.NoError(t, err, "ListDirInfo should not error")
			assert.GreaterOrEqual(t, len(files), 2, "Should list at least 2 files")
		})

		t.Run("OpenReader", func(t *testing.T) {
			testFilePath := zipReader.JoinCleanPath("test", "test-file.txt")
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
			testFilePath := zipReader.JoinCleanPath("test", "test-file.txt")
			_, err := zipReader.OpenReadWriter(testFilePath, nil)
			require.Error(t, err, "OpenReadWriter should error on read-only ZIP")
			assert.Contains(t, err.Error(), "read-only ZIP archive", "Error should mention read-only")
		})
	})

	t.Run("WriteOnlyZipFile", func(t *testing.T) {
		writeOnlyZipFile := tempDir.Join("write-only.zip")
		zipWriter, err := NewWriterFileSystem(writeOnlyZipFile)
		require.NoError(t, err, "NewWriterFileSystem should not error")

		t.Cleanup(func() {
			assert.NoError(t, zipWriter.Close(), "zipWriter.Close() should not error")
		})

		t.Run("OpenReadWriter_WriteOnly", func(t *testing.T) {
			testFilePath := zipWriter.JoinCleanPath("test", "test-file.txt")
			_, err := zipWriter.OpenReadWriter(testFilePath, nil)
			require.Error(t, err, "OpenReadWriter should error on write-only ZIP")
			assert.Contains(t, err.Error(), "write-only ZIP archive", "Error should mention write-only")
		})
	})
}
