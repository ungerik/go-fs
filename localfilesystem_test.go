package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// watchTimeout is how long the watch tests wait for the
// events that the watcher reports asynchronously.
const watchTimeout = 5 * time.Second

func Test_LocalFileSystem_MoveSameSrcDest(t *testing.T) {
	tmp := MustMakeTempDir()
	t.Cleanup(func() { _ = tmp.RemoveRecursive(context.Background()) })

	file := tmp.Join("a.txt")
	require.NoError(t, file.WriteAll(t.Context(), []byte("hello")))

	// File: Move(src, src) is a no-op, matching os.Rename.
	srcPath := file.LocalPath()
	require.NoError(t, Local.Move(t.Context(), srcPath, srcPath), "Move(file, file) must be a no-op")
	got, err := file.ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "hello", got, "file content preserved")

	// Same after cleaning (./a.txt should normalize to a.txt).
	noisy := filepath.Join(filepath.Dir(srcPath), ".", filepath.Base(srcPath))
	require.NoError(t, Local.Move(t.Context(), srcPath, noisy), "Move with redundant path components is still a no-op")
	require.True(t, file.Exists(), "file still present after no-op move with redundant path")

	// Directory: Move(dir, dir) is also a no-op (matches os.Rename, even
	// though our usual Move(dir, dir-that-exists) semantics would otherwise
	// append a base name).
	dir := tmp.Join("subdir")
	require.NoError(t, dir.MakeDir())
	dirPath := dir.LocalPath()
	require.NoError(t, Local.Move(t.Context(), dirPath, dirPath), "Move(dir, dir) must be a no-op")
	assert.True(t, dir.IsDir(), "directory still present after no-op move")
}

// setupLocalMove creates these files in a new temp directory and returns it:
//
//	src.txt          "source"
//	dest.txt         "destination"
//	srcdir/child.txt "child"
//	emptydir/
func setupLocalMove(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src.txt"), []byte("source"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dest.txt"), []byte("destination"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "srcdir"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "srcdir", "child.txt"), []byte("child"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "emptydir"), 0o700))
	return dir
}

// requireLocalContent checks that the file at filePath has the content want.
func requireLocalContent(t *testing.T, filePath, want string) {
	t.Helper()
	data, err := os.ReadFile(filePath) //#nosec G304
	require.NoError(t, err, "reading %s", filePath)
	require.Equal(t, want, string(data), "content of %s", filePath)
}

func TestLocalFileSystem_Move(t *testing.T) {
	t.Run("Canceled", func(t *testing.T) {
		dir := setupLocalMove(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := Local.Move(ctx, filepath.Join(dir, "src.txt"), filepath.Join(dir, "moved.txt"))
		require.ErrorIs(t, err, context.Canceled)
		requireLocalContent(t, filepath.Join(dir, "src.txt"), "source")
		assert.NoFileExists(t, filepath.Join(dir, "moved.txt"))
	})

	t.Run("OntoEmptyDirectory", func(t *testing.T) {
		// os.Rename replaces an empty directory on some platforms
		dir := setupLocalMove(t)
		for _, src := range []string{"srcdir", "src.txt"} {
			err := Local.Move(t.Context(), filepath.Join(dir, src), filepath.Join(dir, "emptydir"))
			require.ErrorIs(t, err, os.ErrExist, "Move of %s onto an empty directory", src)
			require.ErrorIs(t, err, ErrAlreadyExists{}, "Move of %s onto an empty directory", src)
		}
		requireLocalContent(t, filepath.Join(dir, "src.txt"), "source")
		requireLocalContent(t, filepath.Join(dir, "srcdir", "child.txt"), "child")
		entries, err := os.ReadDir(filepath.Join(dir, "emptydir"))
		require.NoError(t, err)
		require.Empty(t, entries, "the empty directory must stay empty")
	})

	t.Run("DirectoryOntoFile", func(t *testing.T) {
		dir := setupLocalMove(t)
		err := Local.Move(t.Context(), filepath.Join(dir, "srcdir"), filepath.Join(dir, "dest.txt"))
		require.ErrorIs(t, err, ErrIsNotDirectory{})
		requireLocalContent(t, filepath.Join(dir, "dest.txt"), "destination")
		requireLocalContent(t, filepath.Join(dir, "srcdir", "child.txt"), "child")
	})

	t.Run("IntoItself", func(t *testing.T) {
		dir := setupLocalMove(t)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "srcdir", "sub"), 0o700))
		t.Chdir(dir)
		// A relative source and an absolute destination are compared as absolute paths
		for _, dest := range []string{
			filepath.Join("srcdir", "new"),
			filepath.Join(dir, "srcdir", "sub", "new"),
		} {
			err := Local.Move(t.Context(), "srcdir", dest)
			require.ErrorIs(t, err, os.ErrInvalid, "Move of srcdir to %s", dest)
			assert.NoDirExists(t, dest)
		}
		requireLocalContent(t, filepath.Join(dir, "srcdir", "child.txt"), "child")
	})

	t.Run("CaseOnlyRename", func(t *testing.T) {
		// On a case-insensitive file system the destination exists:
		// it is the source itself, which must be renamed, not rejected
		dir := setupLocalMove(t)
		require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "srcdir"), filepath.Join(dir, "SRCDIR")))
		require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "src.txt"), filepath.Join(dir, "SRC.txt")))
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		assert.ElementsMatch(t, []string{"SRC.txt", "SRCDIR", "dest.txt", "emptydir"}, names)
		requireLocalContent(t, filepath.Join(dir, "SRCDIR", "child.txt"), "child")
	})

	t.Run("Overwrite", func(t *testing.T) {
		dir := setupLocalMove(t)
		require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "src.txt"), filepath.Join(dir, "dest.txt")))
		requireLocalContent(t, filepath.Join(dir, "dest.txt"), "source")
		assert.NoFileExists(t, filepath.Join(dir, "src.txt"))
	})
}

