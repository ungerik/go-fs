package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
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

func TestFile_RelPathOf(t *testing.T) {
	base := File(t.TempDir())

	tests := []struct {
		name   string
		base   File
		target File
		want   string
	}{
		{
			name:   "same file is dot",
			base:   base,
			target: base,
			want:   ".",
		},
		{
			name:   "direct child",
			base:   base,
			target: base.Join("sub"),
			want:   "sub",
		},
		{
			name:   "nested descendant",
			base:   base,
			target: base.Join("a", "b", "c"),
			want:   filepath.Join("a", "b", "c"),
		},
		{
			name:   "parent of base",
			base:   base.Join("a"),
			target: base,
			want:   "..",
		},
		{
			name:   "sibling via parent",
			base:   base.Join("a"),
			target: base.Join("b"),
			want:   filepath.Join("..", "b"),
		},
		{
			name:   "uncle via grandparent",
			base:   base.Join("a", "b"),
			target: base.Join("c"),
			want:   filepath.Join("..", "..", "c"),
		},
		{
			name:   "joined path is cleaned",
			base:   base,
			target: base.Join("a", "..", "b"),
			want:   "b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.base.RelPathOf(tt.target)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	t.Run("relative base and absolute target errors", func(t *testing.T) {
		_, err := File("relative/dir").RelPathOf(base)
		require.Error(t, err)
	})

	t.Run("different file systems error", func(t *testing.T) {
		// InvalidFile lives on the Invalid file system, base lives on Local.
		_, err := base.RelPathOf(InvalidFile)
		require.ErrorContains(t, err, "file systems do not match")
	})
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
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
			return expectedErr
		})
		assert.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel() // Cancel immediately

		err := dir.ListDirInfoRecursiveContext(ctx, func(info *FileInfo) error {
			return nil
		})
		// Should return context.Canceled or similar error
		assert.Error(t, err)
	})

	t.Run("empty file path", func(t *testing.T) {
		emptyFile := File("")
		err := emptyFile.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
			return nil
		})
		assert.Equal(t, ErrEmptyPath, err)
	})

	t.Run("non-existent directory", func(t *testing.T) {
		nonExistent := dir.Join("nonexistent")
		err := nonExistent.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
			return nil
		})
		// Should return an error (likely file not found)
		assert.Error(t, err)
	})

	t.Run("verify info fields", func(t *testing.T) {
		var infos []*FileInfo
		err := dir.ListDirInfoRecursiveContext(t.Context(), func(info *FileInfo) error {
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

// noRenameMoveFS wraps a FileSystem but only exposes the base FileSystem
// interface, deliberately hiding any Rename or Move methods of the wrapped
// implementation. This forces File.Rename into its default copy-and-remove
// fallback branch for file systems that implement neither RenameFileSystem
// nor MoveFileSystem (for example s3fs).
type noRenameMoveFS struct {
	FileSystem
}

// registerNoRenameMoveFS creates an in-memory file system with the given
// initial files, re-registers it wrapped so that it routes through the
// default Rename branch, and returns the wrapped file system's root dir.
func registerNoRenameMoveFS(t *testing.T, initialFiles ...MemFile) File {
	t.Helper()
	memFS, err := NewMemFileSystem("/", initialFiles...)
	require.NoError(t, err)
	// Replace the registered *MemFileSystem (which implements Rename and
	// Move) with a wrapper that only exposes the base FileSystem interface.
	Unregister(memFS)
	wrapped := &noRenameMoveFS{FileSystem: memFS}
	Register(wrapped)
	t.Cleanup(func() {
		Unregister(wrapped)
		_ = memFS.Close()
	})
	// Guard the test's premise: the wrapper must not satisfy the optional
	// rename/move interfaces, otherwise the default branch is never reached.
	if _, ok := any(wrapped).(RenameFileSystem); ok {
		t.Fatal("noRenameMoveFS must not implement RenameFileSystem")
	}
	if _, ok := any(wrapped).(MoveFileSystem); ok {
		t.Fatal("noRenameMoveFS must not implement MoveFileSystem")
	}
	return wrapped.RootDir()
}

// TestFile_Rename_DefaultBranch covers the fallback used by file systems that
// implement neither RenameFileSystem nor MoveFileSystem. The previous
// implementation created an empty directory and then tried file.Remove(),
// which silently discarded a non-empty directory's contents and failed to
// remove the source. See file.go Rename default case.
func TestFile_Rename_DefaultBranch(t *testing.T) {
	t.Run("non-empty directory keeps all contents", func(t *testing.T) {
		root := registerNoRenameMoveFS(t,
			NewMemFile("dir/a.txt", []byte("aaa")),
			NewMemFile("dir/b.txt", []byte("bbb")),
			NewMemFile("dir/sub/c.txt", []byte("ccc")),
		)
		dir := root.Join("dir")
		require.True(t, dir.IsDir(), "source must be a directory")

		renamed, err := dir.Rename("renamed")
		require.NoError(t, err, "Rename of non-empty directory")

		// Source directory must be gone, including its contents.
		require.False(t, dir.Exists(), "source directory must be removed")

		// Destination must exist and carry over all contents recursively.
		require.True(t, renamed.IsDir(), "renamed must be a directory")
		require.Equal(t, "renamed", renamed.Name())
		assertFileContent(t, renamed.Join("a.txt"), "aaa")
		assertFileContent(t, renamed.Join("b.txt"), "bbb")
		require.True(t, renamed.Join("sub").IsDir(), "nested directory preserved")
		assertFileContent(t, renamed.Join("sub", "c.txt"), "ccc")
	})

	t.Run("single file", func(t *testing.T) {
		root := registerNoRenameMoveFS(t, NewMemFile("a.txt", []byte("hello")))
		src := root.Join("a.txt")
		require.True(t, src.Exists())

		renamed, err := src.Rename("b.txt")
		require.NoError(t, err, "Rename of single file")

		require.False(t, src.Exists(), "source file must be removed")
		require.Equal(t, "b.txt", renamed.Name())
		assertFileContent(t, renamed, "hello")
	})
}

func assertFileContent(t *testing.T, file File, want string) {
	t.Helper()
	require.True(t, file.Exists(), "file %s must exist", file)
	got, err := file.ReadAllString()
	require.NoError(t, err, "ReadAllString %s", file)
	require.Equal(t, want, got, "content of %s", file)
}
