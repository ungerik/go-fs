package fs

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStdFS(t *testing.T) {
	// Round trip: a MapFS through StdFileSystem and back through StdFS
	// must still pass the io/fs test suite.
	t.Run("MapFS", func(t *testing.T) {
		fixture := fstest.MapFS{
			"a.txt":       {Data: []byte("a")},
			"sub/b.txt":   {Data: []byte("b")},
			"sub/.hidden": {Data: []byte("h")},
		}
		stdFS := NewStdFileSystem(fixture, "")
		require.NoError(t, Register(stdFS))
		t.Cleanup(func() { _ = stdFS.Close() })
		require.NoError(t, fstest.TestFS(stdFS.RootDir().StdFS(), "a.txt", "sub/b.txt", "sub/.hidden"))
	})

	// A controlled tree instead of the repository checkout, which
	// makes the walk deterministic and fast on every platform.
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive(context.Background()) })
	require.NoError(t, dir.Join("sub").MakeDir())
	files := []string{"a.txt", "b.txt", "sub/c.txt", "sub/.hidden"}
	for _, name := range files {
		require.NoError(t, dir.Join(name).WriteAllString(t.Context(), name))
	}
	// NTFS updates the modification time cached in the parent's directory
	// entry lazily, so a directory listing can report an older ModTime for
	// "sub" than Stat until a handle to the directory is written and closed.
	now := time.Now()
	require.NoError(t, os.Chtimes(dir.Join("sub").LocalPath(), now, now))

	err := fstest.TestFS(dir.StdFS(), files...)
	if err != nil {
		t.Fatal(err)
	}
}

// TestStdFS_TestFS runs the io/fs test suite over trees of the
// memory and the local file system, including hidden files and,
// where the platform allows it, symbolic links.
func TestStdFS_TestFS(t *testing.T) {
	t.Run("Mem", func(t *testing.T) {
		ctx := t.Context()
		memFS, err := RegisteredTestMemFileSystem(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })
		require.NoError(t, memFS.MakeAllDirs("/a/b", 0))
		require.NoError(t, memFS.WriteAll(ctx, "/a/b/c.txt", []byte("c"), 0))
		require.NoError(t, memFS.WriteAll(ctx, "/x.txt", []byte("x"), 0))
		require.NoError(t, memFS.WriteAll(ctx, "/.hidden", []byte("h"), 0))
		require.NoError(t, fstest.TestFS(memFS.RootDir().StdFS(), "a/b/c.txt", "x.txt", ".hidden"))
	})

	t.Run("Local", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "c.txt"), []byte("c"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden"), []byte("h"), 0o600))
		expected := []string{"a/b/c.txt", "x.txt", ".hidden"}
		if os.Symlink("a", filepath.Join(dir, "dirlink")) == nil && os.Symlink("x.txt", filepath.Join(dir, "filelink")) == nil {
			expected = append(expected, "dirlink", "filelink")
		}
		if runtime.GOOS == "windows" {
			syncWindowsModTimes(t, dir)
		}
		require.NoError(t, fstest.TestFS(File(dir).StdFS(), expected...))
	})
}