func TestLocalFileSystem_MoveCrossDevice(t *testing.T) {
	crossDevice := error(syscall.EXDEV)
	if runtime.GOOS == "windows" {
		crossDevice = syscall.Errno(0x11) // ERROR_NOT_SAME_DEVICE
	}
	renames := 0
	osRename = func(oldPath, newPath string) error {
		renames++
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: crossDevice}
	}
	t.Cleanup(func() { osRename = os.Rename })

	dir := setupLocalMove(t)
	require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "srcdir"), filepath.Join(dir, "newdir")))
	requireLocalContent(t, filepath.Join(dir, "newdir", "child.txt"), "child")
	assert.NoDirExists(t, filepath.Join(dir, "srcdir"))

	require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "src.txt"), filepath.Join(dir, "dest.txt")))
	requireLocalContent(t, filepath.Join(dir, "dest.txt"), "source")
	assert.NoFileExists(t, filepath.Join(dir, "src.txt"))

	// A symbolic link is moved as link, like os.Rename does it
	if err := os.Symlink("dest.txt", filepath.Join(dir, "link")); err != nil {
		t.Logf("can't test moving a symbolic link: %s", err)
	} else {
		require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "link"), filepath.Join(dir, "moved-link")))
		target, err := os.Readlink(filepath.Join(dir, "moved-link"))
		require.NoError(t, err, "the moved link must be a symbolic link")
		assert.Equal(t, "dest.txt", target)
		assert.NoFileExists(t, filepath.Join(dir, "link"))
	}
	assert.Positive(t, renames, "Move must try os.Rename first")
}

