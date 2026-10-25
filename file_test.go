package fs

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	iofs "io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	stdfstest "testing/fstest"
	"testing/iotest"
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

	err = baseDir.Join(pathParts[0]).RemoveRecursive(context.Background())
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
		assert.Equal(t, filepath.FromSlash(exp), f.LocalPath())
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
		// The extension is taken from the decoded path, not from the URI
		{file: File(LocalPrefix + "/dir/file.100%2525"), want: ".100%25"},
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
	type fileEvent struct {
		file  File
		event Event
	}
	var (
		dir = File(t.TempDir())
		mtx sync.Mutex // the callback runs on a goroutine of the watch
		got []fileEvent
	)
	cancel, err := dir.Watch(func(file File, event Event) {
		mtx.Lock()
		defer mtx.Unlock()
		got = append(got, fileEvent{file, event})
	})
	require.NoError(t, err, "dir.Watch")

	// received reports if an event of file has the operation op,
	// fsnotify can report several operations in one event
	received := func(file File, op Event) bool {
		mtx.Lock()
		defer mtx.Unlock()
		return slices.ContainsFunc(got, func(e fileEvent) bool {
			return e.file == file && e.event&op == op
		})
	}
	// The watcher reports the events asynchronously, so wait for
	// the event of every step before taking the next one.
	waitFor := func(file File, op Event) {
		t.Helper()
		require.Eventually(t, func() bool { return received(file, op) },
			5*time.Second, time.Millisecond, "event %s of %s", op, file)
	}

	// kqueue reports the RENAME and REMOVE of a file in a directory
	// only through a watch of the file, which fsnotify adds after it
	// reported the CREATE. A WRITE comes through that watch, so the
	// file is written until a WRITE shows that the watch exists.
	writeUntilWatched := func(file File) {
		t.Helper()
		require.Eventually(t, func() bool {
			require.NoError(t, file.WriteAllString(t.Context(), "content"), "WriteAllString")
			return received(file, EventWrite)
		}, 5*time.Second, 5*time.Millisecond, "event %s of %s", EventWrite, file)
	}

	newFile := dir.Join("newFile")
	require.NoError(t, newFile.Touch(), "newFile.Touch")
	waitFor(newFile, EventCreate)
	writeUntilWatched(newFile)

	renamedFile, err := newFile.Rename(t.Context(), "renamedFile")
	require.NoError(t, err, "newFile.Rename")
	// The order of the two events differs between platforms
	// (inotify reports the RENAME of newFile before the CREATE
	// of renamedFile, kqueue the other way round)
	waitFor(newFile, EventRename)
	waitFor(renamedFile, EventCreate)
	writeUntilWatched(renamedFile)

	require.NoError(t, renamedFile.Remove(), "renamedFile.Remove")
	waitFor(renamedFile, EventRemove)

	err = cancel()
	assert.NoError(t, err, "cancel watch")

	mtx.Lock()
	defer mtx.Unlock()
	for _, e := range got {
		assert.Contains(t, []File{newFile, renamedFile}, e.file, "event %s of an unexpected file", e.event)
	}
}

// mapFSDir registers a read-only StdFileSystem over the MapFS
// fixture for the test and returns its root directory.
func mapFSDir(t *testing.T, fixture stdfstest.MapFS) File {
	t.Helper()
	stdFS := NewStdFileSystem(fixture, "")
	require.NoError(t, Register(stdFS))
	t.Cleanup(func() { _ = stdFS.Close() })
	return stdFS.RootDir()
}