// TestStdFS_InvalidNames verifies that no name can address a file
// outside of the File of the StdFS. Stat and Sub skipped the validation
// that Open does, so Stat("../secret.txt") and Sub("..") escaped,
// and a name containing the separator `\` of the file system,
// like `..\secret.txt`, was split by it into ".." and "secret.txt".
func TestStdFS_InvalidNames(t *testing.T) {
	ctx := t.Context()
	for _, sep := range []string{`/`, `\`} {
		t.Run("separator "+sep, func(t *testing.T) {
			memFS, err := RegisteredTestMemFileSystem(&MemFileSystemOptions{Separator: sep})
			require.NoError(t, err)
			t.Cleanup(func() { _ = memFS.Close() })
			require.NoError(t, memFS.MakeAllDirs(sep+"root"+sep+"sub", 0))
			require.NoError(t, memFS.WriteAll(ctx, sep+"root"+sep+"a.txt", []byte("a"), 0))
			require.NoError(t, memFS.WriteAll(ctx, sep+"secret.txt", []byte("secret"), 0))
			stdFS := memFS.RootDir().Join("root").StdFS()

			invalid := []string{"", "/", "..", "../secret.txt", "sub/../../secret.txt", "/secret.txt", "a.txt/"}
			if sep == `\` {
				invalid = append(invalid, `..\secret.txt`, `sub\..\..\secret.txt`)
			}
			for _, name := range invalid {
				_, err := stdFS.Stat(name)
				assert.ErrorIs(t, err, iofs.ErrInvalid, "Stat(%q)", name)
				_, err = stdFS.Sub(name)
				assert.ErrorIs(t, err, iofs.ErrInvalid, "Sub(%q)", name)
				_, err = stdFS.Open(name)
				assert.ErrorIs(t, err, iofs.ErrInvalid, "Open(%q)", name)
				_, err = stdFS.ReadFile(name)
				assert.ErrorIs(t, err, iofs.ErrInvalid, "ReadFile(%q)", name)
				_, err = stdFS.ReadDir(name)
				assert.ErrorIs(t, err, iofs.ErrInvalid, "ReadDir(%q)", name)
				var pathErr *iofs.PathError
				assert.ErrorAs(t, err, &pathErr, "ReadDir(%q)", name)
			}

			// Valid names work
			info, err := stdFS.Stat("a.txt")
			require.NoError(t, err)
			assert.Equal(t, "a.txt", info.Name())
			sub, err := stdFS.Sub("sub")
			require.NoError(t, err)
			_, err = iofs.Stat(sub, ".")
			require.NoError(t, err)
			_, err = iofs.ReadFile(sub, "../a.txt")
			assert.ErrorIs(t, err, iofs.ErrInvalid, "the sub tree can't be left either")
		})
	}
}

// TestStdFS_LocalNames is a regression test: the Windows rules for names,
// no backslash and no colon, were applied to every file system, so the key
// "10:00.json" of an S3 bucket couldn't be opened on Windows. Local didn't
// reject the names that os.DirFS rejects: reserved device names like "NUL"
// on Windows, which open the device instead of a file in the directory,
// and names with a NUL byte on every platform.
func TestStdFS_LocalNames(t *testing.T) {
	ctx := t.Context()
	windowsNames := []string{"10:00.json", `a\b.json`, "NUL", "CON", "COM1", "LPT1", "sub/AUX"}

	// Other file systems accept the names that Windows rejects
	memFS, err := RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.NoError(t, memFS.MakeDir("/sub", 0))
	memStdFS := memFS.RootDir().StdFS()
	for _, name := range windowsNames {
		require.NoError(t, memFS.WriteAll(ctx, "/"+name, []byte(name), 0))
		data, err := iofs.ReadFile(memStdFS, name)
		require.NoError(t, err, "ReadFile(%q)", name)
		assert.Equal(t, name, string(data))
	}

	// Local rejects the names that os.DirFS rejects on the platform
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
	localStdFS := File(dir).StdFS()
	invalid := []string{"a\x00b", "sub/a\x00b"}
	if runtime.GOOS == "windows" {
		invalid = append(invalid, windowsNames...)
	}
	for _, name := range invalid {
		_, err := iofs.ReadFile(os.DirFS(dir), name)
		require.ErrorIs(t, err, iofs.ErrInvalid, "os.DirFS ReadFile(%q)", name)

		_, err = localStdFS.Stat(name)
		assert.ErrorIs(t, err, iofs.ErrInvalid, "Stat(%q)", name)
		_, err = localStdFS.Sub(name)
		assert.ErrorIs(t, err, iofs.ErrInvalid, "Sub(%q)", name)
		_, err = localStdFS.Open(name)
		assert.ErrorIs(t, err, iofs.ErrInvalid, "Open(%q)", name)
		_, err = localStdFS.ReadFile(name)
		assert.ErrorIs(t, err, iofs.ErrInvalid, "ReadFile(%q)", name)
		_, err = localStdFS.ReadDir(name)
		assert.ErrorIs(t, err, iofs.ErrInvalid, "ReadDir(%q)", name)
	}
	if runtime.GOOS != "windows" {
		// The names that Windows rejects are files in the directory elsewhere
		for _, name := range windowsNames {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
			data, err := iofs.ReadFile(localStdFS, name)
			require.NoError(t, err, "ReadFile(%q)", name)
			assert.Equal(t, name, string(data))
		}
	}
}

// statCountingFS is a MemFileSystem that counts the calls of Stat and Lstat.
type statCountingFS struct {
	*MemFileSystem
	stats atomic.Int32
}

func (c *statCountingFS) Stat(filePath string) (*FileInfo, error) {
	c.stats.Add(1)
	return c.MemFileSystem.Stat(filePath)
}

func (c *statCountingFS) Lstat(filePath string) (*FileInfo, error) {
	c.stats.Add(1)
	return c.MemFileSystem.Lstat(filePath)
}

// TestStdFS_ReadDirWithoutStat verifies that the entries of ReadDir
// answer from the FileInfo of the directory listing. They used to call
// Lstat in each of IsDir, Type and Info, three times per entry.
func TestStdFS_ReadDirWithoutStat(t *testing.T) {
	ctx := t.Context()
	memFS, err := NewMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.NoError(t, memFS.MakeDir("/dir", 0))
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, memFS.WriteAll(ctx, "/"+name, []byte(name), 0))
	}
	counting := &statCountingFS{MemFileSystem: memFS}
	require.NoError(t, Register(counting))
	t.Cleanup(func() { Unregister(counting) })

	for _, readDir := range map[string]func() ([]iofs.DirEntry, error){
		"StdFS.ReadDir": func() ([]iofs.DirEntry, error) {
			return iofs.ReadDir(counting.RootDir().StdFS(), ".")
		},
		"ReadDirFile.ReadDir": func() ([]iofs.DirEntry, error) {
			dir, err := counting.RootDir().StdFS().Open(".")
			if err != nil {
				return nil, err
			}
			defer dir.Close()
			return dir.(iofs.ReadDirFile).ReadDir(-1)
		},
	} {
		counting.stats.Store(0)
		entries, err := readDir()
		require.NoError(t, err)
		require.Len(t, entries, 4)
		listingStats := counting.stats.Load()
		for _, entry := range entries {
			info, err := entry.Info()
			require.NoError(t, err)
			assert.Equal(t, entry.Name(), info.Name())
			assert.Equal(t, entry.Name() == "dir", entry.IsDir(), "IsDir of %s", entry.Name())
			assert.Equal(t, entry.IsDir(), entry.Type().IsDir(), "Type of %s", entry.Name())
		}
		assert.Equal(t, listingStats, counting.stats.Load(), "the entries must not call Stat or Lstat")
	}
}

// TestStdFS_ReadDirSymlink verifies that ReadDir describes symbolic links
// themselves like os.ReadDir, so io/fs.WalkDir doesn't follow them.
func TestStdFS_ReadDirSymlink(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "target"), 0o700))
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Skipf("symbolic links not available: %v", err)
	}

	entries, err := iofs.ReadDir(File(dir).StdFS(), ".")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	link := entries[0]
	require.Equal(t, "link", link.Name())
	assert.False(t, link.IsDir(), "a link to a directory is no directory entry")
	assert.Equal(t, iofs.ModeSymlink, link.Type())
	info, err := link.Info()
	require.NoError(t, err)
	assert.Equal(t, iofs.ModeSymlink, info.Mode().Type(), "Info describes the link itself")
	assert.False(t, info.IsDir())
	assert.True(t, entries[1].IsDir(), "the target directory")
}

// TestStdFSDirFileClose verifies that Close makes a directory opened
// through StdFS unusable, like os.File.Close. The no-op Close left
// ReadDir and Stat fully working after the file was closed.
func TestStdFSDirFileClose(t *testing.T) {
	mapFS := fstest.MapFS{
		"dir/a.txt": &fstest.MapFile{Data: []byte("a")},
		"dir/b.txt": &fstest.MapFile{Data: []byte("b")},
	}
	stdFS := NewStdFileSystem(mapFS, "")
	require.NoError(t, Register(stdFS))
	t.Cleanup(func() { _ = stdFS.Close() })

	dir, err := stdFS.RootDir().StdFS().Open("dir")
	require.NoError(t, err)

	entries, err := dir.(interface {
		ReadDir(int) ([]os.DirEntry, error)
	}).ReadDir(1)
	require.NoError(t, err, "reading before Close works")
	require.Len(t, entries, 1)

	require.NoError(t, dir.Close())
	require.NoError(t, dir.Close(), "Close is idempotent")

	_, err = dir.(interface {
		ReadDir(int) ([]os.DirEntry, error)
	}).ReadDir(1)
	require.ErrorIs(t, err, os.ErrClosed, "ReadDir after Close must fail")

	_, err = dir.Stat()
	require.ErrorIs(t, err, os.ErrClosed, "Stat after Close must fail")
}

// syncWindowsModTimes sets the modification time of every entry below dir
// to the one of its Stat, like the os.DirFS test of the standard library.
// NTFS updates the copy of a directory's metadata in the listing of its
// parent lazily, so a directory listing can report an older time than
// Stat, which testing/fstest.TestFS reports as a mismatch.
func syncWindowsModTimes(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stat, err := os.Stat(path)
		if err != nil || stat.ModTime().Equal(info.ModTime()) {
			return nil // a dangling link, or nothing to sync
		}
		return os.Chtimes(path, stat.ModTime(), stat.ModTime())
	})
	require.NoError(t, err)
}
