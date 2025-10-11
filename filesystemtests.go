package fs

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RunFileSystemTests is a comprehensive test helper that tests all methods
// of a FileSystem interface implementation.
//
// name is the expected name of the filesystem that will be verified against fs.Name().
// prefix is the expected prefix of the filesystem that will be verified against fs.Prefix().
// testDir is the directory path within the filesystem where test files will be created.
// It should be empty and will be cleaned up after tests.
func RunFileSystemTests(ctx context.Context, t *testing.T, fs FileSystem, name, prefix, testDir string) {
	t.Helper()

	require.NotEmpty(t, name, "name must not be empty")
	require.NotEmpty(t, prefix, "prefix must not be empty")
	require.NotEmpty(t, testDir, "testDir must not be empty")

	// Basic metadata tests
	t.Run("Metadata", func(t *testing.T) {
		readable, writable := fs.ReadableWritable()
		t.Logf("FileSystem: %s, Readable: %v, Writable: %v", fs.Name(), readable, writable)

		assert.Equal(t, name, fs.Name(), "Name() should match expected name")
		assert.Equal(t, prefix, fs.Prefix(), "Prefix() should match expected prefix")
		require.NotEmpty(t, fs.String(), "String() should not be empty")
		require.NotEmpty(t, fs.Separator(), "Separator() should not be empty")

		id, err := fs.ID()
		require.NoError(t, err, "ID() should not error")
		require.NotEmpty(t, id, "ID() should not be empty")

		rootDir := fs.RootDir()
		require.NotEmpty(t, rootDir, "RootDir() should not be empty")
	})

	// Path manipulation tests
	t.Run("PathManipulation", func(t *testing.T) {
		sep := fs.Separator()

		// JoinCleanPath
		joined := fs.JoinCleanPath("a", "b", "c")
		require.Contains(t, joined, "a", "JoinCleanPath should contain parts")
		require.Contains(t, joined, "c", "JoinCleanPath should contain parts")

		// JoinCleanFile
		file := fs.JoinCleanFile("a", "b", "file.txt")
		require.NotEmpty(t, file, "JoinCleanFile should not be empty")
		require.True(t, strings.HasPrefix(file.URL(), fs.Prefix()), "JoinCleanFile should have prefix")

		// SplitPath
		parts := fs.SplitPath("a" + sep + "b" + sep + "c")
		require.Len(t, parts, 3, "SplitPath should split into correct number of parts")
		assert.Equal(t, "a", parts[0])
		assert.Equal(t, "c", parts[2])

		// SplitDirAndName
		dir, name := fs.SplitDirAndName("a" + sep + "b" + sep + "file.txt")
		require.NotEmpty(t, dir, "SplitDirAndName dir should not be empty")
		assert.Equal(t, "file.txt", name, "SplitDirAndName should extract name")

		// URL and CleanPathFromURI
		url := fs.URL("test/path")
		require.True(t, strings.HasPrefix(url, fs.Prefix()), "URL should have prefix")
		cleanPath := fs.CleanPathFromURI(url)
		require.NotEmpty(t, cleanPath, "CleanPathFromURI should return path")

		// IsAbsPath and AbsPath
		if fs.IsAbsPath("relative/path") {
			t.Log("FileSystem treats relative/path as absolute")
		}
		absPath := fs.AbsPath("relative/path")
		require.NotEmpty(t, absPath, "AbsPath should return path")
	})

	// Pattern matching tests
	t.Run("PatternMatching", func(t *testing.T) {
		matched, err := fs.MatchAnyPattern("file.txt", []string{"*.txt"})
		require.NoError(t, err, "MatchAnyPattern should not error")
		assert.True(t, matched, "*.txt should match file.txt")

		matched, err = fs.MatchAnyPattern("file.txt", []string{"*.go"})
		require.NoError(t, err, "MatchAnyPattern should not error")
		assert.False(t, matched, "*.go should not match file.txt")

		matched, err = fs.MatchAnyPattern("file.txt", nil)
		require.NoError(t, err, "MatchAnyPattern with nil patterns should not error")
		assert.True(t, matched, "nil patterns should match everything")
	})

	// Skip write tests if filesystem is read-only
	_, writable := fs.ReadableWritable()
	if !writable {
		t.Log("Skipping write tests - filesystem is read-only")
		return
	}

	// Directory creation tests
	t.Run("DirectoryOperations", func(t *testing.T) {
		testDirPath := fs.JoinCleanPath(testDir, "test-dir")

		err := fs.MakeDir(testDirPath, nil)
		require.NoError(t, err, "MakeDir should not error")

		info, err := fs.Stat(testDirPath)
		require.NoError(t, err, "Stat should work on created directory")
		assert.True(t, info.IsDir(), "Created path should be a directory")

		// Test MakeAllDirs if supported
		if mafs, ok := fs.(MakeAllDirsFileSystem); ok {
			nestedPath := fs.JoinCleanPath(testDir, "a", "b", "c")
			err = mafs.MakeAllDirs(nestedPath, nil)
			require.NoError(t, err, "MakeAllDirs should not error")

			info, err = fs.Stat(nestedPath)
			require.NoError(t, err, "Stat should work on nested directory")
			assert.True(t, info.IsDir(), "Nested path should be a directory")
		}
	})

	// File write/read tests
	t.Run("FileWriteRead", func(t *testing.T) {
		testFilePath := fs.JoinCleanPath(testDir, "test-file.txt")
		testContent := []byte("Hello, FileSystem!")

		// Write using OpenWriter
		writer, err := fs.OpenWriter(testFilePath, nil)
		require.NoError(t, err, "OpenWriter should not error")
		n, err := writer.Write(testContent)
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, len(testContent), n, "Should write all bytes")
		err = writer.Close()
		require.NoError(t, err, "Close writer should not error")

		// Stat the file
		info, err := fs.Stat(testFilePath)
		require.NoError(t, err, "Stat should work on created file")
		assert.False(t, info.IsDir(), "Created path should not be a directory")
		assert.Equal(t, int64(len(testContent)), info.Size(), "File size should match")

		// Read using OpenReader
		reader, err := fs.OpenReader(testFilePath)
		require.NoError(t, err, "OpenReader should not error")
		readContent, err := io.ReadAll(reader)
		require.NoError(t, err, "ReadAll should not error")
		err = reader.Close()
		require.NoError(t, err, "Close reader should not error")
		assert.Equal(t, testContent, readContent, "Read content should match written content")

		// Test ReadAll if supported
		if rafs, ok := fs.(ReadAllFileSystem); ok {
			content, err := rafs.ReadAll(ctx, testFilePath)
			require.NoError(t, err, "ReadAll should not error")
			assert.Equal(t, testContent, content, "ReadAll content should match")
		}

		// Test WriteAll if supported
		if wafs, ok := fs.(WriteAllFileSystem); ok {
			newContent := []byte("Updated content")
			err := wafs.WriteAll(ctx, testFilePath, newContent, nil)
			require.NoError(t, err, "WriteAll should not error")

			if rafs, ok := fs.(ReadAllFileSystem); ok {
				content, err := rafs.ReadAll(ctx, testFilePath)
				require.NoError(t, err, "ReadAll after WriteAll should not error")
				assert.Equal(t, newContent, content, "Content should be updated")
			}
		}
	})

	// Append tests
	t.Run("FileAppend", func(t *testing.T) {
		if afs, ok := fs.(AppendFileSystem); ok {
			testFilePath := fs.JoinCleanPath(testDir, "append-test.txt")
			content1 := []byte("First line\n")
			content2 := []byte("Second line\n")

			err := afs.Append(ctx, testFilePath, content1, nil)
			require.NoError(t, err, "First Append should not error")

			err = afs.Append(ctx, testFilePath, content2, nil)
			require.NoError(t, err, "Second Append should not error")

			if rafs, ok := fs.(ReadAllFileSystem); ok {
				content, err := rafs.ReadAll(ctx, testFilePath)
				require.NoError(t, err, "ReadAll should not error")
				assert.Equal(t, append(content1, content2...), content, "Appended content should match")
			}
		}

		if awfs, ok := fs.(AppendWriterFileSystem); ok {
			testFilePath := fs.JoinCleanPath(testDir, "append-writer-test.txt")

			writer, err := awfs.OpenAppendWriter(testFilePath, nil)
			require.NoError(t, err, "OpenAppendWriter should not error")
			_, err = writer.Write([]byte("Appended data"))
			require.NoError(t, err, "Write to append writer should not error")
			err = writer.Close()
			require.NoError(t, err, "Close append writer should not error")
		}
	})

	// ReadWriter tests
	t.Run("FileReadWriter", func(t *testing.T) {
		testFilePath := fs.JoinCleanPath(testDir, "readwriter-test.txt")
		initialContent := []byte("Initial content")

		// Create file first
		writer, err := fs.OpenWriter(testFilePath, nil)
		require.NoError(t, err, "OpenWriter should not error")
		_, err = writer.Write(initialContent)
		require.NoError(t, err, "Write should not error")
		err = writer.Close()
		require.NoError(t, err, "Close should not error")

		// Test ReadWriter
		rw, err := fs.OpenReadWriter(testFilePath, nil)
		require.NoError(t, err, "OpenReadWriter should not error")

		// Read
		buf := make([]byte, len(initialContent))
		n, err := rw.Read(buf)
		require.NoError(t, err, "Read should not error")
		assert.Equal(t, len(initialContent), n, "Should read all bytes")
		assert.Equal(t, initialContent, buf, "Read content should match")

		// Seek back to beginning
		pos, err := rw.Seek(0, io.SeekStart)
		require.NoError(t, err, "Seek should not error")
		assert.Equal(t, int64(0), pos, "Should seek to beginning")

		// Write
		newContent := []byte("Updated!")
		n, err = rw.Write(newContent)
		require.NoError(t, err, "Write should not error")
		assert.Equal(t, len(newContent), n, "Should write all bytes")

		err = rw.Close()
		require.NoError(t, err, "Close should not error")
	})

	// ListDir tests
	t.Run("ListDirectory", func(t *testing.T) {
		listTestDir := fs.JoinCleanPath(testDir, "list-test")
		err := fs.MakeDir(listTestDir, nil)
		require.NoError(t, err, "MakeDir should not error")

		// Create some test files
		for _, name := range []string{"file1.txt", "file2.go", "file3.txt"} {
			filePath := fs.JoinCleanPath(listTestDir, name)
			writer, err := fs.OpenWriter(filePath, nil)
			require.NoError(t, err, "OpenWriter should not error for "+name)
			_, err = writer.Write([]byte("test"))
			require.NoError(t, err, "Write should not error")
			err = writer.Close()
			require.NoError(t, err, "Close should not error")
		}

		// Test ListDirInfo
		var fileInfos []*FileInfo
		err = fs.ListDirInfo(ctx, listTestDir, func(info *FileInfo) error {
			fileInfos = append(fileInfos, info)
			return nil
		}, nil)
		require.NoError(t, err, "ListDirInfo should not error")
		assert.Len(t, fileInfos, 3, "Should list all 3 files")

		// Test ListDirInfo with pattern
		fileInfos = nil
		err = fs.ListDirInfo(ctx, listTestDir, func(info *FileInfo) error {
			fileInfos = append(fileInfos, info)
			return nil
		}, []string{"*.txt"})
		require.NoError(t, err, "ListDirInfo with pattern should not error")
		assert.Len(t, fileInfos, 2, "Should list only .txt files")

		// Test ListDirMax if supported
		if ldmfs, ok := fs.(ListDirMaxFileSystem); ok {
			files, err := ldmfs.ListDirMax(ctx, listTestDir, -1, nil)
			require.NoError(t, err, "ListDirMax should not error")
			assert.Len(t, files, 3, "Should list all 3 files")

			files, err = ldmfs.ListDirMax(ctx, listTestDir, 2, nil)
			require.NoError(t, err, "ListDirMax with limit should not error")
			assert.Len(t, files, 2, "Should list only 2 files")
		}

		// Test ListDirInfoRecursive if supported
		if ldrfs, ok := fs.(ListDirRecursiveFileSystem); ok {
			// Create a subdirectory with a file
			subDir := fs.JoinCleanPath(listTestDir, "subdir")
			err = fs.MakeDir(subDir, nil)
			require.NoError(t, err, "MakeDir for subdir should not error")

			subFilePath := fs.JoinCleanPath(subDir, "subfile.txt")
			writer, err := fs.OpenWriter(subFilePath, nil)
			require.NoError(t, err, "OpenWriter in subdir should not error")
			_, err = writer.Write([]byte("test"))
			require.NoError(t, err, "Write should not error")
			err = writer.Close()
			require.NoError(t, err, "Close should not error")

			fileInfos = nil
			err = ldrfs.ListDirInfoRecursive(ctx, listTestDir, func(info *FileInfo) error {
				fileInfos = append(fileInfos, info)
				return nil
			}, nil)
			require.NoError(t, err, "ListDirInfoRecursive should not error")
			assert.GreaterOrEqual(t, len(fileInfos), 4, "Should list at least 4 files recursively")
		}
	})

	// Truncate tests
	t.Run("FileTruncate", func(t *testing.T) {
		if tfs, ok := fs.(TruncateFileSystem); ok {
			testFilePath := fs.JoinCleanPath(testDir, "truncate-test.txt")
			initialContent := []byte("Hello, World! This is a test.")

			writer, err := fs.OpenWriter(testFilePath, nil)
			require.NoError(t, err, "OpenWriter should not error")
			_, err = writer.Write(initialContent)
			require.NoError(t, err, "Write should not error")
			err = writer.Close()
			require.NoError(t, err, "Close should not error")

			// Truncate to smaller size
			err = tfs.Truncate(testFilePath, 5)
			require.NoError(t, err, "Truncate should not error")

			info, err := fs.Stat(testFilePath)
			require.NoError(t, err, "Stat should not error")
			assert.Equal(t, int64(5), info.Size(), "File should be truncated to 5 bytes")

			// Truncate to larger size (should pad with zeros)
			err = tfs.Truncate(testFilePath, 20)
			require.NoError(t, err, "Truncate to larger size should not error")

			info, err = fs.Stat(testFilePath)
			require.NoError(t, err, "Stat should not error")
			assert.Equal(t, int64(20), info.Size(), "File should be expanded to 20 bytes")
		}
	})

	// Exists tests
	t.Run("FileExists", func(t *testing.T) {
		if efs, ok := fs.(ExistsFileSystem); ok {
			existingPath := fs.JoinCleanPath(testDir, "exists-test.txt")
			nonExistingPath := fs.JoinCleanPath(testDir, "does-not-exist.txt")

			// Create a file
			writer, err := fs.OpenWriter(existingPath, nil)
			require.NoError(t, err, "OpenWriter should not error")
			err = writer.Close()
			require.NoError(t, err, "Close should not error")

			assert.True(t, efs.Exists(existingPath), "Exists should return true for existing file")
			assert.False(t, efs.Exists(nonExistingPath), "Exists should return false for non-existing file")
		}
	})

	// Touch tests
	t.Run("FileTouch", func(t *testing.T) {
		if tfs, ok := fs.(TouchFileSystem); ok {
			testFilePath := fs.JoinCleanPath(testDir, "touch-test.txt")

			err := tfs.Touch(testFilePath, nil)
			require.NoError(t, err, "Touch should not error")

			info, err := fs.Stat(testFilePath)
			require.NoError(t, err, "Stat should work on touched file")
			assert.False(t, info.IsDir(), "Touched file should not be a directory")
		}
	})

	// Remove tests
	t.Run("FileRemove", func(t *testing.T) {
		testFilePath := fs.JoinCleanPath(testDir, "remove-test.txt")

		// Create file
		writer, err := fs.OpenWriter(testFilePath, nil)
		require.NoError(t, err, "OpenWriter should not error")
		err = writer.Close()
		require.NoError(t, err, "Close should not error")

		// Verify it exists
		_, err = fs.Stat(testFilePath)
		require.NoError(t, err, "File should exist before removal")

		// Remove it
		err = fs.Remove(testFilePath)
		require.NoError(t, err, "Remove should not error")

		// Verify it's gone
		_, err = fs.Stat(testFilePath)
		assert.Error(t, err, "Stat should error after removal")
	})

	// IsHidden tests
	t.Run("IsHidden", func(t *testing.T) {
		hiddenPath := fs.JoinCleanPath(testDir, ".hidden")
		isHidden := fs.IsHidden(hiddenPath)
		t.Logf("IsHidden for %q: %v", hiddenPath, isHidden)
		// Note: Don't assert as behavior may vary by filesystem
	})

	// IsSymbolicLink tests
	t.Run("IsSymbolicLink", func(t *testing.T) {
		regularPath := fs.JoinCleanPath(testDir, "regular.txt")
		isSymlink := fs.IsSymbolicLink(regularPath)
		t.Logf("IsSymbolicLink for %q: %v", regularPath, isSymlink)
		// Note: Don't assert as behavior may vary by filesystem
	})

	// Optional interface tests
	t.Run("OptionalInterfaces", func(t *testing.T) {
		if cfs, ok := fs.(CopyFileSystem); ok {
			t.Log("FileSystem implements CopyFileSystem")
			srcPath := fs.JoinCleanPath(testDir, "copy-src.txt")
			dstPath := fs.JoinCleanPath(testDir, "copy-dst.txt")

			// Create source file
			writer, err := fs.OpenWriter(srcPath, nil)
			require.NoError(t, err, "OpenWriter should not error")
			_, err = writer.Write([]byte("copy test"))
			require.NoError(t, err, "Write should not error")
			err = writer.Close()
			require.NoError(t, err, "Close should not error")

			// Copy
			var buf []byte
			err = cfs.CopyFile(ctx, srcPath, dstPath, &buf)
			require.NoError(t, err, "CopyFile should not error")

			// Verify
			_, err = fs.Stat(dstPath)
			require.NoError(t, err, "Copied file should exist")
		}

		if mfs, ok := fs.(MoveFileSystem); ok {
			t.Log("FileSystem implements MoveFileSystem")
			_ = mfs // Move requires changing paths, complex to test generically
		}

		if rfs, ok := fs.(RenameFileSystem); ok {
			t.Log("FileSystem implements RenameFileSystem")
			_ = rfs // Rename requires changing paths, complex to test generically
		}

		if vfs, ok := fs.(VolumeNameFileSystem); ok {
			t.Log("FileSystem implements VolumeNameFileSystem")
			vol := vfs.VolumeName("C:\\test")
			t.Logf("VolumeName: %q", vol)
		}

		if wfs, ok := fs.(WatchFileSystem); ok {
			t.Log("FileSystem implements WatchFileSystem")
			_ = wfs // Watch requires async testing, complex to test generically
		}

		if ufs, ok := fs.(UserFileSystem); ok {
			t.Log("FileSystem implements UserFileSystem")
			_ = ufs // User operations require permissions, complex to test generically
		}

		if gfs, ok := fs.(GroupFileSystem); ok {
			t.Log("FileSystem implements GroupFileSystem")
			_ = gfs // Group operations require permissions, complex to test generically
		}

		if pfs, ok := fs.(PermissionsFileSystem); ok {
			t.Log("FileSystem implements PermissionsFileSystem")
			_ = pfs // Permission operations complex to test generically
		}
	})

	// Close test
	t.Run("Close", func(t *testing.T) {
		err := fs.Close()
		// Don't require NoError as some filesystems may not be closable
		// or may already be closed by cleanup
		t.Logf("Close returned: %v", err)
	})
}