func TestLocalFileSystem_DirectoryErrors(t *testing.T) {
	ctx := t.Context()
	dir := setupLocalMove(t)
	srcdir := filepath.Join(dir, "srcdir")
	file := filepath.Join(dir, "src.txt")

	r, err := Local.OpenReader(srcdir)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "OpenReader")
	assert.True(t, r == nil, "OpenReader must not return a reader with an error")
	_, err = Local.ReadAll(ctx, srcdir)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "ReadAll")

	err = Local.WriteAll(ctx, srcdir, []byte("data"), 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "WriteAll")
	w, err := Local.OpenWriter(srcdir, 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "OpenWriter")
	assert.True(t, w == nil, "OpenWriter must not return a writer with an error")
	w, err = Local.OpenAppendWriter(srcdir, 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "OpenAppendWriter")
	assert.True(t, w == nil, "OpenAppendWriter must not return a writer with an error")
	rw, err := Local.OpenReadWriter(srcdir, 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "OpenReadWriter")
	assert.True(t, rw == nil, "OpenReadWriter must not return a file with an error")
	err = Local.Append(ctx, srcdir, []byte("data"), 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "Append")
	err = Local.Truncate(ctx, srcdir, 0)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "Truncate")

	err = Local.CopyFile(ctx, file, srcdir)
	assert.ErrorIs(t, err, ErrIsDirectory{}, "CopyFile onto a directory")
	err = Local.CopyFile(ctx, file, filepath.Join(dir, "emptydir"))
	assert.ErrorIs(t, err, ErrIsDirectory{}, "CopyFile onto an empty directory")
	err = Local.CopyFile(ctx, srcdir, filepath.Join(dir, "copy"))
	assert.ErrorIs(t, err, ErrIsDirectory{}, "CopyFile of a directory")
	assert.NoFileExists(t, filepath.Join(dir, "copy"))

	err = Local.Remove(srcdir)
	assert.ErrorIs(t, err, ErrDirectoryNotEmpty{}, "Remove of a non-empty directory")
	requireLocalContent(t, filepath.Join(srcdir, "child.txt"), "child")

	if runtime.GOOS != "windows" {
		// ENOTDIR, Windows reports a file used as directory as missing
		_, err = Local.Stat(filepath.Join(file, "child"))
		assert.ErrorIs(t, err, ErrIsNotDirectory{}, "Stat below a file")
	}
}

// pathNotFoundError is an error that matches both os.ErrNotExist and
// syscall.ENOTDIR, like ERROR_PATH_NOT_FOUND on Windows, where
// syscall.ENOTDIR is defined as ERROR_PATH_NOT_FOUND.
type pathNotFoundError struct{}

func (pathNotFoundError) Error() string { return "the system cannot find the path specified" }

func (pathNotFoundError) Is(target error) bool {
	return target == os.ErrNotExist || target == syscall.ENOTDIR
}

// TestLocalFileSystem_PathNotFound checks that a missing parent directory
// on Windows is reported as not existing, not as "not a directory".
func TestLocalFileSystem_PathNotFound(t *testing.T) {
	err := &os.PathError{Op: "open", Path: "missing/file.txt", Err: pathNotFoundError{}}
	assert.ErrorIs(t, wrapOSErr("missing/file.txt", err), os.ErrNotExist)
	assert.NotErrorIs(t, wrapOSErr("missing/file.txt", err), ErrIsNotDirectory{})
	assert.NotErrorIs(t, wrapRenameErr("missing/dest", err), ErrIsNotDirectory{})

	notDir := &os.PathError{Op: "open", Path: "file/child", Err: syscall.ENOTDIR}
	if !errors.Is(notDir, os.ErrNotExist) {
		// Unix: ENOTDIR means a file is in the way
		assert.ErrorIs(t, wrapOSErr("file/child", notDir), ErrIsNotDirectory{})
		assert.ErrorIs(t, wrapRenameErr("file/child", notDir), ErrIsNotDirectory{})
	}
}

func TestLocalFileSystem_MakeAllDirs(t *testing.T) {
	localFileSystem := LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite,
	}
	dir := filepath.Join(t.TempDir(), "a", "b")
	require.NoError(t, localFileSystem.MakeAllDirs(dir, 0))
	assert.DirExists(t, dir)
	require.NoError(t, localFileSystem.MakeAllDirs(dir, 0), "MakeAllDirs of an existing directory")

	file := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	require.ErrorIs(t, localFileSystem.MakeAllDirs(file, 0), ErrIsNotDirectory{})
}

