package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fsimpl"
)

func TestInvalidFile(t *testing.T) {
	require.False(t, InvalidFile.IsDir(), "InvalidFile does not exist")

	require.Equal(t, InvalidFile, InvalidFile.Dir(), "dir of InvalidFile is still an InvalidFile")
	dir, name := InvalidFile.DirAndName()
	require.Equal(t, InvalidFile, dir, "dir of InvalidFile is still an InvalidFile")
	require.Equal(t, "", name, "name of InvalidFile is empty string")

	require.Equal(t, InvalidFileSystem(""), InvalidFile.FileSystem(), "InvalidFile has an InvalidFileSystem")

	_, err := InvalidFile.OpenReader()
	require.Equal(t, ErrEmptyPath, err, "can't open InvalidFile")
}

func TestFileMakeAllDirs(t *testing.T) {
	checkDir := func(dir File) {
		if !dir.Exists() {
			t.Fatalf("dir does not exist: %s", dir)
		}
		if !dir.IsDir() {
			t.Fatalf("not a directory: %s", dir)
		}
	}

	baseDir := TempDir()

	err := baseDir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}

	file := baseDir.Join(fsimpl.RandomString())
	err = file.Touch()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Remove()

	err = file.MakeAllDirs()
	if !errors.As(err, new(ErrIsNotDirectory)) {
		t.Fatalf("should be ErrIsNotDirectory but is: %s", err)
	}

	pathParts := make([]string, 5)
	for i := range pathParts {
		pathParts[i] = fsimpl.RandomString()
	}

	dir := baseDir.Join(pathParts...)

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}
	err = dir.Dir().Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = baseDir.Join(pathParts[0]).RemoveRecursive()
	if err != nil {
		t.Fatal(err)
	}
}

func Test_FileJoin(t *testing.T) {
	exptectedPaths := []string{
		"/1/2/3/4/5",
		"/1/2/3/4",
		"/1/2/3",
		"/1/2",
		"/1",
		"/",
		"/",
	}

	f := File("/").Join("1", "2", "3", "4", "5")

	for _, exp := range exptectedPaths {
		assert.Equal(t, exp, f.LocalPath())
		// Up one directory
		f = f.Dir()
	}
}