func TestFile_ListDirInfoRecursiveContext(t *testing.T) {
	dir := mapFSDir(t, stdfstest.MapFS{
		"file1.txt":                 {},
		"file2.log":                 {},
		"subdir1/file3.txt":         {},
		"subdir1/file4.log":         {},
		"subdir1/subdir2/file5.txt": {},
		"subdir1/subdir2/file6.md":  {},
	})
	file1 := dir.Join("file1.txt")
	file2 := dir.Join("file2.log")
	subdir1 := dir.Join("subdir1")
	file3 := subdir1.Join("file3.txt")
	file4 := subdir1.Join("file4.log")
	subdir2 := subdir1.Join("subdir2")
	file5 := subdir2.Join("file5.txt")
	file6 := subdir2.Join("file6.md")

	t.Run("all files without pattern", func(t *testing.T) {
		var files []File
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
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
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
			return expectedErr
		})
		assert.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel() // Cancel immediately

		err := dir.ListDirInfoRecursive(ctx, func(info *FileInfo) error {
			return nil
		})
		// Should return context.Canceled or similar error
		assert.Error(t, err)
	})

	t.Run("empty file path", func(t *testing.T) {
		emptyFile := File("")
		err := emptyFile.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
			return nil
		})
		assert.Equal(t, ErrEmptyPath, err)
	})

	t.Run("non-existent directory", func(t *testing.T) {
		nonExistent := dir.Join("nonexistent")
		err := nonExistent.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
			return nil
		})
		// Should return an error (likely file not found)
		assert.Error(t, err)
	})

	t.Run("verify info fields", func(t *testing.T) {
		var infos []*FileInfo
		err := dir.ListDirInfoRecursive(t.Context(), func(info *FileInfo) error {
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
	dir := mapFSDir(t, stdfstest.MapFS{"a": {}, "b": {}, "c": {}})
	files := map[File]bool{
		dir.Join("a"): true,
		dir.Join("b"): true,
		dir.Join("c"): true,
	}

	err := dir.ListDir(t.Context(), func(file File) error {
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
	dir := mapFSDir(t, stdfstest.MapFS{"a": {}, "b": {}, "c": {}})
	files := map[File]bool{
		dir.Join("a"): true,
		dir.Join("b"): true,
		dir.Join("c"): true,
	}

	for file, err := range dir.ListDirIter(t.Context()) {
		require.NoError(t, err, "ListDirIter should not return an error")
		if !files[file] {
			t.Errorf("unexpected file: %s", file)
		}
		delete(files, file)
	}
	require.Empty(t, files, "not all files listed")
}

func TestFile_Glob(t *testing.T) {
	rootDir := mapFSDir(t, stdfstest.MapFS{
		"seed/a/b/c/cFile":                   {},
		"seed/a/b/c/Hello/World/x/file1.txt": {},
		"seed/a/b/c/Hello/World/x/file2.txt": {},
		"seed/a/b/c/Hello/World/x/file3.txt": {},
		"seed/a/b/c/Hello/World/y":           {Mode: iofs.ModeDir},
	})
	dir := rootDir.Join("seed")
	xDir := dir.Join("a", "b", "c", "Hello", "World", "x")
	cFile := dir.Join("a", "b", "c", "cFile")
	xFile1 := xDir.Join("file1.txt")
	xFile2 := xDir.Join("file2.txt")
	xFile3 := xDir.Join("file3.txt")

	// Absolute patterns below are relative to the root directory
	dirPathFromRoot := dir.Path()

	tests := []struct {
		name    string
		file    File
		pattern string
		want    []File
		wantErr error
	}{
		{
			name:    "empty pattern",
			file:    dir,
			pattern: "",
			want:    []File{dir},
		},
		{
			name:    "no wildcard pattern",
			file:    dir,
			pattern: "a/b/c/Hello/World/x/file1.txt",
			want:    []File{xFile1},
		},
		{
			name:    "no wildcard non-canonical pattern",
			file:    dir,
			pattern: "/./a/b//c/Hello/./././/World/x/file1.txt",
			want:    []File{xFile1},
		},
		{
			name:    "no wildcard directory pattern on a file",
			file:    dir,
			pattern: "a/b/c/Hello/World/x/file1.txt/",
			want:    nil,
		},
		{
			name:    "root files",
			file:    dir,
			pattern: "*",
			want:    []File{dir.Join("a")},
		},
		{
			name:    "root dirs",
			file:    dir,
			pattern: "*/",
			want:    []File{dir.Join("a")},
		},
		{
			name:    "file and dir",
			file:    dir,
			pattern: "./a/b/c/*",
			want:    []File{dir.Join("a", "b", "c", "Hello"), cFile},
		},
		{
			name:    "directories only",
			file:    dir,
			pattern: "./a/b/c/*/",
			want:    []File{dir.Join("a", "b", "c", "Hello")},
		},
		{
			name:    "complexer pattern",
			file:    dir,
			pattern: "*/b/c/*/W???d/x/file[1-2].txt",
			want:    []File{xFile1, xFile2},
		},
		{
			name:    "root dir base",
			file:    rootDir,
			pattern: dirPathFromRoot + "/a/b/c/Hello/World/x/*.txt",
			want:    []File{xFile1, xFile2, xFile3},
		},
		{
			name:    "missing directory is no match",
			file:    dir,
			pattern: "*/missing/*",
			want:    nil,
		},
		{
			name:    "pattern continuing below a file is no match",
			file:    dir,
			pattern: "a/b/c/cFile/*",
			want:    nil,
		},
		{
			name:    "no wildcard pattern continuing below a file is no match",
			file:    dir,
			pattern: "*/b/c/cFile/x.txt",
			want:    nil,
		},
		// Errors
		{
			name:    "invalid file",
			file:    "",
			pattern: "",
			wantErr: ErrEmptyPath,
		},
		{
			name:    "invalid empty path base file",
			file:    "",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/*",
			wantErr: ErrEmptyPath,
		},
		{
			name:    "malformed pattern",
			file:    dir,
			pattern: "a/b/c/Hello/World/x/[file1.txt",
			wantErr: path.ErrBadPattern,
		},
		{
			name:    "parent directory in pattern",
			file:    dir,
			pattern: "a/../*",
			wantErr: path.ErrBadPattern,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := collectGlob(tt.file.Glob(t.Context(), tt.pattern))
			if tt.wantErr != nil {
				require.Len(t, errs, 1, "File.Glob must yield exactly one error")
				require.ErrorIs(t, errs[0], tt.wantErr)
				require.Empty(t, got, "no files after a pattern error")
				return
			}
			require.Empty(t, errs, "File.Glob errors")
			require.Equal(t, tt.want, got, "file path sorted results")
		})
	}
}

// collectGlob returns the files yielded by a Glob iterator
// sorted by path and the yielded errors.
func collectGlob(seq iter.Seq2[File, error]) (files []File, errs []error) {
	for file, err := range seq {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
	return files, errs
}

func TestGlob(t *testing.T) {
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive(context.Background()) })
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

	tests := []struct {
		name    string
		pattern string
		want    []File
		wantErr error
	}{
		{
			name:    "empty pattern for current dir",
			pattern: "",
			want:    []File{"."},
		},
		{
			name:    "no wildcard pattern",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/file1.txt",
			want:    []File{xFile1},
		},
		{
			name:    "no wildcard non-canonical pattern",
			pattern: dir.PathWithSlashes() + "/./a/b//c/Hello/./././/World/x/file1.txt",
			want:    []File{xFile1},
		},
		{
			// A trailing slash matches only directories, also without wildcards
			name:    "no wildcard directory pattern on a file",
			pattern: dir.PathWithSlashes() + "/a/b/c/cFile/",
			want:    nil,
		},
		{
			name:    "no wildcard directory pattern",
			pattern: dir.PathWithSlashes() + "/a/b/c/",
			want:    []File{dir.Join("a", "b", "c")},
		},
		{
			name:    "root files",
			pattern: dir.PathWithSlashes() + "/*",
			want:    []File{dir.Join("a")},
		},
		{
			name:    "root dirs",
			pattern: dir.PathWithSlashes() + "/*/",
			want:    []File{dir.Join("a")},
		},
		{
			name:    "file and dir",
			pattern: dir.PathWithSlashes() + "/a/b/c/*",
			want:    []File{dir.Join("a", "b", "c", "Hello"), cFile},
		},
		{
			name:    "directories only",
			pattern: dir.PathWithSlashes() + "/a/b/c/*/",
			want:    []File{dir.Join("a", "b", "c", "Hello")},
		},
		{
			name:    "complexer pattern",
			pattern: dir.PathWithSlashes() + "/*/b/c/*/W???d/x/file[1-2].txt",
			want:    []File{xFile1, xFile2},
		},
		{
			name:    "*.txt",
			pattern: dir.PathWithSlashes() + "/a/b/c/Hello/World/x/*.txt",
			want:    []File{xFile1, xFile2, xFile3},
		},
		{
			// The local file system reports "not a directory" for these paths
			name:    "pattern continuing below a file is no match",
			pattern: dir.PathWithSlashes() + "/a/b/c/cFile/x/*",
			want:    nil,
		},
		{
			name:    "no wildcard pattern continuing below a file is no match",
			pattern: dir.PathWithSlashes() + "/*/b/c/cFile/x/y.txt",
			want:    nil,
		},
		// Errors
		{
			name:    "malformed pattern",
			pattern: "/a/b/c/Hello/World/x/[file1.txt",
			wantErr: path.ErrBadPattern,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := collectGlob(Glob(t.Context(), tt.pattern))
			if tt.wantErr != nil {
				require.Len(t, errs, 1, "Glob must yield exactly one error")
				require.ErrorIs(t, errs[0], tt.wantErr)
				return
			}
			require.Empty(t, errs, "Glob errors")
			require.Equal(t, tt.want, got, "file path sorted results")
		})
	}
}

// TestGlob_Break is a regression test: breaking out of a Glob loop
// over a pattern with several wildcard segments panicked with
// "range function continued iteration", because the iterator kept
// listing directories and calling yield after it returned false.
func TestGlob_Break(t *testing.T) {
	memFS, err := RegisteredTestMemFileSystem(nil,
		NewMemFile("/d1/x.txt", []byte("1")),
		NewMemFile("/d2/x.txt", []byte("2")),
		NewMemFile("/d3/x.txt", []byte("3")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })

	for _, pattern := range []string{"*/x.txt", "*/*.txt", "*/*"} {
		t.Run(pattern, func(t *testing.T) {
			n := 0
			require.NotPanics(t, func() {
				for file, err := range memFS.RootDir().Glob(t.Context(), pattern) {
					require.NoError(t, err)
					require.True(t, file.Exists())
					n++
					break
				}
			})
			require.Equal(t, 1, n, "one file before the break")
		})
	}
}

// TestGlob_URIWithoutWildcard is a regression test: a pattern without
// a wildcard was cleaned with path.Clean, which turned "mem://id/x"
// into "mem:/id/x", so the existing file was not yielded.
func TestGlob_URIWithoutWildcard(t *testing.T) {
	memFS, err := RegisteredTestMemFileSystem(nil,
		NewMemFile("/d1/x.txt", []byte("1")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	file := memFS.RootDir().Join("d1", "x.txt")
	dir := memFS.RootDir().Join("d1")

	got, errs := collectGlob(Glob(t.Context(), string(file)))
	require.Empty(t, errs)
	require.Equal(t, []File{file}, got, "URI without wildcard")

	got, errs = collectGlob(Glob(t.Context(), string(dir)+"/"))
	require.Empty(t, errs)
	require.Equal(t, []File{dir}, got, "directory URI with trailing slash")

	got, errs = collectGlob(Glob(t.Context(), string(file)+"/"))
	require.Empty(t, errs)
	require.Empty(t, got, "file URI with trailing slash matches only directories")

	got, errs = collectGlob(Glob(t.Context(), string(dir)+"/*.txt"))
	require.Empty(t, errs)
	require.Equal(t, []File{file}, got, "URI with wildcard")
}

// globFailListFS wraps a file system and fails ListDir
// for one directory, to test that Glob yields listing errors.
type globFailListFS struct {
	WriteFileSystem
	failDir string
}

var errGlobTestListing = errors.New("listing failed")

func (f *globFailListFS) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if dirPath == f.failDir {
		return errGlobTestListing
	}
	return f.WriteFileSystem.ListDir(ctx, dirPath, patterns, callback)
}

// TestGlob_Errors is a regression test: Glob used to swallow listing
// errors including context cancellation and returned pattern errors
// separately. Now they are yielded by the iterator.
func TestGlob_Errors(t *testing.T) {
	memFS, err := RegisteredTestMemFileSystem(nil,
		NewMemFile("/d1/x.txt", []byte("1")),
		NewMemFile("/d2/x.txt", []byte("2")),
		NewMemFile("/d3/x.txt", []byte("3")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	root := memFS.RootDir()

	t.Run("listing error", func(t *testing.T) {
		Unregister(memFS)
		wrapped := &globFailListFS{WriteFileSystem: memFS, failDir: "/d2"}
		require.NoError(t, Register(wrapped))
		t.Cleanup(func() {
			Unregister(wrapped)
			require.NoError(t, Register(memFS))
		})

		got, errs := collectGlob(root.Glob(t.Context(), "*/*.txt"))
		require.Len(t, errs, 1, "the listing error is yielded")
		require.ErrorIs(t, errs[0], errGlobTestListing)
		require.Equal(t, []File{root.Join("d1", "x.txt"), root.Join("d3", "x.txt")}, got,
			"the iteration continues after a listing error")

		// Any error from listing a file is no match
		wrapped.failDir = "/d1/x.txt"
		got, errs = collectGlob(root.Glob(t.Context(), "d1/x.txt/*"))
		require.Empty(t, errs, "listing a file is no match")
		require.Empty(t, got)
	})

	t.Run("canceled before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		got, errs := collectGlob(root.Glob(ctx, "*/*.txt"))
		require.Empty(t, got)
		require.Len(t, errs, 1, "the context error is yielded once")
		require.ErrorIs(t, errs[0], context.Canceled)
	})

	t.Run("canceled while iterating", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var (
			files int
			errs  []error
		)
		for file, err := range root.Glob(ctx, "*/*.txt") {
			if err != nil {
				require.Equal(t, InvalidFile, file, "errors are yielded with InvalidFile")
				errs = append(errs, err)
				continue
			}
			files++
			cancel()
		}
		require.Equal(t, 1, files, "no file after the cancellation")
		require.Len(t, errs, 1, "the context error is yielded once")
		require.ErrorIs(t, errs[0], context.Canceled)
	})
}

// noRenameMoveFS wraps a FileSystem but only exposes the WriteFileSystem
// interface, deliberately hiding the Move method of the wrapped
// implementation. This forces File.Rename into the copy-and-remove
// emulation moveByCopy in dispatch.go, used by file systems
// that don't implement MoveFileSystem (for example s3fs).
type noRenameMoveFS struct {
	WriteFileSystem
}

// registerNoRenameMoveFS creates an in-memory file system with the given
// initial files, re-registers it wrapped so that it routes through the
// move emulation, and returns the wrapped file system's root dir.
func registerNoRenameMoveFS(t *testing.T, initialFiles ...MemFile) File {
	t.Helper()
	memFS, err := RegisteredTestMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	// Replace the registered *MemFileSystem (which implements Move)
	// with a wrapper that only exposes the WriteFileSystem interface.
	Unregister(memFS)
	wrapped := &noRenameMoveFS{WriteFileSystem: memFS}
	require.NoError(t, Register(wrapped))
	t.Cleanup(func() {
		Unregister(wrapped)
		_ = memFS.Close()
	})
	// Guard the test's premise: the wrapper must not satisfy the optional
	// move interface, otherwise the emulation is never reached.
	if _, ok := any(wrapped).(MoveFileSystem); ok {
		t.Fatal("noRenameMoveFS must not implement MoveFileSystem")
	}
	return wrapped.RootDir()
}

// TestFile_Rename_DefaultBranch covers the move emulation used by file systems
// that don't implement MoveFileSystem. A previous implementation created
// an empty directory and then tried file.Remove(), which silently discarded
// a non-empty directory's contents and failed to remove the source.
// See moveByCopy in dispatch.go.
func TestFile_Rename_DefaultBranch(t *testing.T) {
	t.Run("non-empty directory keeps all contents", func(t *testing.T) {
		root := registerNoRenameMoveFS(t,
			NewMemFile("dir/a.txt", []byte("aaa")),
			NewMemFile("dir/b.txt", []byte("bbb")),
			NewMemFile("dir/sub/c.txt", []byte("ccc")),
		)
		dir := root.Join("dir")
		require.True(t, dir.IsDir(), "source must be a directory")

		renamed, err := dir.Rename(t.Context(), "renamed")
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

		renamed, err := src.Rename(t.Context(), "b.txt")
		require.NoError(t, err, "Rename of single file")

		require.False(t, src.Exists(), "source file must be removed")
		require.Equal(t, "b.txt", renamed.Name())
		assertFileContent(t, renamed, "hello")
	})
}

func assertFileContent(t *testing.T, file File, want string) {
	t.Helper()
	require.True(t, file.Exists(), "file %s must exist", file)
	got, err := file.ReadAllString(t.Context())
	require.NoError(t, err, "ReadAllString %s", file)
	require.Equal(t, want, got, "content of %s", file)
}

// TestFile_Size is a regression test: the documentation says that
// Size returns 0 for a directory, but the local file system
// returned the size of the directory entries.
func TestFile_Size(t *testing.T) {
	dir := File(t.TempDir())
	require.NoError(t, dir.Join("a.txt").WriteAllString(t.Context(), "abc"))
	require.Equal(t, int64(0), dir.Size(), "local directory")
	require.Equal(t, int64(3), dir.Join("a.txt").Size(), "local file")
	require.Equal(t, int64(0), dir.Join("missing").Size(), "missing file")

	memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("/sub/a.txt", []byte("abc")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.Equal(t, int64(0), memFS.RootDir().Join("sub").Size(), "mem directory")
	require.Equal(t, int64(3), memFS.RootDir().Join("sub", "a.txt").Size(), "mem file")
}

// readFromTestFS wraps a file system so that OpenWriter returns
// writers that commit the data only in Close, like uploads.
type readFromTestFS struct {
	WriteFileSystem
	closeErr  error // returned by Close of the writers
	abortable bool  // the writers implement WriteAborter

	closed, aborted bool
}

func (f *readFromTestFS) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	w := &readFromTestWriter{fs: f, filePath: filePath, perm: perm}
	if f.abortable {
		return &readFromTestAbortWriter{w}, nil
	}
	return w, nil
}

type readFromTestWriter struct {
	fs       *readFromTestFS
	filePath string
	perm     Permissions
	buf      bytes.Buffer
}

func (w *readFromTestWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *readFromTestWriter) Close() error {
	w.fs.closed = true
	if w.fs.closeErr != nil {
		return w.fs.closeErr
	}
	inner, err := w.fs.WriteFileSystem.OpenWriter(w.filePath, w.perm)
	if err != nil {
		return err
	}
	_, err = inner.Write(w.buf.Bytes())
	return errors.Join(err, inner.Close())
}

type readFromTestAbortWriter struct{ *readFromTestWriter }

func (w *readFromTestAbortWriter) Abort() error {
	w.fs.aborted = true
	return nil
}

// registerReadFromTestFS registers a readFromTestFS
// over a MemFileSystem with initialFiles.
func registerReadFromTestFS(t *testing.T, initialFiles ...MemFile) (*readFromTestFS, File) {
	t.Helper()
	memFS, err := NewMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	wrapped := &readFromTestFS{WriteFileSystem: memFS}
	require.NoError(t, Register(wrapped))
	t.Cleanup(func() {
		Unregister(wrapped)
		_ = memFS.Close()
	})
	return wrapped, wrapped.RootDir()
}

// TestFile_ReadFrom is a regression test: ReadFrom deferred the Close
// of the writer, so a failed upload in Close was not reported,
// and a failed copy committed the partial data in Close.
func TestFile_ReadFrom(t *testing.T) {
	errRead := errors.New("read failed")

	t.Run("success", func(t *testing.T) {
		_, root := registerReadFromTestFS(t)
		n, err := root.Join("a.txt").ReadFrom(strings.NewReader("data"))
		require.NoError(t, err)
		require.Equal(t, int64(4), n)
		assertFileContent(t, root.Join("a.txt"), "data")
	})

	t.Run("close error", func(t *testing.T) {
		fileSystem, root := registerReadFromTestFS(t)
		errClose := errors.New("upload failed")
		fileSystem.closeErr = errClose
		_, err := root.Join("a.txt").ReadFrom(strings.NewReader("data"))
		require.ErrorIs(t, err, errClose)
	})

	t.Run("copy error aborts", func(t *testing.T) {
		fileSystem, root := registerReadFromTestFS(t, NewMemFile("/a.txt", []byte("precious")))
		fileSystem.abortable = true
		_, err := root.Join("a.txt").ReadFrom(iotest.ErrReader(errRead))
		require.ErrorIs(t, err, errRead)
		require.True(t, fileSystem.aborted, "writer must be aborted")
		require.False(t, fileSystem.closed, "writer must not be closed after Abort")
		assertFileContent(t, root.Join("a.txt"), "precious")
	})

	t.Run("copy error closes writer without Abort", func(t *testing.T) {
		fileSystem, root := registerReadFromTestFS(t)
		_, err := root.Join("a.txt").ReadFrom(iotest.ErrReader(errRead))
		require.ErrorIs(t, err, errRead)
		require.True(t, fileSystem.closed, "writer must be closed")
	})

	t.Run("empty path", func(t *testing.T) {
		_, err := InvalidFile.ReadFrom(strings.NewReader("data"))
		require.ErrorIs(t, err, ErrEmptyPath)
	})
}

// TestFile_RemoveDirContents is a regression test: RemoveDirContents
// is documented to delete the files of a directory, but it also tried
// to remove sub-directories and failed for non-empty ones.
func TestFile_RemoveDirContents(t *testing.T) {
	newDir := func(t *testing.T) File {
		dir := File(t.TempDir())
		require.NoError(t, dir.Join("a.txt").WriteAllString(t.Context(), "a"))
		require.NoError(t, dir.Join("b.log").WriteAllString(t.Context(), "b"))
		require.NoError(t, dir.Join("sub", "c.txt").MakeAllDirs())
		require.NoError(t, dir.Join("sub", "c.txt").Remove())
		require.NoError(t, dir.Join("sub", "c.txt").WriteAllString(t.Context(), "c"))
		require.NoError(t, dir.Join("empty").MakeDir())
		return dir
	}

	t.Run("all files", func(t *testing.T) {
		dir := newDir(t)
		require.NoError(t, dir.RemoveDirContents(t.Context()))
		require.False(t, dir.Join("a.txt").Exists())
		require.False(t, dir.Join("b.log").Exists())
		require.True(t, dir.Join("sub", "c.txt").Exists(), "sub-directories are kept")
		require.True(t, dir.Join("empty").IsDir(), "empty sub-directories are kept")
	})

	t.Run("patterns", func(t *testing.T) {
		dir := newDir(t)
		require.NoError(t, dir.RemoveDirContents(t.Context(), "*.txt"))
		require.False(t, dir.Join("a.txt").Exists())
		require.True(t, dir.Join("b.log").Exists())
		require.True(t, dir.Join("sub", "c.txt").Exists())
	})

	t.Run("symbolic link to a directory", func(t *testing.T) {
		dir := newDir(t)
		link := dir.Join("link")
		if err := os.Symlink(dir.Join("sub").LocalPath(), link.LocalPath()); err != nil {
			t.Skipf("can't create symbolic link: %s", err)
		}
		require.NoError(t, dir.RemoveDirContents(t.Context()))
		require.False(t, link.IsSymbolicLink(), "the link is removed")
		require.True(t, dir.Join("sub", "c.txt").Exists(), "the link target is kept")
	})

	t.Run("RemoveDirContentsRecursive", func(t *testing.T) {
		dir := newDir(t)
		require.NoError(t, dir.RemoveDirContentsRecursive(t.Context()))
		require.True(t, dir.IsEmptyDir())
	})
}

func TestFile_WriteJSON(t *testing.T) {
	dir := File(t.TempDir())
	input := map[string]int{"a": 1}

	file := dir.Join("a.json")
	require.NoError(t, file.WriteJSON(t.Context(), input, UserReadWrite))
	assertFileContent(t, file, `{"a":1}`)
	if runtime.GOOS != "windows" {
		require.Equal(t, UserReadWrite, file.Permissions(), "permissions like WriteAll")
	}

	file = dir.Join("indent.json")
	require.NoError(t, file.WriteJSONIndent(t.Context(), input, "  "))
	assertFileContent(t, file, "{\n  \"a\": 1\n}")

	// An empty indent still starts a new line for every element
	file = dir.Join("newlines.json")
	require.NoError(t, file.WriteJSONIndent(t.Context(), input, ""))
	assertFileContent(t, file, "{\n\"a\": 1\n}")

	require.ErrorIs(t, dir.Join("x.json").WriteJSON(t.Context(), make(chan int)), ErrMarshalJSON)
	require.ErrorIs(t, dir.Join("x.json").WriteJSONIndent(t.Context(), make(chan int), "  "), ErrMarshalJSON)
	require.ErrorIs(t, InvalidFile.WriteJSON(t.Context(), input), ErrEmptyPath)
	require.ErrorIs(t, InvalidFile.WriteJSONIndent(t.Context(), input, "  "), ErrEmptyPath)

	var output map[string]int
	require.NoError(t, dir.Join("indent.json").ReadJSON(t.Context(), &output))
	require.Equal(t, input, output)
}

func TestFile_WriteXML(t *testing.T) {
	type doc struct {
		XMLName xml.Name `xml:"doc"`
		A       int      `xml:"a"`
	}
	dir := File(t.TempDir())
	input := doc{A: 1}

	file := dir.Join("a.xml")
	require.NoError(t, file.WriteXML(t.Context(), input, UserReadWrite))
	assertFileContent(t, file, xml.Header+"<doc><a>1</a></doc>")
	if runtime.GOOS != "windows" {
		require.Equal(t, UserReadWrite, file.Permissions(), "permissions like WriteAll")
	}

	file = dir.Join("indent.xml")
	require.NoError(t, file.WriteXMLIndent(t.Context(), input, "\t"))
	assertFileContent(t, file, xml.Header+"<doc>\n\t<a>1</a>\n</doc>")

	// An empty indent still starts a new line for every element
	file = dir.Join("newlines.xml")
	require.NoError(t, file.WriteXMLIndent(t.Context(), input, ""))
	assertFileContent(t, file, xml.Header+"<doc>\n<a>1</a>\n</doc>")

	require.ErrorIs(t, dir.Join("x.xml").WriteXML(t.Context(), make(chan int)), ErrMarshalXML)
	require.ErrorIs(t, dir.Join("x.xml").WriteXMLIndent(t.Context(), make(chan int), "\t"), ErrMarshalXML)
	require.ErrorIs(t, InvalidFile.WriteXML(t.Context(), input), ErrEmptyPath)
	require.ErrorIs(t, InvalidFile.WriteXMLIndent(t.Context(), input, "\t"), ErrEmptyPath)

	var output doc
	require.NoError(t, file.ReadXML(t.Context(), &output))
	require.Equal(t, input.A, output.A)
}

// xmlLeadingTokensDoc writes a processing instruction and a comment
// before its root element, so the line prefix of the root element
// does not start the output of xml.MarshalIndent
type xmlLeadingTokensDoc struct{ A int }

func (d xmlLeadingTokensDoc) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	err := e.EncodeToken(xml.ProcInst{Target: "xml-stylesheet", Inst: []byte(`href="a.xsl"`)})
	if err != nil {
		return err
	}
	err = e.EncodeToken(xml.Comment(" c "))
	if err != nil {
		return err
	}
	start.Name.Local = "doc"
	return e.EncodeElement(struct {
		A int `xml:"a"`
	}{d.A}, start)
}

func Test_xmlLinePrefixRemover(t *testing.T) {
	// The encoder writes in chunks, a NUL at the end of one chunk
	// is still removed when the next chunk starts with '<'.
	// Only the NUL directly before '<' goes, other NUL bytes stay
	var w xmlLinePrefixRemover
	for _, chunk := range []string{"\x00<a>\n\x00", "<b>x\x00\x00", "</b>\n\x00", "</a>\x00"} {
		n, err := w.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n)
	}
	require.Equal(t, "<a>\n<b>x\x00</b>\n</a>\x00", string(w.data))
}

func Test_xmlMarshalIndent(t *testing.T) {
	type item struct {
		Name string `xml:"name,attr"`
		Text string `xml:",chardata"`
	}
	type doc struct {
		XMLName xml.Name `xml:"doc"`
		Items   []item   `xml:"item"`
	}
	// NUL bytes in attributes and character data are replaced with U+FFFD
	// by the encoder, so they can't be mistaken for the line prefix
	input := doc{Items: []item{{Name: "a\x00", Text: "\x00x"}, {Name: "b", Text: "y"}}}

	data, err := xmlMarshalIndent(input, "")
	require.NoError(t, err)
	require.Equal(t, "<doc>\n<item name=\"a\uFFFD\">\uFFFDx</item>\n<item name=\"b\">y</item>\n</doc>", string(data))

	data, err = xmlMarshalIndent(input, "  ")
	require.NoError(t, err)
	require.Equal(t, "<doc>\n  <item name=\"a\uFFFD\">\uFFFDx</item>\n  <item name=\"b\">y</item>\n</doc>", string(data))

	// Deeper elements are not indented either
	type nested struct {
		XMLName xml.Name `xml:"a"`
		B       struct {
			C int `xml:"c"`
		} `xml:"b"`
	}
	data, err = xmlMarshalIndent(nested{}, "")
	require.NoError(t, err)
	require.Equal(t, "<a>\n<b>\n<c>0</c>\n</b>\n</a>", string(data))

	// Multiple top level elements start a new line each
	data, err = xmlMarshalIndent(input.Items, "")
	require.NoError(t, err)
	require.Equal(t, "<item name=\"a\uFFFD\">\uFFFDx</item>\n<item name=\"b\">y</item>", string(data))

	// Tokens before the root element don't start a new line,
	// the line prefix of the root element must not stay in the output
	data, err = xmlMarshalIndent(xmlLeadingTokensDoc{A: 1}, "")
	require.NoError(t, err)
	require.Equal(t, `<?xml-stylesheet href="a.xsl"?><!-- c --><doc>`+"\n<a>1</a>\n</doc>", string(data))

	// Only a NUL directly before '<' can be a line marker, raw content
	// that the encoder writes without escaping keeps every other NUL
	type rawDoc struct {
		XMLName xml.Name `xml:"doc"`
		Inner   string   `xml:",innerxml"`
	}
	data, err = xmlMarshalIndent(rawDoc{Inner: "<x>a\x00b</x>\n\x00<y/>\n\x00z"}, "")
	require.NoError(t, err)
	require.Equal(t, "<doc><x>a\x00b</x>\n<y/>\n\x00z</doc>", string(data))

	// The encoder flushes its 4096 byte buffer between the line marker
	// of the second item (the last buffered byte) and the following '<'
	long := strings.Repeat("x", 4074)
	data, err = xmlMarshalIndent(doc{Items: []item{{Name: "a", Text: long}, {Name: "b", Text: "y"}}}, "")
	require.NoError(t, err)
	require.Equal(t, "<doc>\n<item name=\"a\">"+long+"</item>\n<item name=\"b\">y</item>\n</doc>", string(data))

	// Removing the indentation must not join the bytes around a NUL
	// to markup: "]]\x00>" in a CDATA section must not become "]]>"
	type cdataDoc struct {
		XMLName xml.Name `xml:"doc"`
		Data    string   `xml:",cdata"`
	}
	data, err = xmlMarshalIndent(cdataDoc{Data: "]]\x00><b/>"}, "")
	require.NoError(t, err)
	require.Equal(t, "<doc><![CDATA[]]\x00><b/>]]></doc>", string(data))

	_, err = xmlMarshalIndent(make(chan int), "")
	require.Error(t, err)
}

// TestNewFileReadWriteAllSeekCloser checks that the reads see the content
// of the file and that the changes are written back with Close.
func TestNewFileReadWriteAllSeekCloser(t *testing.T) {
	ctx := t.Context()
	file := File(t.TempDir()).Join("rw.txt")
	require.NoError(t, file.WriteAllString(ctx, "hello world"))

	rw := NewFileReadWriteAllSeekCloser(file)
	buf := make([]byte, 5)
	_, err := io.ReadFull(rw, buf)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(buf))
	_, err = rw.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	_, err = rw.Write([]byte("!"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", mustReadString(t, file), "the changes are buffered until Close")
	require.NoError(t, rw.Close())
	assert.Equal(t, "hello world!", mustReadString(t, file))
}