func TestLocalFileSystem_MakeAllDirsOthersWrite(t *testing.T) {
	if extraDirPermissions == 0 {
		t.Skip("OthersWrite is only set by a chmod after the umask on Unix")
	}
	parent := t.TempDir()
	parentInfo, err := os.Stat(parent)
	require.NoError(t, err)

	// The chmod must only change the directories that MakeAllDirs created,
	// with an absolute path as with a relative one
	dirPath := filepath.Join(parent, "a", "b")
	require.NoError(t, new(LocalFileSystem).MakeAllDirs(dirPath, AllReadWrite))
	for _, dir := range []string{filepath.Join(parent, "a"), dirPath} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o777), info.Mode().Perm(), "permissions of %s", dir)
	}
	parentAfter, err := os.Stat(parent)
	require.NoError(t, err)
	assert.Equal(t, parentInfo.Mode(), parentAfter.Mode(), "the existing parent must keep its permissions")

	t.Chdir(parent)
	require.NoError(t, new(LocalFileSystem).MakeAllDirs(filepath.Join("c", "d"), AllReadWrite))
	info, err := os.Stat(filepath.Join(parent, "c", "d"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o777), info.Mode().Perm(), "permissions of a relative path")
}

func TestLocalFileSystem_ZeroValue(t *testing.T) {
	var local LocalFileSystem
	ctx := t.Context()
	dir := t.TempDir()

	// The zero value falls back to the default permissions,
	// zero permission fields don't create unreadable files
	createFile := map[string]func(filePath string) error{
		"WriteAll": func(filePath string) error { return local.WriteAll(ctx, filePath, []byte("x"), 0) },
		"Append":   func(filePath string) error { return local.Append(ctx, filePath, []byte("x"), 0) },
		"Touch":    func(filePath string) error { return local.Touch(filePath, 0) },
		"OpenWriter": func(filePath string) error {
			w, err := local.OpenWriter(filePath, 0)
			if err != nil {
				return err
			}
			return w.Close()
		},
		"OpenAppendWriter": func(filePath string) error {
			w, err := local.OpenAppendWriter(filePath, 0)
			if err != nil {
				return err
			}
			return w.Close()
		},
		"OpenReadWriter": func(filePath string) error {
			rw, err := local.OpenReadWriter(filePath, 0)
			if err != nil {
				return err
			}
			return rw.Close()
		},
	}
	for name, create := range createFile {
		filePath := filepath.Join(dir, name)
		require.NoError(t, create(filePath), name)
		info, err := os.Stat(filePath)
		require.NoError(t, err, name)
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm()&0o600, "%s must create a file the user can read and write", name)
		}
		_, err = os.ReadFile(filePath) //#nosec G304
		assert.NoError(t, err, "%s must create a readable file", name)
	}

	createDir := map[string]func(dirPath string) error{
		"MakeDir":     func(dirPath string) error { return local.MakeDir(dirPath, 0) },
		"MakeAllDirs": func(dirPath string) error { return local.MakeAllDirs(dirPath, 0) },
	}
	for name, create := range createDir {
		dirPath := filepath.Join(dir, name+"-dir")
		require.NoError(t, create(dirPath), name)
		info, err := os.Stat(dirPath)
		require.NoError(t, err, name)
		if runtime.GOOS != "windows" {
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm()&0o700, "%s must create a directory the user can use", name)
		}
		assert.NoError(t, os.WriteFile(filepath.Join(dirPath, "file.txt"), nil, 0o600), "%s must create a writable directory", name)
	}
}

func TestLocalFileSystem_ListDirSkipsDeletedEntries(t *testing.T) {
	dir := t.TempDir()
	names := []string{"a", "b", "c"}
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}
	var listed []string
	err := Local.ListDir(t.Context(), dir, nil, func(info *FileInfo) error {
		if len(listed) == 0 {
			// Delete the other entries after the directory was read
			for _, name := range names {
				if name != info.Name {
					require.NoError(t, os.Remove(filepath.Join(dir, name)))
				}
			}
		}
		listed = append(listed, info.Name)
		return nil
	})
	require.NoError(t, err, "ListDir must skip entries that are deleted while it reads the directory")
	if runtime.GOOS != "windows" {
		// Windows reads the FileInfo of the entries with the directory
		assert.Len(t, listed, 1, "only the entry that was not deleted is listed")
	}
}

func Test_LocalFileSystem_SplitDirAndName(t *testing.T) {
	root := Local.Separator()

	dir, name := Local.SplitDirAndName(root)
	assert.Equal(t, root, dir)
	assert.Equal(t, "", name)

	dir, name = Local.SplitDirAndName(root + "FileInRoot")
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)

	dir, name = Local.SplitDirAndName(root + "FileInRoot" + Local.Separator())
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)
}

