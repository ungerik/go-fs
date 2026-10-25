package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
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
	data, err := os.ReadFile(filePath) //#nosec G304 -- the tests pass files they created in a temp directory
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

	t.Run("OntoHardLink", func(t *testing.T) {
		// Regression test: rename(2) does nothing for two hard links
		// of the same file, so Move returned nil and kept the source
		dir := setupLocalMove(t)
		src, dest := filepath.Join(dir, "src.txt"), filepath.Join(dir, "hard-link.txt")
		if err := os.Link(src, dest); err != nil {
			t.Skipf("can't create hard link: %v", err)
		}
		require.NoError(t, Local.Move(t.Context(), src, dest))
		assert.NoFileExists(t, src, "the source must be removed")
		requireLocalContent(t, dest, "source")
	})

	t.Run("SameDirectoryEntry", func(t *testing.T) {
		// Two paths of the same directory entry are the same file like hard
		// links, but rename(2) does nothing for them and the file must be kept
		dir := setupLocalMove(t)
		src := filepath.Join(dir, "srcdir", "child.txt")
		link := filepath.Join(dir, "link")
		if err := os.Symlink(filepath.Join(dir, "srcdir"), link); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		require.NoError(t, Local.Move(t.Context(), src, filepath.Join(link, "child.txt")), "through a link to the directory")
		requireLocalContent(t, src, "child")

		t.Chdir(filepath.Join(dir, "srcdir"))
		require.NoError(t, Local.Move(t.Context(), "child.txt", src), "relative and absolute path")
		requireLocalContent(t, src, "child")
	})

	t.Run("EquivalentNames", func(t *testing.T) {
		// Regression test: file systems match names with their own rules,
		// not only ignoring case like strings.EqualFold. A destination that
		// is the source under another spelling of its name was taken for
		// another hard link and the source was removed, so Move returned nil
		// and no file was left. The cases run where the file system of the
		// temp directory treats both names as one directory entry, the first
		// three on APFS, the last two on Windows.
		for _, test := range []struct {
			name      string
			src, dest string
			want      string // the name of the moved file
		}{
			{name: "NFDToNFC", src: "café.txt", dest: "café.txt", want: "café.txt"},
			{name: "SharpS", src: "straße.txt", dest: "STRASSE.txt", want: "STRASSE.txt"},
			{name: "Ligature", src: "ﬁle.txt", dest: "file.txt", want: "file.txt"},
			{name: "TrailingDot", src: "file.txt", dest: "file.txt.", want: "file.txt"},
			{name: "TrailingSpace", src: "file.txt", dest: "file.txt ", want: "file.txt"},
		} {
			// Another hard link in another directory gives the file a link
			// count of 2, so the link count doesn't tell that both names
			// are the same directory entry
			for _, otherLink := range []bool{false, true} {
				name := test.name
				if otherLink {
					name += "WithOtherHardLink"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					src, dest := filepath.Join(dir, test.src), filepath.Join(dir, test.dest)
					require.NoError(t, os.WriteFile(src, []byte("source"), 0o600))
					srcInfo, err := os.Lstat(src)
					require.NoError(t, err)
					destInfo, err := os.Lstat(dest)
					if err != nil || !os.SameFile(srcInfo, destInfo) {
						t.Skipf("the file system of the temp directory treats %+q and %+q as different names", test.src, test.dest)
					}
					other := filepath.Join(t.TempDir(), "other-link.txt")
					if otherLink {
						if err := os.Link(src, other); err != nil {
							t.Skipf("can't create hard link: %v", err)
						}
					}

					require.NoError(t, Local.Move(t.Context(), src, dest))
					assert.Equal(t, []string{test.want}, localDirNames(t, dir), "the file must be renamed, not removed")
					requireLocalContent(t, filepath.Join(dir, test.want), "source")
					if otherLink {
						requireLocalContent(t, other, "source")
					}
				})
			}
		}
	})

	t.Run("CaseOnlyHardLinks", func(t *testing.T) {
		// Regression test: two hard links whose names only differ in case
		// were taken for the same directory entry on a case-sensitive
		// file system and renamed, but rename(2) does nothing for two links
		// of the same file, so Move returned nil and kept the source
		dir := t.TempDir()
		src, dest := filepath.Join(dir, "File.txt"), filepath.Join(dir, "file.txt")
		require.NoError(t, os.WriteFile(src, []byte("source"), 0o600))
		if err := os.Link(src, dest); err != nil {
			t.Skipf("can't create hard links whose names only differ in case, the temp directory is case-insensitive: %v", err)
		}
		require.NoError(t, Local.Move(t.Context(), src, dest))
		assert.Equal(t, []string{"file.txt"}, localDirNames(t, dir), "the source link must be removed")
		requireLocalContent(t, dest, "source")
	})

	t.Run("LinkOntoItsTarget", func(t *testing.T) {
		// Regression test: the link replaced the file it points to and then
		// pointed to itself, so the file was gone and Move returned nil
		dir := setupLocalMove(t)
		dest := filepath.Join(dir, "dest.txt")
		for _, target := range []string{"dest.txt", dest} {
			link := filepath.Join(dir, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("can't create symbolic link: %v", err)
			}
			err := Local.Move(t.Context(), link, dest)
			require.ErrorIs(t, err, os.ErrInvalid, "link to %s", target)
			requireLocalContent(t, dest, "destination")
			stored, err := os.Readlink(link)
			require.NoError(t, err, "the link must be kept")
			assert.Equal(t, target, stored)
			require.NoError(t, os.Remove(link))
		}
	})

	t.Run("LinkOntoItsTargetCrossDevice", func(t *testing.T) {
		// The fallback of copying the link must not replace its target either
		dir := setupLocalMove(t)
		dest, link := filepath.Join(dir, "dest.txt"), filepath.Join(dir, "link")
		if err := os.Symlink("dest.txt", link); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		osRename = func(oldPath, newPath string) error {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
		}
		t.Cleanup(func() { osRename = os.Rename })

		err := Local.Move(t.Context(), link, dest)
		require.ErrorIs(t, err, os.ErrInvalid)
		requireLocalContent(t, dest, "destination")
		_, err = os.Readlink(link)
		require.NoError(t, err, "the link must be kept")
	})

	t.Run("LinkOntoItsTargetWithOtherHardLink", func(t *testing.T) {
		// The file is kept by its other hard link, so the link replaces
		// the destination like any other file, like mv does it
		if runtime.GOOS == "windows" {
			t.Skip("the link count is unknown on Windows, the move is refused")
		}
		dir := setupLocalMove(t)
		dest, other, link := filepath.Join(dir, "dest.txt"), filepath.Join(dir, "other.txt"), filepath.Join(dir, "link")
		if err := os.Link(dest, other); err != nil {
			t.Skipf("can't create hard link: %v", err)
		}
		if err := os.Symlink("other.txt", link); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		require.NoError(t, Local.Move(t.Context(), link, dest))
		stored, err := os.Readlink(dest)
		require.NoError(t, err, "the destination must be the link")
		assert.Equal(t, "other.txt", stored)
		requireLocalContent(t, dest, "destination")
		assert.NoFileExists(t, link)
	})

	t.Run("OntoHardLinkFails", func(t *testing.T) {
		// The source link is moved to a temporary name before the
		// destination is replaced. If that fails, the source link
		// must be back at its name and no temporary file left.
		dir := t.TempDir()
		src, dest := filepath.Join(dir, "src.txt"), filepath.Join(dir, "hard-link.txt")
		require.NoError(t, os.WriteFile(src, []byte("source"), 0o600))
		if err := os.Link(src, dest); err != nil {
			t.Skipf("can't create hard link: %v", err)
		}
		osRename = func(oldPath, newPath string) error {
			if newPath == dest {
				return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: os.ErrPermission}
			}
			return os.Rename(oldPath, newPath)
		}
		t.Cleanup(func() { osRename = os.Rename })

		err := Local.Move(t.Context(), src, dest)
		require.ErrorIs(t, err, os.ErrPermission)
		assert.ElementsMatch(t, []string{"src.txt", "hard-link.txt"}, localDirNames(t, dir), "both links must be kept")
		requireLocalContent(t, src, "source")
		requireLocalContent(t, dest, "source")
	})
}