func TestFile_Ext(t *testing.T) {
	tests := []struct {
		file File
		want string
	}{
		{file: "image.png", want: ".png"},
		{file: "image.66.png", want: ".png"},
		{file: "image", want: ""},
		{file: JoinCleanFilePath("dir.with.ext", "file"), want: ""},
		{file: JoinCleanFilePath("dir.with.ext", "file.ext"), want: ".ext"},
	}
	for _, tt := range tests {
		t.Run(string(tt.file), func(t *testing.T) {
			if got := tt.file.Ext(); got != tt.want {
				t.Errorf("File.Ext() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_TrimExt(t *testing.T) {
	tests := []struct {
		file File
		want File
	}{
		{file: "image.png", want: "image"},
		{file: "image.66.png", want: "image.66"},
		{file: "image", want: "image"},
		{file: JoinCleanFilePath("dir.with.ext", "file"), want: JoinCleanFilePath("dir.with.ext", "file")},
		{file: JoinCleanFilePath("dir.with.ext", "file.ext"), want: JoinCleanFilePath("dir.with.ext", "file")},
	}
	for _, tt := range tests {
		t.Run(string(tt.file), func(t *testing.T) {
			if got := tt.file.TrimExt(); got != tt.want {
				t.Errorf("File.TrimExt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_Watch(t *testing.T) {
	Local.WatchEventLogger = LoggerFunc(func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	Local.WatchErrorLogger = LoggerFunc(func(format string, args ...any) {
		t.Errorf(format, args...)
	})
	const sleepDurationForCallback = time.Millisecond * 10
	var (
		dir       = File(t.TempDir())
		gotFiles  []File
		gotEvents []Event
	)
	cancel, err := dir.Watch(func(file File, event Event) {
		gotFiles = append(gotFiles, file)
		gotEvents = append(gotEvents, event)
	})
	require.NoError(t, err, "dir.Watch")

	newFile := dir.Join("newFile")
	err = newFile.Touch()
	require.NoError(t, err, "newFile.Touch")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	renamedFile, err := newFile.Rename("renamedFile")
	require.NoError(t, err, "newFile.Rename")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	err = renamedFile.Remove()
	require.NoError(t, err, "renamedFile.Remove")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	assert.Equal(t, []File{newFile, renamedFile, newFile, renamedFile}, gotFiles)
	assert.Equal(t, []Event{eventCreate, eventCreate, eventRename, eventRemove}, gotEvents)

	err = cancel()
	assert.NoError(t, err, "cancel watch")
}

func TestFile_ListDirInfoRecursiveContext(t *testing.T) {
	// Create test directory structure
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive() })

	// Create a nested directory structure:
	// dir/
	//   file1.txt
	//   file2.log
	//   subdir1/
	//     file3.txt
	//     file4.log
	//     subdir2/
	//       file5.txt
	//       file6.md
	file1 := dir.Join("file1.txt")
	file2 := dir.Join("file2.log")
	subdir1 := dir.Join("subdir1")
	file3 := subdir1.Join("file3.txt")
	file4 := subdir1.Join("file4.log")
	subdir2 := subdir1.Join("subdir2")
	file5 := subdir2.Join("file5.txt")
	file6 := subdir2.Join("file6.md")

	require.NoError(t, subdir2.MakeAllDirs())
	require.NoError(t, file1.Touch())
	require.NoError(t, file2.Touch())
	require.NoError(t, file3.Touch())
	require.NoError(t, file4.Touch())
	require.NoError(t, file5.Touch())
	require.NoError(t, file6.Touch())

	t.Run("all files without pattern", func(t *testing.T) {
		var files []File
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			files = append(files, info.File)
			return nil
		})
		require.NoError(t, err)

		expectedFiles := []File{file1, file2, file3, file4, file5, file6}
		sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
		sort.Slice(expectedFiles, func(i, j int) bool { return expectedFiles[i].Path() < expectedFiles[j].Path() })
		assert.Equal(t, expectedFiles, files)
	})

	t.Run("filter by pattern *.txt", func(t *testing.T) {
		var files []File
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			files = append(files, info.File)
			return nil
		}, "*.txt")
		require.NoError(t, err)

		expectedFiles := []File{file1, file3, file5}
		sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
		sort.Slice(expectedFiles, func(i, j int) bool { return expectedFiles[i].Path() < expectedFiles[j].Path() })
		assert.Equal(t, expectedFiles, files)
	})

	t.Run("filter by pattern *.log", func(t *testing.T) {
		var files []File
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			files = append(files, info.File)
			return nil
		}, "*.log")
		require.NoError(t, err)

		expectedFiles := []File{file2, file4}
		sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
		sort.Slice(expectedFiles, func(i, j int) bool { return expectedFiles[i].Path() < expectedFiles[j].Path() })
		assert.Equal(t, expectedFiles, files)
	})

	t.Run("filter by multiple patterns", func(t *testing.T) {
		var files []File
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			files = append(files, info.File)
			return nil
		}, "*.txt", "*.md")
		require.NoError(t, err)

		expectedFiles := []File{file1, file3, file5, file6}
		sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
		sort.Slice(expectedFiles, func(i, j int) bool { return expectedFiles[i].Path() < expectedFiles[j].Path() })
		assert.Equal(t, expectedFiles, files)
	})

	t.Run("callback returns error", func(t *testing.T) {
		expectedErr := errors.New("callback error")
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			return expectedErr
		})
		assert.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := dir.ListDirInfoRecursiveContext(ctx, func(info *FileInfo) error {
			return nil
		})
		// Should return context.Canceled or similar error
		assert.Error(t, err)
	})

	t.Run("empty file path", func(t *testing.T) {
		emptyFile := File("")
		err := emptyFile.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			return nil
		})
		assert.Equal(t, ErrEmptyPath, err)
	})

	t.Run("non-existent directory", func(t *testing.T) {
		nonExistent := dir.Join("nonexistent")
		err := nonExistent.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			return nil
		})
		// Should return an error (likely file not found)
		assert.Error(t, err)
	})

	t.Run("verify info fields", func(t *testing.T) {
		var infos []*FileInfo
		err := dir.ListDirInfoRecursiveContext(context.Background(), func(info *FileInfo) error {
			infos = append(infos, info)
			return nil
		}, "*.txt")
		require.NoError(t, err)

		// Verify all infos have correct fields
		for _, info := range infos {
			assert.NotEmpty(t, info.Name, "Name should not be empty")
			assert.False(t, info.IsDir, "Files should not be directories")
			assert.True(t, strings.HasSuffix(info.Name, ".txt"), "Name should end with .txt")
		}
	})
}