func Test_expandTilde(t *testing.T) {
	currentUser, err := user.Current()
	require.NoError(t, err, "user.Current() should not error")
	require.NotNil(t, currentUser, "currentUser should not be nil")
	require.NotEmpty(t, currentUser.HomeDir, "HomeDir should not be empty")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde only",
			input:    "~",
			expected: currentUser.HomeDir,
		},
		{
			name:     "tilde with path",
			input:    "~/Documents",
			expected: filepath.Join(currentUser.HomeDir, "Documents"),
		},
		{
			name:     "tilde with platform separator",
			input:    "~" + Separator + "Documents",
			expected: filepath.Join(currentUser.HomeDir, "Documents"),
		},
		{
			name:     "tilde with nested path",
			input:    "~/Documents/test/file.txt",
			expected: filepath.Join(currentUser.HomeDir, "Documents/test/file.txt"),
		},
		{
			name:     "tilde followed by a name",
			input:    "~foo",
			expected: "~foo",
		},
		{
			name:     "tilde followed by a name and a path",
			input:    "~foo/bar",
			expected: "~foo/bar",
		},
		{
			name:     "no tilde at start",
			input:    "/usr/local/bin",
			expected: "/usr/local/bin",
		},
		{
			name:     "tilde in middle",
			input:    "/path/~/test",
			expected: "/path/~/test",
		},
		{
			name:     "relative path without tilde",
			input:    "relative/path",
			expected: "relative/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func Test_expandTilde_HonorsEnv(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv(homeEnvVar(), fakeHome)

	require.Equal(t, fakeHome, expandTilde("~"),
		"expandTilde(~) must use %s before falling back to user.Current", homeEnvVar())
	require.Equal(t, filepath.Join(fakeHome, "foo"), expandTilde("~/foo"))
	require.Equal(t, filepath.Join(fakeHome, "Documents/test/file.txt"),
		expandTilde("~/Documents/test/file.txt"))
}

func TestLocalFileSystem_URL(t *testing.T) {
	// ParseRawURI decodes the URL escapes of a file:// URI,
	// but never those of a local path without the prefix.
	localPath := filepath.Join(t.TempDir(), "100%25 a%20b.txt")
	file := File(localPath)
	assert.Equal(t, localPath, file.Path(), "a local path is not decoded")
	assert.Equal(t, "100%25 a%20b.txt", file.Name())

	uri := file.URL()
	assert.True(t, strings.HasPrefix(uri, LocalPrefix), "URL %q has the local prefix", uri)
	assert.True(t, strings.HasSuffix(uri, "/100%2525 a%2520b.txt"),
		"URL %q escapes every %%", uri)
	assert.Equal(t, localPath, File(uri).Path(), "the URL resolves to the path")
	assert.Equal(t, "100%25 a%20b.txt", File(uri).Name())
}

func TestLocalFileSystem_ID(t *testing.T) {
	id := Local.ID()
	assert.NotEmpty(t, id)
	assert.Equal(t, id, new(LocalFileSystem).ID(), "every LocalFileSystem has the ID of the root file system")
}