// localDirNames returns the names of the entries of the directory dir.
func localDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
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

		// A symbolic link replaces an existing file like with os.Rename,
		// this was an ErrAlreadyExists
		require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("file"), 0o600))
		require.NoError(t, Local.Move(t.Context(), filepath.Join(dir, "moved-link"), filepath.Join(dir, "file.txt")))
		target, err = os.Readlink(filepath.Join(dir, "file.txt"))
		require.NoError(t, err, "the file must be replaced with the link")
		assert.Equal(t, "dest.txt", target)
		assert.NoFileExists(t, filepath.Join(dir, "moved-link"))
		requireLocalContent(t, filepath.Join(dir, "dest.txt"), "source")
	}
	assert.Positive(t, renames, "Move must try os.Rename first")
}

// TestLocalFileSystem_MoveSameFileCrossDevice verifies that Move doesn't
// fall back to copying when the source and the destination are the same
// file or directory. rename(2) fails with EXDEV for the paths of a file
// through two bind mounts of its directory on Linux, like
// docker run -v /data:/a -v /data:/b. Copying the file onto itself and
// removing the source destroyed it. A symbolic link to the directory
// stands in for the bind mount.
func TestLocalFileSystem_MoveSameFileCrossDevice(t *testing.T) {
	crossDevice := error(syscall.EXDEV)
	if runtime.GOOS == "windows" {
		crossDevice = syscall.Errno(0x11) // ERROR_NOT_SAME_DEVICE
	}
	osRename = func(oldPath, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: crossDevice}
	}
	t.Cleanup(func() { osRename = os.Rename })
	dir := setupLocalMove(t)
	srcdir, alias := filepath.Join(dir, "srcdir"), filepath.Join(dir, "alias")
	if err := os.Symlink(srcdir, alias); err != nil {
		t.Skipf("can't create symbolic link: %v", err)
	}
	require.NoError(t, os.Mkdir(filepath.Join(srcdir, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(srcdir, "sub", "file.txt"), []byte("file"), 0o600))

	err := Local.Move(t.Context(), filepath.Join(srcdir, "child.txt"), filepath.Join(alias, "child.txt"))
	assert.ErrorIs(t, err, crossDevice, "a file onto itself: the rename error, no copy")
	requireLocalContent(t, filepath.Join(srcdir, "child.txt"), "child")

	err = Local.Move(t.Context(), filepath.Join(alias, "sub"), filepath.Join(srcdir, "sub"))
	assert.ErrorIs(t, err, crossDevice, "a directory onto itself: the rename error, no copy")
	requireLocalContent(t, filepath.Join(srcdir, "sub", "file.txt"), "file")

	err = Local.Move(t.Context(), filepath.Join(alias, "sub"), filepath.Join(srcdir, "sub", "inner"))
	require.ErrorIs(t, err, os.ErrInvalid, "a directory into itself")
	requireLocalContent(t, filepath.Join(srcdir, "sub", "file.txt"), "file")
	assert.NoDirExists(t, filepath.Join(srcdir, "sub", "inner"), "nothing must be copied")
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
		_, err = os.ReadFile(filePath) //#nosec G304 -- a file the test created in t.TempDir()
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

// TestLocalFileSystem_CleanPathTilde is a regression test: CleanPath
// expanded the tilde after joining and cleaning all parts, so a name
// below a relative directory reached the home directory, like the name
// "~/private.txt" of File(".").StdFS(), the path "/~/private.txt"
// of a SubFileSystem rooted at "." or File(".").Join("~", "private.txt").
func TestLocalFileSystem_CleanPathTilde(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	t.Setenv(homeEnvVar(), home)
	require.NoError(t, os.WriteFile(filepath.Join(home, "private.txt"), []byte("private"), 0o600))
	work := t.TempDir()
	t.Chdir(work)

	// Only a tilde that starts the first part is the home directory
	assert.Equal(t, home, Local.CleanPath("~"))
	assert.Equal(t, filepath.Join(home, "x"), Local.CleanPath("~/x"))
	assert.Equal(t, filepath.Join(home, "x"), Local.CleanPath("~", "x"))
	assert.Equal(t, filepath.Join(home, "x"), Local.CleanPath(LocalPrefix+"~/x"))
	assert.Equal(t, filepath.Dir(home), Local.CleanPath("~/.."), "the tilde is expanded before cleaning")
	// A directory named "~" stays relative, also when the path is cleaned again
	literal := "." + Separator + filepath.Join("~", "x")
	assert.Equal(t, literal, Local.CleanPath(".", "~", "x"))
	assert.Equal(t, literal, Local.CleanPath(".", "~/x"))
	assert.Equal(t, literal, Local.CleanPath("./~/x"))
	assert.Equal(t, literal, Local.CleanPath("a", "..", "~", "x"))
	assert.Equal(t, literal, Local.CleanPath(literal))
	assert.Equal(t, "."+Separator+"~", Local.CleanPath(".", "~"))
	assert.Equal(t, filepath.Join(work, "~", "x"), Local.CleanPath(work, "~", "x"))

	stdFS := File(".").StdFS()
	subFS, err := NewSubFileSystem(Local, ".", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = subFS.Close() })
	joined := File(".").Join("~", "private.txt")

	_, err = iofs.ReadFile(stdFS, "~/private.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "StdFS must not reach the home directory")
	_, err = subFS.ReadAll(ctx, "/~/private.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "SubFileSystem must not reach the home directory")
	assert.False(t, joined.Exists(), "%s must not be in the home directory", joined)

	// A directory named "~" in the working directory is reachable
	require.NoError(t, os.Mkdir(filepath.Join(work, "~"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(work, "~", "private.txt"), []byte("work"), 0o600))
	data, err := iofs.ReadFile(stdFS, "~/private.txt")
	require.NoError(t, err)
	assert.Equal(t, "work", string(data))
	data, err = subFS.ReadAll(ctx, "/~/private.txt")
	require.NoError(t, err)
	assert.Equal(t, "work", string(data))
	data, err = joined.ReadAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, "work", string(data))

	// The files listed in such a directory are below it, not in the home directory
	var listed []File
	require.NoError(t, joined.Dir().ListDir(ctx, func(file File) error {
		listed = append(listed, file)
		return nil
	}))
	require.Equal(t, []File{joined}, listed)
	data, err = listed[0].ReadAll(ctx)
	require.NoError(t, err)
	assert.Equal(t, "work", string(data))
	info, err := Local.Stat(filepath.Join("~", "private.txt"))
	require.NoError(t, err)
	assert.Equal(t, joined, info.File, "the File of the FileInfo of a path starting with ~")
	var subListed []File
	require.NoError(t, subFS.ListDir(ctx, "/~", nil, func(info *FileInfo) error {
		subListed = append(subListed, info.File)
		return nil
	}))
	assert.Equal(t, []File{File(subFS.Prefix() + "/~/private.txt")}, subListed)
}

func TestLocalFileSystem_URL(t *testing.T) {
	// ParseRawURI decodes the percent-encoded characters of a file:// URI,
	// but never those of a local path without the prefix.
	localPath := filepath.Join(t.TempDir(), "100%25 a%20b.txt")
	file := File(localPath)
	assert.Equal(t, localPath, file.Path(), "a local path is not decoded")
	assert.Equal(t, "100%25 a%20b.txt", file.Name())

	uri := file.URI()
	assert.True(t, strings.HasPrefix(uri, LocalPrefix), "URL %q has the local prefix", uri)
	assert.True(t, strings.HasSuffix(uri, "/100%2525 a%2520b.txt"),
		"URL %q escapes every %%", uri)
	assert.Equal(t, localPath, File(uri).Path(), "the URL resolves to the path")
	assert.Equal(t, "100%25 a%20b.txt", File(uri).Name())
}

// TestFileURILocalPath checks the RFC 8089 rules for the rules
// of both platforms, so they run on every platform.
func TestFileURILocalPath(t *testing.T) {
	for _, tc := range []struct {
		rest        string // URI after "file://"
		windows     bool
		wantPath    string
		wantIsLocal bool
	}{
		{"/etc/hosts", false, "/etc/hosts", true},
		{"localhost/etc/hosts", false, "/etc/hosts", true},
		{"LocalHost/etc/hosts", false, "/etc/hosts", true},
		{"/", false, "/", true},
		{"", false, "", true},
		{"/~/x", false, "/~/x", true},
		{"/C:/x", false, "/C:/x", true},
		{"server/share/x", false, "/server/share/x", false},
		{"C:/x", false, "/C:/x", false},
		{"~/x", false, "~/x", true},
		{"~", false, "~", true},
		{`~\x`, true, `~\x`, true},

		{"/C:/dir/file", true, "C:/dir/file", true},
		{"/c:/dir", true, "c:/dir", true},
		{"/C:", true, "C:/", true},
		{"/C:/", true, "C:/", true},
		{"localhost/C:/x", true, "C:/x", true},
		{"C:/x", true, "C:/x", true}, // written by go-fs before v1.0.0
		{"C:", true, "C:/", true},
		{`C:\x`, true, `C:\x`, true},
		{"server/share/x", true, "//server/share/x", true},
		{"//server/share/x", true, "//server/share/x", true},
		{"/x", true, "/x", true},
		{"/CD:/x", true, "/CD:/x", true},

		// No hosts: go-fs before v1.0.0 resolved "file://./x" relative to the
		// working directory, on Windows they would be device namespace paths
		{"./x", false, "/./x", false},
		{"../x", false, "/../x", false},
		{"?/x", false, "/?/x", false},
		{"./PhysicalDrive0", true, "/./PhysicalDrive0", false},
		{`.\x`, true, `/.\x`, false},
		{"../x", true, "/../x", false},
		{"?/C:/x", true, "/?/C:/x", false},
		{"//./PhysicalDrive0", true, "/./PhysicalDrive0", false},
		{`\\?\C:\x`, true, `/?\C:\x`, false},
		{"//./x", false, "//./x", true}, // only a path on other platforms
		{"//.x/share", true, "//.x/share", true},
	} {
		gotPath, gotIsLocal := fileURILocalPath(tc.rest, tc.windows)
		assert.Equal(t, tc.wantPath, gotPath, "fileURILocalPath(%q, windows=%t) path", tc.rest, tc.windows)
		assert.Equal(t, tc.wantIsLocal, gotIsLocal, "fileURILocalPath(%q, windows=%t) isLocal", tc.rest, tc.windows)
	}
}

func TestFileURIPathOf(t *testing.T) {
	for slashPath, want := range map[string]string{
		"/etc/hosts":       "/etc/hosts",
		"/":                "/",
		"C:/dir/file":      "/C:/dir/file",
		"C:/":              "/C:/",
		"//server/share/x": "server/share/x",
	} {
		assert.Equal(t, want, fileURIPathOf(slashPath, true), "fileURIPathOf(%q)", slashPath)
		rest := fileURIPathOf(slashPath, true)
		gotPath, isLocal := fileURILocalPath(rest, true)
		assert.True(t, isLocal, "the URI of %q is local on Windows", slashPath)
		assert.Equal(t, slashPath, gotPath, "the Windows path of the URI of %q", slashPath)
	}

	// A relative path, which AbsPath returns if the working directory can't
	// be determined, gets the authority "." that names no file. It used to
	// get a leading slash, the URI of another file below the root.
	for _, tc := range []struct {
		slashPath string
		windows   bool
		want      string
	}{
		{"data/x", false, "./data/x"},
		{"data/x", true, "./data/x"},
		{"../x", false, "./../x"},
		{"C:/x", false, "./C:/x"}, // a directory named "C:" on other platforms
		{"C:x", true, "./C:x"},    // relative to the working directory of drive C:
	} {
		rest := fileURIPathOf(tc.slashPath, tc.windows)
		assert.Equal(t, tc.want, rest, "fileURIPathOf(%q, windows=%t)", tc.slashPath, tc.windows)
		_, isLocal := fileURILocalPath(rest, tc.windows)
		assert.False(t, isLocal, "the URI of the relative path %q names no file", tc.slashPath)
	}

	// Regression test: a Windows long path like `\\?\C:\x` got the URI
	// "file://?/C:/x", which names no file since "?" is no host, while
	// go-fs before v1.0.0 round-tripped it. Its URI is that of the same
	// path without the prefix, which the os package adds back if needed.
	// Device paths like `\\.\COM1` still get a URI that names no file.
	for slashPath, wantPath := range map[string]string{
		"//?/C:/dir/file":           "C:/dir/file",
		"//?/c:/":                   "c:/",
		"//?/UNC/server/share/x":    "//server/share/x",
		"//?/unc/server/share/x":    "//server/share/x",
		"//?/Volume{1234}/dir/file": "",
		"//./COM1":                  "",
	} {
		rest := fileURIPathOf(slashPath, true)
		gotPath, isLocal := fileURILocalPath(rest, true)
		if wantPath == "" {
			assert.False(t, isLocal, "the URI %q of the device or volume path %q names no file", rest, slashPath)
			continue
		}
		assert.True(t, isLocal, "the URI %q of the long path %q is local", rest, slashPath)
		assert.Equal(t, wantPath, gotPath, "the Windows path of the URI %q of %q", rest, slashPath)
	}
}

// TestLocalFileSystem_SplitPath checks that SplitPath converts a file URI
// like CleanPath before it splits the path into its elements.
func TestLocalFileSystem_SplitPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b", "c")
	want := strings.Split(strings.Trim(dir, Separator), Separator)
	assert.Equal(t, want, Local.SplitPath(dir), "SplitPath of a path")
	assert.Equal(t, want, Local.SplitPath(Local.URI(dir)), "SplitPath of the file URI of the path")
	assert.Nil(t, Local.SplitPath(Separator), "SplitPath of the root")
	assert.Nil(t, Local.SplitPath(""), "SplitPath of an empty path")
}

// TestLocalFileSystem_URIWithoutWorkingDirectory checks the URI of a relative
// path while the working directory is deleted, so the path can't be made
// absolute: the URI has the authority "." and names no file. It used to be
// the URI of the absolute path with the same elements below the root.
func TestLocalFileSystem_URIWithoutWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the working directory can't be deleted on Windows")
	}
	cwd := filepath.Join(t.TempDir(), "cwd")
	require.NoError(t, os.Mkdir(cwd, 0o700))
	t.Chdir(cwd)
	require.NoError(t, os.Remove(cwd))
	if _, err := os.Getwd(); err == nil {
		t.Skip("os.Getwd works without the working directory on this platform")
	}

	uri := File(filepath.Join("data", "x")).URI()
	assert.Equal(t, LocalPrefix+"./data/x", uri)
	fileSystem, _ := ParseRawURI(uri)
	assert.Equal(t, Invalid, fileSystem, "the URI of a relative path names no file")
}