func TestFile_ListDir(t *testing.T) {
	dir, err := MakeTempDir()
	require.NoError(t, err, "MakeTempDir")
	t.Cleanup(func() { dir.RemoveRecursive() })

	files := map[File]bool{
		dir.Join("a"): true,
		dir.Join("b"): true,
		dir.Join("c"): true,
	}

	for file := range files {
		err := file.Touch()
		require.NoError(t, err)
	}

	err = dir.ListDir(func(file File) error {
		if !files[file] {
			t.Errorf("unexpected file: %s", file)
		}
		delete(files, file)
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, files, "not all files listed")
}

func TestFile_ListDirIter(t *testing.T) {
	dir, err := MakeTempDir()
	require.NoError(t, err, "MakeTempDir")
	t.Cleanup(func() { dir.RemoveRecursive() })

	files := map[File]bool{
		dir.Join("a"): true,
		dir.Join("b"): true,
		dir.Join("c"): true,
	}

	for file := range files {
		err := file.Touch()
		require.NoError(t, err)
	}

	for file, err := range dir.ListDirIter() {
		require.NoError(t, err, "ListDirIter should not return an error")
		if !files[file] {
			t.Errorf("unexpected file: %s", file)
		}
		delete(files, file)
	}
	require.NoError(t, err)
	require.Empty(t, files, "not all files listed")
}

func TestFile_Glob(t *testing.T) {
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive() })
	xDir := dir.Join("a", "b", "c", "Hello", "World", "x")
	yDir := dir.Join("a", "b", "c", "Hello", "World", "y")
	require.NoError(t, xDir.MakeAllDirs())
	require.NoError(t, yDir.MakeAllDirs())
	cFile := dir.Join("a", "b", "c", "cFile")
	require.NoError(t, cFile.Touch())
	xFile1 := xDir.Join("file1.txt")
	require.NoError(t, xFile1.Touch())
	xFile2 := xDir.Join("file2.txt")
	require.NoError(t, xFile2.Touch())
	xFile3 := xDir.Join("file3.txt")
	require.NoError(t, xFile3.Touch())

	type result struct {
		file   File
		values []string
	}

	tests := []struct {
		name    string
		file    File
		pattern string
		want    []result
		wantErr bool
	}{
		{
			name:    "invalid file",
			file:    "",
			pattern: "",
			want:    nil,
		},
		{
			name:    "empty pattern",
			file:    dir,
			pattern: "",
			want:    []result{{dir, nil}},
		},
		{
			name:    "no wildcard pattern",
			file:    dir,
			pattern: "a/b/c/Hello/World/x/file1.txt",
			want: []result{
				{xFile1, nil},
			},
		},
		{
			name:    "no wildcard non-canonical pattern",
			file:    dir,
			pattern: "/./a/b//c/Hello/./././/World/x/file1.txt",
			want: []result{
				{xFile1, nil},
			},
		},
		{
			name:    "root files",
			file:    dir,
			pattern: "*",
			want: []result{
				{dir.Join("a"), []string{"a"}},
			},
		},
		{
			name:    "root dirs",
			file:    dir,
			pattern: "*/",
			want: []result{
				{dir.Join("a"), []string{"a"}},
			},
		},
		{
			name:    "file and dir",
			file:    dir,
			pattern: "./a/b/c/*",
			want: []result{
				{dir.Join("a", "b", "c", "Hello"), []string{"Hello"}},
				{cFile, []string{"cFile"}},
			},
		},
		{
			name:    "directories only",
			file:    dir,
			pattern: "./a/b/c/*/",
			want: []result{
				{dir.Join("a", "b", "c", "Hello"), []string{"Hello"}},
			},
		},
		{
			name:    "complexer pattern",
			file:    dir,
			pattern: "*/b/c/*/W???d/x/file[1-2].txt",
			want: []result{
				{xFile1, []string{"a", "Hello", "World", "file1.txt"}},
				{xFile2, []string{"a", "Hello", "World", "file2.txt"}},
			},
		},
		{
			name:    "invalid empty path base file",
			file:    "",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/*",
			want:    nil,
		},
		{
			name:    "root dir base",
			file:    "/",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/*.txt",
			want: []result{
				{xFile1, []string{"file1.txt"}},
				{xFile2, []string{"file2.txt"}},
				{xFile3, []string{"file3.txt"}},
			},
		},
		// Errors
		{
			name:    "malformed pattern",
			file:    dir,
			pattern: "a/b/c/Hello/World/x/[file1.txt",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIter, err := tt.file.Glob(tt.pattern)
			if tt.wantErr {
				require.Error(t, err, "File.Glob")
				return
			}
			require.NoError(t, err, "File.Glob")
			var got []result
			for file, values := range gotIter {
				require.Truef(t, file.Exists(), "file %s does not exist", file)
				got = append(got, result{file, values})
			}
			sort.Slice(got, func(i, j int) bool { return got[i].file.LocalPath() < got[j].file.LocalPath() })
			require.Equal(t, tt.want, got, "file path sorted results")
		})
	}
}

func TestGlob(t *testing.T) {
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive() })
	xDir := dir.Join("a", "b", "c", "Hello", "World", "x")
	yDir := dir.Join("a", "b", "c", "Hello", "World", "y")
	require.NoError(t, xDir.MakeAllDirs())
	require.NoError(t, yDir.MakeAllDirs())
	cFile := dir.Join("a", "b", "c", "cFile")
	require.NoError(t, cFile.Touch())
	xFile1 := xDir.Join("file1.txt")
	require.NoError(t, xFile1.Touch())
	xFile2 := xDir.Join("file2.txt")
	require.NoError(t, xFile2.Touch())
	xFile3 := xDir.Join("file3.txt")
	require.NoError(t, xFile3.Touch())

	type result struct {
		file   File
		values []string
	}

	tests := []struct {
		name    string
		pattern string
		want    []result
		wantErr bool
	}{
		{
			name:    "empty pattern for current dir",
			pattern: "",
			want:    []result{{".", nil}},
		},
		{
			name:    "no wildcard pattern",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/file1.txt",
			want: []result{
				{xFile1, nil},
			},
		},
		{
			name:    "no wildcard non-canonical pattern",
			pattern: dir.PathWithSlashes() + "/./a/b//c/Hello/./././/World/x/file1.txt",
			want: []result{
				{xFile1, nil},
			},
		},
		{
			name:    "root files",
			pattern: dir.PathWithSlashes() + "/*",
			want: []result{
				{dir.Join("a"), []string{"a"}},
			},
		},
		{
			name:    "root dirs",
			pattern: dir.PathWithSlashes() + "/*/",
			want: []result{
				{dir.Join("a"), []string{"a"}},
			},
		},
		{
			name:    "file and dir",
			pattern: dir.PathWithSlashes() + "/a/b/c/*",
			want: []result{
				{dir.Join("a", "b", "c", "Hello"), []string{"Hello"}},
				{cFile, []string{"cFile"}},
			},
		},
		{
			name:    "directories only",
			pattern: dir.PathWithSlashes() + "/a/b/c/*/",
			want: []result{
				{dir.Join("a", "b", "c", "Hello"), []string{"Hello"}},
			},
		},
		{
			name:    "complexer pattern",
			pattern: dir.PathWithSlashes() + "/*/b/c/*/W???d/x/file[1-2].txt",
			want: []result{
				{xFile1, []string{"a", "Hello", "World", "file1.txt"}},
				{xFile2, []string{"a", "Hello", "World", "file2.txt"}},
			},
		},
		{
			name:    "*.txt",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/*.txt",
			want: []result{
				{xFile1, []string{"file1.txt"}},
				{xFile2, []string{"file2.txt"}},
				{xFile3, []string{"file3.txt"}},
			},
		},
		// Errors
		{
			name:    "malformed pattern",
			pattern: "/a/b/c/Hello/World/x/[file1.txt",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIter, err := Glob(tt.pattern)
			if tt.wantErr {
				require.Error(t, err, "Glob")
				return
			}
			require.NoError(t, err, "Glob")
			var got []result
			for file, values := range gotIter {
				require.Truef(t, file.Exists(), "file %s does not exist", file)
				got = append(got, result{file, values})
			}
			sort.Slice(got, func(i, j int) bool { return got[i].file.LocalPath() < got[j].file.LocalPath() })
			require.Equal(t, tt.want, got, "file path sorted results")
		})
	}
}

// mockFileInfo implements io/fs.FileInfo for testing
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return m.modTime }
func (m *mockFileInfo) IsDir() bool        { return m.isDir }
func (m *mockFileInfo) Sys() any           { return nil }

// mockReadCloser implements iofs.File for testing
type mockReadCloser struct {
	io.ReadCloser
}

func (m *mockReadCloser) Stat() (iofs.FileInfo, error) {
	return &mockFileInfo{name: "test.txt", size: 12}, nil
}

func (m *mockReadCloser) ReadDir(n int) ([]iofs.DirEntry, error) {
	return nil, errors.New("not a directory")
}

// mockWriteCloser implements WriteCloser for testing
type mockWriteCloser struct {
	io.Writer
}

func (m *mockWriteCloser) Close() error {
	return nil
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// TestFile comprehensively tests File methods using MockFileSystem with different Permissions
func TestFile(t *testing.T) {
	// Helper to create a mock file system with minimal setup
	createMockFS := func(prefix string) *MockFullyFeaturedFileSystem {
		mockFS := &MockFullyFeaturedFileSystem{}
		mockFS.MockFileSystem = MockFileSystem{
			MockPrefix: prefix,
			MockReadableWritable: func() (bool, bool) {
				return true, true // Mock filesystem is both readable and writable
			},
			MockURL: func(path string) string {
				// Simply concatenate prefix and path (path should have leading / for absolute paths)
				return prefix + strings.TrimPrefix(path, "/")
			},
			MockCleanPathFromURI: func(uri string) string {
				// Simple implementation that removes the prefix and returns the path
				if strings.HasPrefix(uri, prefix) {
					path := strings.TrimPrefix(uri, prefix)
					// Ensure path starts with /
					if !strings.HasPrefix(path, "/") {
						path = "/" + path
					}
					return path
				}
				return uri
			},
			MockSplitDirAndName: func(path string) (string, string) {
				// Simple implementation that splits on the last /
				lastSlash := strings.LastIndex(path, "/")
				if lastSlash == -1 {
					return "", path
				}
				return path[:lastSlash], path[lastSlash+1:]
			},
			MockJoinCleanFile: func(elements ...string) File {
				// Simple implementation that joins elements with /
				path := strings.Join(elements, "/")
				// Clean up double slashes
				path = strings.ReplaceAll(path, "//", "/")
				return File(prefix + path)
			},
			MockJoinCleanPath: func(elements ...string) string {
				// Simple implementation that joins elements with /
				path := strings.Join(elements, "/")
				// Clean up double slashes
				path = strings.ReplaceAll(path, "//", "/")
				return path
			},
			MockStat: func(path string) (iofs.FileInfo, error) {
				// Default implementation that returns a file info
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					size:  100,
					mode:  0644,
				}, nil
			},
			MockMakeDir: func(dirPath string, perm []Permissions) error {
				return nil
			},
			MockRemove: func(filePath string) error {
				return nil
			},
			MockAbsPath: func(path string) string {
				// For mock purposes, just ensure it starts with / (but only add if not present)
				if !strings.HasPrefix(path, "/") {
					return "/" + path
				}
				return path
			},
			MockSeparator: func() string {
				return "/"
			},
			MockIsHidden: func(path string) bool {
				// Check if filename starts with dot
				_, name := mockFS.MockSplitDirAndName(path)
				return strings.HasPrefix(name, ".")
			},
			MockIsAbsPath: func(path string) bool {
				return strings.HasPrefix(path, "/")
			},
			MockIsSymbolicLink: func(path string) bool {
				// Mock filesystem doesn't support symbolic links
				return false
			},
			MockRootDir: func() File {
				return File(prefix + "/")
			},
			MockOpenReader: func(filePath string) (ReadCloser, error) {
				// Return a reader with empty content by default
				data, err := mockFS.MockReadAll(context.Background(), filePath)
				if err != nil {
					return nil, err
				}
				return &mockReadCloser{ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
			},
		}
		mockFS.MockExists = func(filePath string) bool {
			_, err := mockFS.MockStat(filePath)
			return err == nil
		}
		mockFS.MockOpenAppendWriter = func(filePath string, perm []Permissions) (WriteCloser, error) {
			// Emulate the fallback implementation: read existing content,
			// return a buffer that calls WriteAll on close
			current, err := mockFS.MockReadAll(context.Background(), filePath)
			if err != nil {
				current = []byte{} // Empty if file doesn't exist
			}
			var fileBuffer *fsimpl.FileBuffer
			fileBuffer = fsimpl.NewFileBufferWithClose(current, func() error {
				return mockFS.MockWriteAll(context.Background(), filePath, fileBuffer.Bytes(), perm)
			})
			return fileBuffer, nil
		}
		mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
			// Default implementation that returns empty data
			return []byte{}, nil
		}
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
			// Default implementation that returns empty list
			return []File{}, nil
		}
		mockFS.MockVolumeName = func(filePath string) string {
			// Mock file systems don't have volumes
			return ""
		}
		mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
			// Default implementation that returns empty directory
			return nil
		}
		mockFS.MockSetPermissions = func(filePath string, perm Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockUser = func(filePath string) (string, error) {
			// Default implementation that returns a test user
			return "testuser", nil
		}
		mockFS.MockSetUser = func(filePath string, user string) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockGroup = func(filePath string) (string, error) {
			// Default implementation that returns a test group
			return "testgroup", nil
		}
		mockFS.MockSetGroup = func(filePath string, group string) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockTouch = func(filePath string, perm []Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockWatch = func(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
			// Default implementation that returns a no-op cancel function
			return func() error { return nil }, nil
		}
		mockFS.MockTruncate = func(filePath string, size int64) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockRename = func(filePath string, newName string) (newPath string, err error) {
			// Default implementation that returns a new path
			dir, _ := mockFS.MockSplitDirAndName(filePath)
			return dir + "/" + newName, nil
		}
		mockFS.MockMove = func(filePath string, destinationPath string) error {
			// Default implementation that does nothing
			return nil
		}
		return mockFS
	}

	// Test different permission combinations for each method
	permissionTests := []struct {
		name        string
		permissions []Permissions
	}{
		{"NoPermissions", []Permissions{}},
		{"SinglePermission", []Permissions{0644}},
		{"MultiplePermissions", []Permissions{0644, 0755}},
		{"ReadOnly", []Permissions{0444}},
		{"WriteOnly", []Permissions{0222}},
		{"ExecuteOnly", []Permissions{0111}},
		{"FullPermissions", []Permissions{0777}},
		{"UserReadWrite", []Permissions{0600}},
		{"GroupReadWrite", []Permissions{0660}},
		{"OtherReadWrite", []Permissions{0606}},
		{"StickyBit", []Permissions{01777}},
		{"SetUID", []Permissions{04755}},
		{"SetGID", []Permissions{02755}},
	}

	t.Run("MakeDir", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerms []Permissions
				mockFS.MockMakeDir = func(dirPath string, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", dirPath)
					return nil
				}

				err := file.MakeDir(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("MakeAllDirs", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// Mock Stat to return file not found
				mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
					return nil, errors.New("file not found")
				}

				var capturedPerms []Permissions
				mockFS.MockMakeDir = func(dirPath string, perm []Permissions) error {
					capturedPerms = perm
					return nil
				}

				err := file.MakeAllDirs(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("OpenWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerms []Permissions
				mockFS.MockOpenWriter = func(filePath string, perm []Permissions) (WriteCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				writer, err := file.OpenWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("OpenAppendWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// Mock ReadAll to return existing content
				mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
					return []byte("existing content"), nil
				}

				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					return nil
				}

				writer, err := file.OpenAppendWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)

				// Close to trigger WriteAll
				err = writer.Close()
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("OpenReadWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerms []Permissions
				mockFS.MockOpenReadWriter = func(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				readWriter, err := file.OpenReadWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, readWriter)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteAll", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("test content")
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAll(testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteAllContext", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("test content")
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					require.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAllContext(context.Background(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteAllString", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "test content"
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllString(testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteAllStringContext", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "test content"
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllStringContext(context.Background(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("Append", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("appended content")
				var capturedPerms []Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.Append(context.Background(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("AppendString", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "appended content"
				var capturedPerms []Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.AppendString(context.Background(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteJSON", func(t *testing.T) {
		// WriteJSON doesn't accept permissions, so we just test it once
		// Create mock file system for this test with only needed functions
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		testData := map[string]any{"name": "test", "value": 123}
		var capturedPerms []Permissions
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
			capturedPerms = perm
			assert.Equal(t, "/test/path/to/file.txt", filePath)
			// Verify it's valid JSON
			assert.Contains(t, string(data), `"name":"test"`)
			assert.Contains(t, string(data), `"value":123`)
			return nil
		}

		err := file.WriteJSON(context.Background(), testData)
		require.NoError(t, err)
		// WriteJSON doesn't support permissions, so it should pass nil
		require.Nil(t, capturedPerms)
	})

	t.Run("WriteXML", func(t *testing.T) {
		// WriteXML doesn't accept permissions, so we just test it once
		// Create mock file system for this test with only needed functions
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		testData := struct {
			XMLName struct{} `xml:"root"`
			Name    string   `xml:"name"`
			Value   int      `xml:"value"`
		}{Name: "test", Value: 123}

		var capturedPerms []Permissions
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
			capturedPerms = perm
			assert.Equal(t, "/test/path/to/file.txt", filePath)
			// Verify it's valid XML with header
			assert.Contains(t, string(data), `<?xml version="1.0" encoding="UTF-8"?>`)
			assert.Contains(t, string(data), `<name>test</name>`)
			assert.Contains(t, string(data), `<value>123</value>`)
			return nil
		}

		err := file.WriteXML(context.Background(), testData)
		require.NoError(t, err)
		// WriteXML doesn't support permissions, so it should pass nil
		require.Nil(t, capturedPerms)
	})

	t.Run("ReadFrom", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testReader := strings.NewReader("test content")

				// Mock Stat to return existing file with permissions
				mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
					return &mockFileInfo{
						name: "file.txt",
						size: 0,
						mode: 0644,
					}, nil
				}

				var capturedPerms []Permissions
				mockFS.MockOpenWriter = func(filePath string, perm []Permissions) (WriteCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				n, err := file.ReadFrom(testReader)
				require.NoError(t, err)
				assert.Equal(t, int64(12), n) // "test content" length
				// ReadFrom should use existing file permissions, not the test permissions
				assert.Equal(t, []Permissions{0644}, capturedPerms)
			})
		}
	})

	t.Run("GobDecode", func(t *testing.T) {
		// GobDecode doesn't accept permissions, so we just test it once
		// Create mock file system for this test with only needed functions
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		// First encode some data
		mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
			return []byte("test content"), nil
		}

		encodedData, err := file.GobEncode()
		require.NoError(t, err)

		// Now decode it - GobDecode doesn't take permissions, but WriteAll does
		var capturedPerms []Permissions
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
			capturedPerms = perm
			assert.Equal(t, []byte("test content"), data)
			return nil
		}

		err = file.GobDecode(encodedData)
		require.NoError(t, err)
		// GobDecode doesn't support permissions, so it should pass nil
		require.Nil(t, capturedPerms)
	})

	// Test methods that don't take permissions but should still work
	t.Run("NonPermissionMethods", func(t *testing.T) {
		// Create a shared mock file system for all NonPermissionMethods subtests
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		t.Run("FileSystem", func(t *testing.T) {
			fs := file.FileSystem()
			require.Equal(t, mockFS, fs)
		})

		t.Run("ParseRawURI", func(t *testing.T) {
			fs, path := file.ParseRawURI()
			require.Equal(t, mockFS, fs)
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("RawURI", func(t *testing.T) {
			uri := file.RawURI()
			assert.Equal(t, string(file), uri)
		})

		t.Run("String", func(t *testing.T) {
			str := file.String()
			assert.Equal(t, string(file), str)
		})

		t.Run("URL", func(t *testing.T) {
			url := file.URL()
			assert.Equal(t, string(file), url)
		})

		t.Run("Path", func(t *testing.T) {
			path := file.Path()
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("PathWithSlashes", func(t *testing.T) {
			path := file.PathWithSlashes()
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("LocalPath", func(t *testing.T) {
			localPath := file.LocalPath()
			assert.Equal(t, "", localPath) // Not a local file system
		})

		t.Run("MustLocalPath", func(t *testing.T) {
			// Should panic for non-local file system
			assert.Panics(t, func() {
				file.MustLocalPath()
			})
		})

		t.Run("Name", func(t *testing.T) {
			name := file.Name()
			assert.Equal(t, "file.txt", name)
		})

		t.Run("Dir", func(t *testing.T) {
			dir := file.Dir()
			require.Equal(t, file.Dir(), dir) // Just verify it works consistently
		})

		t.Run("DirAndName", func(t *testing.T) {
			dir, name := file.DirAndName()
			assert.Equal(t, file.Dir(), dir)
			assert.Equal(t, "file.txt", name)
		})

		t.Run("VolumeName", func(t *testing.T) {
			volume := file.VolumeName()
			assert.Equal(t, "", volume) // MockFileSystem doesn't implement VolumeNameFileSystem
		})

		t.Run("Ext", func(t *testing.T) {
			ext := file.Ext()
			assert.Equal(t, ".txt", ext)
		})

		t.Run("ExtLower", func(t *testing.T) {
			file := File("mock://test/path/to/FILE.TXT")
			ext := file.ExtLower()
			assert.Equal(t, ".txt", ext)
		})

		t.Run("TrimExt", func(t *testing.T) {
			trimmed := file.TrimExt()
			assert.True(t, strings.HasSuffix(string(trimmed), "://test/path/to/file"))
		})

		t.Run("Join", func(t *testing.T) {
			joined := file.Join("subdir", "nested.txt")
			assert.True(t, strings.HasSuffix(string(joined), "file.txt/subdir/nested.txt"))
		})

		t.Run("Joinf", func(t *testing.T) {
			joined := file.Joinf("file_%d.txt", 123)
			assert.True(t, strings.HasSuffix(string(joined), "file.txt/file_123.txt"))
		})

		t.Run("IsReadable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a readable file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					size:  100,
					mode:  0644,
				}, nil
			}

			readable := file.IsReadable()
			require.True(t, readable)
		})

		t.Run("IsWritable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a writable file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					size:  100,
					mode:  0644,
				}, nil
			}

			writable := file.IsWritable()
			require.True(t, writable)
		})

		t.Run("Stat", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedInfo := &mockFileInfo{
				name:  "file.txt",
				isDir: false,
				size:  100,
				mode:  0644,
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return expectedInfo, nil
			}

			info, err := file.Stat()
			require.NoError(t, err)
			require.Equal(t, expectedInfo, info)
		})

		t.Run("Info", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedInfo := &mockFileInfo{
				name:  "file.txt",
				isDir: false,
				size:  100,
				mode:  0644,
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return expectedInfo, nil
			}

			info := file.Info()
			require.NotNil(t, info)
			assert.Equal(t, file, info.File)
		})

		t.Run("Exists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return no error (file exists)
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{name: "file.txt"}, nil
			}

			exists := file.Exists()
			require.True(t, exists)
		})

		t.Run("CheckExists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Test with existing file
			testMockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{name: "file.txt"}, nil
			}

			err := testFile.CheckExists()
			require.NoError(t, err)

			// Test with non-existing file
			testMockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return nil, errors.New("file not found")
			}

			err = testFile.CheckExists()
			require.Error(t, err)
			require.IsType(t, ErrDoesNotExist{}, err)
		})

		t.Run("IsDir", func(t *testing.T) {
			// Use the shared mockFS and override MockStat temporarily
			originalMockStat := mockFS.MockStat
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}
			defer func() { mockFS.MockStat = originalMockStat }()

			isDir := file.IsDir()
			require.True(t, isDir)
		})

		t.Run("CheckIsDir", func(t *testing.T) {
			// Use the shared mockFS and override MockStat temporarily
			originalMockStat := mockFS.MockStat

			// Test with directory
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}

			err := file.CheckIsDir()
			require.NoError(t, err)

			// Test with file (not directory)
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
				}, nil
			}

			err = file.CheckIsDir()
			require.Error(t, err)
			require.IsType(t, ErrIsNotDirectory{}, err)

			mockFS.MockStat = originalMockStat
		})

		t.Run("AbsPath", func(t *testing.T) {
			absPath := file.AbsPath()
			assert.Equal(t, "/test/path/to/file.txt", absPath)
		})

		t.Run("HasAbsPath", func(t *testing.T) {
			hasAbs := file.HasAbsPath()
			assert.True(t, hasAbs) // path starts with /
		})

		t.Run("ToAbsPath", func(t *testing.T) {
			absFile := file.ToAbsPath()
			// ToAbsPath does Prefix() + AbsPath(), which adds extra /
			// Expected: mockTestFile/NonPermissionMethods:///test/path/to/file.txt
			assert.True(t, strings.HasSuffix(string(absFile), "test/path/to/file.txt"))
		})

		t.Run("IsRegular", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Create file with matching prefix
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a regular file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					mode:  0644, // Regular file mode
				}, nil
			}

			isRegular := file.IsRegular()
			assert.True(t, isRegular)
		})

		t.Run("IsEmptyDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Create file with matching prefix
			file := File("mock" + t.Name() + "://test/path/to/dir")

			// Mock ListDirMax to return empty list
			mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return nil, nil
			}

			isEmpty := file.IsEmptyDir()
			assert.True(t, isEmpty)
		})

		t.Run("IsHidden", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			hiddenFile := File("mock" + t.Name() + "://test/path/to/.hidden")
			hidden := hiddenFile.IsHidden()
			assert.True(t, hidden)
		})

		t.Run("IsSymbolicLink", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			symlink := file.IsSymbolicLink()
			assert.False(t, symlink) // MockFileSystem returns false
		})

		t.Run("Size", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a file with size
			testMockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					size: 1024,
				}, nil
			}

			size := testFile.Size()
			assert.Equal(t, int64(1024), size)
		})

		t.Run("ContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			hash, err := file.ContentHash()
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("ContentHashContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			hash, err := file.ContentHashContext(context.Background())
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("Modified", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedTime := time.Now()
			testMockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:    "file.txt",
					modTime: expectedTime,
				}, nil
			}

			modified := testFile.Modified()
			assert.Equal(t, expectedTime, modified)
		})

		t.Run("Permissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					mode: 0644,
				}, nil
			}

			perm := file.Permissions()
			assert.Equal(t, Permissions(0644), perm)
		})

		t.Run("SetPermissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements SetPermissions
			err := testFile.SetPermissions(0644)
			require.NoError(t, err)
		})

		t.Run("ListDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := testFile.ListDir(func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := testFile.ListDirContext(context.Background(), func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirIter", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			for f, err := range testFile.ListDirIter() {
				require.NoError(t, err)
				listedFiles = append(listedFiles, f)
			}

			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirMax", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return expectedFiles, nil
			}

			files, err := testFile.ListDirMax(10)
			require.NoError(t, err)
			assert.Equal(t, expectedFiles, files)
		})

		t.Run("User", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements User methods
			user, err := testFile.User()
			require.NoError(t, err)
			assert.Equal(t, "testuser", user)
		})

		t.Run("SetUser", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements SetUser
			err := testFile.SetUser("testuser")
			require.NoError(t, err)
		})

		t.Run("Group", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Group
			group, err := testFile.Group()
			require.NoError(t, err)
			assert.Equal(t, "testgroup", group)
		})

		t.Run("SetGroup", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements SetGroup
			err := testFile.SetGroup("testgroup")
			require.NoError(t, err)
		})

		t.Run("Touch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Touch
			err := testFile.Touch()
			require.NoError(t, err)
		})

		t.Run("WriteTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			testMockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			var buf bytes.Buffer
			n, err := testFile.WriteTo(&buf)
			require.NoError(t, err)
			assert.Equal(t, int64(12), n) // "test content" length
			assert.Equal(t, "test content", buf.String())
		})

		t.Run("OpenReader", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedReader := &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}
			testMockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return expectedReader, nil
			}

			reader, err := testFile.OpenReader()
			require.NoError(t, err)
			assert.Equal(t, expectedReader, reader)
		})

		t.Run("OpenReadSeeker", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedReader := &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return expectedReader, nil
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					size: 12,
				}, nil
			}

			reader, err := file.OpenReadSeeker()
			require.NoError(t, err)
			require.NotNil(t, reader)
		})

		t.Run("ReadAll", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := testFile.ReadAll()
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := testFile.ReadAllContext(context.Background())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, hash, err := testFile.ReadAllContentHash(context.Background())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
			assert.NotEmpty(t, hash)
		})

		t.Run("ReadAllString", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := testFile.ReadAllString()
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadAllStringContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := testFile.ReadAllStringContext(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadJSON", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			jsonData := []byte(`{"name": "test", "value": 123}`)
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return jsonData, nil
			}

			var result map[string]any
			err := testFile.ReadJSON(context.Background(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result["name"])
			assert.Equal(t, float64(123), result["value"])
		})

		t.Run("ReadXML", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			xmlData := []byte(`<root><name>test</name><value>123</value></root>`)
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return xmlData, nil
			}

			var result struct {
				Name  string `xml:"name"`
				Value int    `xml:"value"`
			}
			err := testFile.ReadXML(context.Background(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result.Name)
			assert.Equal(t, 123, result.Value)
		})

		t.Run("GobEncode", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return []byte("test content"), nil
			}

			data, err := file.GobEncode()
			require.NoError(t, err)
			assert.NotEmpty(t, data)
		})

		t.Run("Watch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Watch
			cancel, err := testFile.Watch(func(f File, e Event) {})
			require.NoError(t, err)
			require.NotNil(t, cancel)

			// Test cancel function works
			err = cancel()
			require.NoError(t, err)
		})

		t.Run("Truncate", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Truncate
			err := testFile.Truncate(100)
			require.NoError(t, err)
		})

		t.Run("Rename", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Rename
			renamedFile, err := testFile.Rename("newfile.txt")
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(string(renamedFile), "/test/path/to/newfile.txt"))
		})

		t.Run("Renamef", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// MockFullyFeaturedFileSystem implements Rename
			renamedFile, err := testFile.Renamef("newfile_%d.txt", 123)
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(string(renamedFile), "/test/path/to/newfile_123.txt"))
		})

		t.Run("MoveTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")
			dest := File("mock" + t.Name() + "://test/path/to/destination.txt")

			// MockFullyFeaturedFileSystem implements Move
			err := testFile.MoveTo(dest)
			require.NoError(t, err)
		})

		t.Run("Remove", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockRemove = func(filePath string) error {
				assert.Equal(t, "/test/path/to/file.txt", filePath)
				return nil
			}

			err := file.Remove()
			require.NoError(t, err)
		})

		t.Run("RemoveRecursive", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock IsDir to return true
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}

			// Mock ListDir to return empty list
			mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				return nil
			}

			// Mock Remove
			mockFS.MockRemove = func(filePath string) error {
				return nil
			}

			err := file.RemoveRecursive()
			require.NoError(t, err)
		})

		t.Run("StdFS", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			stdFS := file.StdFS()
			assert.NotNil(t, stdFS)
			assert.Equal(t, file, stdFS.File)
		})

		t.Run("StdDirEntry", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			stdDirEntry := file.StdDirEntry()
			assert.NotNil(t, stdDirEntry)
			assert.Equal(t, file, stdDirEntry.File)
		})

		t.Run("EmptyFile", func(t *testing.T) {
			emptyFile := File("")

			// Test various methods with empty file
			assert.Equal(t, "", emptyFile.RawURI())
			assert.Equal(t, "", emptyFile.Name())
			assert.Equal(t, InvalidFile, emptyFile.Dir())

			// Test methods that should return errors for empty files
			_, err := emptyFile.OpenReader()
			assert.Equal(t, ErrEmptyPath, err)

			_, err = emptyFile.OpenWriter()
			assert.Equal(t, ErrEmptyPath, err)

			err = emptyFile.Remove()
			assert.Equal(t, ErrEmptyPath, err)

			err = emptyFile.CheckExists()
			assert.Equal(t, ErrEmptyPath, err)
		})
	})
}