// requireWatchGoroutines waits until n goroutines
// of the watcher and the watches of local are running.
func requireWatchGoroutines(t *testing.T, local *LocalFileSystem, n int32, msg string) {
	t.Helper()
	deadline := time.Now().Add(watchTimeout)
	for {
		running := local.watchGoroutines.Load()
		if running == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d watch goroutines are running after %s, want %d", msg, running, watchTimeout, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// receiveFile waits until the file is received from files.
func receiveFile(t *testing.T, files <-chan File, file string, msg string) {
	t.Helper()
	timeout := time.After(watchTimeout)
	for {
		select {
		case f := <-files:
			if f == File(file) {
				return
			}
		case <-timeout:
			require.FailNow(t, "no event", "%s: no event for %s within %s", msg, file, watchTimeout)
		}
	}
}

// receive waits for a value from ch.
func receive[T any](t *testing.T, ch <-chan T, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(watchTimeout):
		require.FailNow(t, "timeout", "%s: nothing received within %s", msg, watchTimeout)
		panic("unreachable")
	}
}

func TestLocalFileSystem_WatchCloseDuringCallback(t *testing.T) {
	dir := t.TempDir()
	local := new(LocalFileSystem)
	t.Cleanup(func() { _ = local.Close() })

	started := make(chan struct{}, 100)
	release := make(chan struct{})
	_, err := local.Watch(dir, func(File, Event) {
		started <- struct{}{}
		<-release
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o600))
	receive(t, started, "callback")

	// Close while the callback is running, the goroutines
	// must end cleanly after it returned
	require.NoError(t, local.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b"), []byte("b"), 0o600))
	close(release)
	requireWatchGoroutines(t, local, 0, "after Close")
	assert.Empty(t, started, "no callback after Close")
}

func TestLocalFileSystem_WatchAfterClose(t *testing.T) {
	dir := t.TempDir()
	local := new(LocalFileSystem)
	t.Cleanup(func() { _ = local.Close() })

	started := make(chan struct{}, 100)
	release := make(chan struct{})
	_, err := local.Watch(dir, func(File, Event) {
		started <- struct{}{}
		<-release
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o600))
	receive(t, started, "callback")
	require.NoError(t, local.Close())

	// A Watch after Close starts a new watcher, the goroutine
	// of the closed one must not take over the new watcher
	files := make(chan File, 100)
	cancel, err := local.Watch(dir, func(f File, _ Event) { files <- f })
	require.NoError(t, err)
	close(release)
	// The loop of the new watcher and the delivery of its watch
	requireWatchGoroutines(t, local, 2, "after the Watch after Close")

	b := filepath.Join(dir, "b")
	require.NoError(t, os.WriteFile(b, []byte("b"), 0o600))
	receiveFile(t, files, b, "the watch after Close")

	require.NoError(t, cancel())
	requireWatchGoroutines(t, local, 0, "after canceling the last watch")
}

func TestLocalFileSystem_WatchSlowCallback(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	local := new(LocalFileSystem)
	t.Cleanup(func() { _ = local.Close() })

	blocked := make(chan struct{}, 100)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_, err := local.Watch(dirA, func(File, Event) {
		blocked <- struct{}{}
		<-release
	})
	require.NoError(t, err)
	filesB := make(chan File, 100)
	_, err = local.Watch(dirB, func(f File, _ Event) { filesB <- f })
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dirA, "a"), []byte("a"), 0o600))
	receive(t, blocked, "callback of the watch of dirA")

	// The blocked callback of the watch of dirA must not delay the watch of dirB
	b := filepath.Join(dirB, "b")
	require.NoError(t, os.WriteFile(b, []byte("b"), 0o600))
	receiveFile(t, filesB, b, "the watch of dirB while the callback of dirA blocks")
}

func TestLocalFileSystem_WatchEventOrder(t *testing.T) {
	type fileEvent struct {
		file  File
		event Event
	}
	var (
		mtx        sync.Mutex
		received   []fileEvent // by the watcher, in order
		called     []fileEvent // by the callback, in order
		running    atomic.Int32
		concurrent atomic.Bool
	)
	local := &LocalFileSystem{
		WatchEventLogger: LoggerFunc(func(format string, args ...any) {
			if len(args) == 1 {
				if e, ok := args[0].(fsnotify.Event); ok {
					mtx.Lock()
					received = append(received, fileEvent{File(e.Name), eventFromFSNotify(e.Op)})
					mtx.Unlock()
				}
			}
		}),
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = local.Close() })

	cancel, err := local.Watch(dir, func(file File, event Event) {
		if running.Add(1) > 1 {
			concurrent.Store(true)
		}
		defer running.Add(-1)
		time.Sleep(time.Millisecond) // Gives a concurrent call the chance to overlap
		mtx.Lock()
		called = append(called, fileEvent{file, event})
		mtx.Unlock()
	})
	require.NoError(t, err)

	const numFiles = 20
	for i := range numFiles {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%02d", i)), []byte("x"), 0o600))
	}
	// Both lists are prefixes of the events the watcher dispatched,
	// so with the same length they must be equal
	var gotReceived, gotCalled []fileEvent
	require.Eventually(t, func() bool {
		mtx.Lock()
		defer mtx.Unlock()
		creates := 0
		for _, e := range called {
			if e.event.HasCreate() {
				creates++
			}
		}
		if creates < numFiles || len(called) != len(received) {
			return false
		}
		gotReceived, gotCalled = slices.Clone(received), slices.Clone(called)
		return true
	}, watchTimeout, time.Millisecond, "callbacks for the CREATE events of all files")
	require.NoError(t, cancel())

	assert.Equal(t, gotReceived, gotCalled, "the callback must get the events in the order of the watcher")
	assert.False(t, concurrent.Load(), "the callbacks of one watch must be called serially")
}