func TestLocalFileSystem_FileURI(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "a b.txt")
	uri := File(filePath).URI()
	assert.True(t, strings.HasPrefix(uri, LocalPrefix+"/"), "the URI %q has an empty authority", uri)
	assert.False(t, strings.HasPrefix(uri, LocalPrefix+"//"), "the URI %q has no empty path element", uri)
	assert.Equal(t, filePath, File(uri).LocalPath())

	withHost := LocalPrefix + "localhost" + strings.TrimPrefix(uri, LocalPrefix)
	fileSystem, gotPath := ParseRawURI(withHost)
	assert.Equal(t, Local, fileSystem, "%q is local", withHost)
	assert.Equal(t, filePath, gotPath, "the path of %q", withHost)

	remote := LocalPrefix + "server/share/x.txt"
	fileSystem, gotPath = ParseRawURI(remote)
	if filepath.Separator == '\\' {
		assert.Equal(t, Local, fileSystem, "%q is a UNC path on Windows", remote)
		assert.Equal(t, `\\server\share\x.txt`, gotPath)
		assert.Equal(t, remote, File(gotPath).URI(), "the URI of a UNC path")
	} else {
		assert.Equal(t, Invalid, fileSystem, "%q names a file on another host", remote)
		assert.Equal(t, Invalid, File(remote).FileSystem())
		assert.Empty(t, File(remote).LocalPath())
	}
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

// TestLocalFileSystem_RemoveRoot verifies that the root directory of a
// volume is never removed, like in the other file systems. RemoveAll of "/"
// called os.RemoveAll("/"), which deletes everything the process may write.
func TestLocalFileSystem_RemoveRoot(t *testing.T) {
	abs, err := filepath.Abs(t.TempDir())
	require.NoError(t, err)
	root := filepath.VolumeName(abs) + Separator
	assert.True(t, isLocalRoot(root), "isLocalRoot(%q)", root)
	assert.False(t, isLocalRoot(abs), "isLocalRoot(%q)", abs)

	assert.ErrorIs(t, Local.Remove(root), os.ErrInvalid, "Remove of the root")
	assert.ErrorIs(t, Local.RemoveAll(t.Context(), root), os.ErrInvalid, "RemoveAll of the root")
	assert.ErrorIs(t, File(root).RemoveRecursive(t.Context()), os.ErrInvalid, "File.RemoveRecursive of the root")
}