func TestLocalFileSystem_WatchCancel(t *testing.T) {
	dir := t.TempDir()
	local := new(LocalFileSystem)
	t.Cleanup(func() { _ = local.Close() })

	_, err := local.Watch("", func(File, Event) {})
	require.ErrorIs(t, err, ErrEmptyPath)
	_, err = local.Watch(dir, nil)
	require.Error(t, err, "nil callback")
	_, err = local.Watch(filepath.Join(dir, "missing"), func(File, Event) {})
	require.ErrorIs(t, err, os.ErrNotExist)

	files1 := make(chan File, 100)
	cancel1, err := local.Watch(dir, func(f File, _ Event) { files1 <- f })
	require.NoError(t, err)
	files2 := make(chan File, 100)
	removed := make(chan File, 100)
	cancel2, err := local.Watch(dir, func(f File, e Event) {
		files2 <- f
		if e.HasRemove() {
			removed <- f
		}
	})
	require.NoError(t, err)

	require.NoError(t, cancel1())
	require.NoError(t, cancel1(), "cancel can be called more than once")
	a := filepath.Join(dir, "a")
	require.NoError(t, os.WriteFile(a, []byte("a"), 0o600))
	receiveFile(t, files2, a, "the watch that was not canceled")
	assert.Empty(t, files1, "no callback after cancel")

	// A watch of a file that gets deleted
	cancelA, err := local.Watch(a, func(File, Event) {})
	require.NoError(t, err)
	require.NoError(t, os.Remove(a))
	receiveFile(t, removed, a, "the removal")
	require.NoError(t, cancelA(), "cancel of a watch of a deleted file")

	require.NoError(t, cancel2())
	requireWatchGoroutines(t, local, 0, "the last cancel closes the watcher")

	cancel3, err := local.Watch(dir, func(File, Event) {})
	require.NoError(t, err)
	require.NoError(t, local.Close())
	require.NoError(t, local.Close(), "Close can be called more than once")
	require.NoError(t, cancel3(), "cancel after Close")
	requireWatchGoroutines(t, local, 0, "after Close")
}

func TestLocalFileSystem_WatchLoggers(t *testing.T) {
	events := make(chan string, 100)
	errs := make(chan string, 100)
	send := func(ch chan string) Logger {
		return LoggerFunc(func(format string, args ...any) {
			select {
			case ch <- fmt.Sprintf(format, args...):
			default:
			}
		})
	}
	local := &LocalFileSystem{
		WatchEventLogger: send(events),
		WatchErrorLogger: send(errs),
	}
	dir := t.TempDir()
	t.Cleanup(func() { _ = local.Close() })

	_, err := local.Watch(dir, func(File, Event) { panic("callback panic") })
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o600))

	assert.Contains(t, receive(t, events, "event log"), "watch event: ")
	msg := receive(t, errs, "error log")
	assert.Contains(t, msg, "watch callback panic")
	assert.Contains(t, msg, "callback panic")
}

func TestLocalFileSystem_OpenReaderReadAll(t *testing.T) {
	// The reader of a file must support ReaderAt and Seeker
	filePath := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello world"), 0o600))
	r, err := Local.OpenReader(filePath)
	require.NoError(t, err)
	defer r.Close()
	_, ok := r.(ReadAtSeekCloser)
	assert.True(t, ok, "the reader implements ReadAtSeekCloser")
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))

	data, err = Local.ReadAll(t.Context(), filePath)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))

	data, err = Local.ReadAll(t.Context(), filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, data)

	if runtime.GOOS == "linux" {
		// Files in /proc report the size zero
		data, err = Local.ReadAll(t.Context(), "/proc/self/status")
		require.NoError(t, err)
		assert.True(t, slices.Contains(strings.Fields(string(data)), "Name:"), "content of /proc/self/status")
	}
}
