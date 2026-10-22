package fs_test

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// TestSubFileSystem_Conformance runs the full suite over views
// of a local temp directory and of a MemFileSystem directory,
// so every forwarded operation is verified against both parents.
func TestSubFileSystem_Conformance(t *testing.T) {
	t.Run("Local", func(t *testing.T) {
		tempDir := fs.MustMakeTempDir()
		t.Cleanup(func() { _ = os.RemoveAll(tempDir.LocalPath()) })
		require.NoError(t, tempDir.Join("root").MakeDir())

		subFS, err := fs.NewSubFileSystem(fs.Local, tempDir.Join("root").LocalPath(), "sub-local")
		require.NoError(t, err)
		require.Equal(t, "sub://sub-local", subFS.Prefix())
		require.Equal(t, fs.FileSystem(fs.Local), subFS.Parent())

		fstest.RunConformance(t, subFS, fstest.Config{
			Prefix:  "sub://sub-local",
			TestDir: "/conformance",
		})
	})

	t.Run("Mem", func(t *testing.T) {
		memFS, err := fs.RegisteredTestMemFileSystem(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })
		require.NoError(t, memFS.MakeDir("/root", 0))

		subFS, err := fs.NewSubFileSystem(memFS, "/root", "")
		require.NoError(t, err)

		fstest.RunConformance(t, subFS, fstest.Config{
			Prefix:  subFS.Prefix(),
			TestDir: "/conformance",
		})
	})
}

// TestSubFileSystem_View verifies that the view is confined to its
// directory and that paths and errors are translated.
func TestSubFileSystem_View(t *testing.T) {
	ctx := t.Context()
	memFS, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	root := memFS.RootDir()
	require.NoError(t, root.Join("outside.txt").WriteAllString(ctx, "outside"))
	require.NoError(t, root.Join("data", "inner").MakeAllDirs())
	require.NoError(t, root.Join("data", "inner", "a.txt").WriteAllString(ctx, "a"))

	_, err = fs.NewSubFileSystem(memFS, "/data/inner/a.txt", "")
	assert.ErrorAs(t, err, new(fs.ErrIsNotDirectory), "root must be a directory")
	_, err = fs.NewSubFileSystem(memFS, "/missing", "")
	assert.ErrorIs(t, err, os.ErrNotExist, "root must exist")

	subFS, err := fs.NewSubFileSystem(memFS, "/data", "view")
	require.NoError(t, err)
	require.NoError(t, fs.Register(subFS))
	t.Cleanup(func() { assert.NoError(t, subFS.Close()) })
	assert.Equal(t, root.Join("data"), subFS.Dir())

	// Files are translated to the view
	files, err := fs.File("sub://view/").ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{"sub://view/inner"}, files)
	data, err := fs.File("sub://view/inner/a.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "a", data)

	// Errors carry view paths
	_, err = subFS.Stat("/nope.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Contains(t, err.Error(), "sub://view/nope.txt")

	// Writes go to the parent
	require.NoError(t, fs.File("sub://view/new.txt").WriteAllString(ctx, "new"))
	assert.True(t, root.Join("data", "new.txt").Exists())

	// Paths can't escape the root: ".." is cleaned away
	_, err = fs.File("sub://view/../outside.txt").Stat()
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.False(t, isSymlink(subFS, "/inner"))

	// The root directory of the view can't be removed
	assert.Error(t, subFS.Remove("/"))
	assert.Error(t, subFS.RemoveAll(ctx, "/"))

	// Closing the view leaves the parent alone
	require.NoError(t, subFS.Close())
	assert.False(t, fs.IsRegistered(subFS))
	assert.True(t, root.Join("data", "new.txt").Exists())
	_ = context.Background()
}

// newSubTestFS returns a registered SubFileSystem rooted at "/root"
// of a MemFileSystem containing the file "/root/a.txt".
func newSubTestFS(t *testing.T) *fs.SubFileSystem {
	t.Helper()
	memFS, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.NoError(t, memFS.MakeDir("/root", 0))
	require.NoError(t, memFS.WriteAll(t.Context(), "/root/a.txt", []byte("a"), 0))

	subFS, err := fs.NewSubFileSystem(memFS, "/root", "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(subFS))
	t.Cleanup(func() { _ = subFS.Close() })
	return subFS
}

// TestSubFileSystem_Closed verifies that every method of a closed view
// reports fs.ErrFileSystemClosed instead of forwarding the operation to
// the still-open parent file system.
func TestSubFileSystem_Closed(t *testing.T) {
	ctx := t.Context()
	subFS := newSubTestFS(t)
	require.NoError(t, subFS.Close())
	require.NoError(t, subFS.Close(), "Close must be idempotent")

	_, err := subFS.Stat("/a.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = subFS.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.Lstat("/")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = subFS.ListDirRecursive(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.OpenReader("/a.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.OpenWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.OpenAppendWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.OpenReadWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.Exists("/a.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.ReadAll(ctx, "/a.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.WriteAll(ctx, "/a.txt", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Append(ctx, "/a.txt", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Truncate(ctx, "/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Touch("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.MakeAllDirs("/dir/sub", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Remove("/a.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.RemoveAll(ctx, "/a.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "/a.txt", "/b.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Move(ctx, "/a.txt", "/b.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.SetPermissions("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.CreateSymbolicLink("/a.txt", "/link"), fs.ErrFileSystemClosed)
	_, err = subFS.ReadSymbolicLink("/link")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.Watch("/", func(fs.File, fs.Event) {})
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.Lstat("/a.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "Lstat of a closed view")

	// The parent is untouched by closing the view
	assert.True(t, subFS.Dir().Join("a.txt").Exists())
}

// TestSubFileSystem_EmptyPath verifies that an empty path is rejected
// with fs.ErrEmptyPath instead of being cleaned to the root directory,
// which would make a typo operate on the whole view.
func TestSubFileSystem_EmptyPath(t *testing.T) {
	ctx := t.Context()
	subFS := newSubTestFS(t)

	_, err := subFS.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	err = subFS.ListDir(ctx, "", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.Lstat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	err = subFS.ListDirRecursive(ctx, "", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.OpenReader("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.OpenWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.OpenAppendWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.OpenReadWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = subFS.ReadAll(ctx, "")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.WriteAll(ctx, "", []byte("x"), 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Append(ctx, "", []byte("x"), 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Truncate(ctx, "", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.MakeAllDirs("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.RemoveAll(ctx, ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "", "/b.txt"), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "/a.txt", ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Move(ctx, "", "/b.txt"), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Move(ctx, "/a.txt", ""), fs.ErrEmptyPath)

	// Exists reports false without an error, like fsExists does
	exists, err := subFS.Exists("")
	assert.NoError(t, err)
	assert.False(t, exists)
}

// TestSubFileSystem_UnsupportedByParent verifies that the optional
// operations a parent doesn't implement are reported as unsupported
// instead of panicking or silently doing nothing.
func TestSubFileSystem_UnsupportedByParent(t *testing.T) {
	// MockFileSystem implements only FileSystem and WriteFileSystem
	parent := &fstest.MockFileSystem{
		MockPrefix: "mock-sub://",
		MockStat: func(filePath string) (*fs.FileInfo, error) {
			return &fs.FileInfo{File: fs.File("mock-sub://" + filePath), Name: "root", Exists: true, IsDir: true}, nil
		},
	}
	fs.Register(parent)
	t.Cleanup(func() { fs.Unregister(parent) })

	subFS, err := fs.NewSubFileSystem(parent, "/root", "unsupported")
	require.NoError(t, err)

	assert.ErrorIs(t, subFS.SetPermissions("/a.txt", 0), errors.ErrUnsupported)
	assert.ErrorIs(t, subFS.CreateSymbolicLink("/a.txt", "/link"), errors.ErrUnsupported)
	_, err = subFS.ReadSymbolicLink("/link")
	assert.ErrorIs(t, err, errors.ErrUnsupported)
	_, err = subFS.Watch("/", func(fs.File, fs.Event) {})
	assert.ErrorIs(t, err, errors.ErrUnsupported)
	assert.False(t, isSymlink(subFS, "/a.txt"), "a parent without symlink support has none")
}

// TestSubFileSystem_Watch verifies that watch events of the parent are
// forwarded with the file translated to the view, so callers never see
// a parent path leak through.
func TestSubFileSystem_Watch(t *testing.T) {
	subFS := newSubTestFS(t)
	events := make(chan fs.File, 8)
	cancel, err := subFS.Watch("/a.txt", func(file fs.File, _ fs.Event) { events <- file })
	require.NoError(t, err)
	t.Cleanup(func() { _ = cancel() })

	require.NoError(t, subFS.WriteAll(t.Context(), "/a.txt", []byte("changed"), 0))

	select {
	case file := <-events:
		assert.Equal(t, fs.File(subFS.Prefix()+"/a.txt"), file, "the event file must be a view path")
	case <-time.After(2 * time.Second):
		t.Fatal("expected a watch event for /a.txt")
	}
}

// TestSubFileSystem_ReadSymbolicLinkSiblingRoot verifies that a link
// target outside the view is returned untranslated. A raw prefix check
// treated the sibling "/root-other" as being below the root "/root" and
// rewrote it to the misleading in-view path "/-other/file".
func TestSubFileSystem_ReadSymbolicLinkSiblingRoot(t *testing.T) {
	var targets = map[string]string{
		"/root/inside":  "/root/data/file", // below the root
		"/root/self":    "/root",           // the root itself
		"/root/sibling": "/root-other/file",
		"/root/outside": "/elsewhere/file",
	}
	parent := &symlinkMockFileSystem{
		MockFileSystem: fstest.MockFileSystem{
			MockPrefix: "mock-sibling://",
			MockStat: func(filePath string) (*fs.FileInfo, error) {
				return &fs.FileInfo{File: fs.File("mock-sibling://" + filePath), Name: "root", Exists: true, IsDir: true}, nil
			},
		},
		readLink: func(linkPath string) (string, error) {
			return targets[linkPath], nil
		},
	}
	fs.Register(parent)
	t.Cleanup(func() { fs.Unregister(parent) })

	subFS, err := fs.NewSubFileSystem(parent, "/root", "sibling")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, subFS.Close()) })

	target, err := subFS.ReadSymbolicLink("/inside")
	require.NoError(t, err)
	assert.Equal(t, "/data/file", target, "a target below the root is translated to the view")

	target, err = subFS.ReadSymbolicLink("/self")
	require.NoError(t, err)
	assert.Equal(t, "/", target, "the root itself is the view root")

	target, err = subFS.ReadSymbolicLink("/sibling")
	require.NoError(t, err)
	assert.Equal(t, "/root-other/file", target, "a sibling of the root is outside and stays untranslated")

	target, err = subFS.ReadSymbolicLink("/outside")
	require.NoError(t, err)
	assert.Equal(t, "/elsewhere/file", target, "an unrelated target stays untranslated")
}

// TestSubFileSystem_ParentSeparator verifies that a name containing the
// separator of the parent can't escape the root directory. The view used
// to pass `/..\secret.txt` to a parent with the separator `\`, which split
// the name into ".." and "secret.txt" and read and wrote outside of the root.
func TestSubFileSystem_ParentSeparator(t *testing.T) {
	ctx := t.Context()
	memFS, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`})
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.NoError(t, memFS.MakeDir(`\root`, 0))
	require.NoError(t, memFS.WriteAll(ctx, `\secret.txt`, []byte("secret"), 0))

	subFS, err := fs.NewSubFileSystem(memFS, `\root`, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(subFS))
	t.Cleanup(func() { _ = subFS.Close() })
	root := subFS.RootDir()

	// Reading outside of the root
	secret := root.Join(`..\secret.txt`)
	_, err = secret.ReadAll(ctx)
	assert.ErrorIs(t, err, os.ErrInvalid, "File.ReadAll of a name with the parent's separator")
	_, err = subFS.Stat(`/..\secret.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "Stat of a name with the parent's separator")
	_, err = subFS.OpenReader(`/..\secret.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "OpenReader of a name with the parent's separator")
	assert.False(t, secret.Exists(), "a name with the parent's separator does not exist")
	exists, err := subFS.Exists(`/..\secret.txt`)
	assert.NoError(t, err)
	assert.False(t, exists)

	// Writing outside of the root
	err = root.Join(`..\evil.txt`).WriteAllString(ctx, "evil")
	assert.ErrorIs(t, err, os.ErrInvalid, "File.WriteAll of a name with the parent's separator")
	assert.ErrorIs(t, subFS.MakeDir(`/a\..\..\evil`, 0), os.ErrInvalid)
	assert.ErrorIs(t, subFS.Remove(`/..\secret.txt`), os.ErrInvalid)
	assert.ErrorIs(t, subFS.CopyFile(ctx, `/..\secret.txt`, "/copy.txt"), os.ErrInvalid)
	assert.ErrorIs(t, subFS.Move(ctx, `/..\secret.txt`, "/moved.txt"), os.ErrInvalid)
	files, err := memFS.RootDir().ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.ElementsMatch(t, []fs.File{memFS.RootDir().Join("root"), memFS.RootDir().Join("secret.txt")}, files,
		"nothing written outside of the root")
	data, err := memFS.ReadAll(ctx, `\secret.txt`)
	require.NoError(t, err)
	assert.Equal(t, "secret", string(data), "the file outside of the root is untouched")

	// Paths with the separator of the view are translated element by element
	require.NoError(t, root.Join("dir").MakeDir())
	require.NoError(t, root.Join("dir", "a.txt").WriteAllString(ctx, "a"))
	data, err = memFS.ReadAll(ctx, `\root\dir\a.txt`)
	require.NoError(t, err)
	assert.Equal(t, "a", string(data))
}

// TestSubFileSystem_LocalInvalidNames verifies that a view of a local
// directory rejects the names that filepath.Localize rejects with an error
// wrapping os.ErrInvalid, like os.DirFS does. The view passed reserved
// device names like "NUL" or "COM1" to the parent, for which Windows
// opens the device instead of a file in the directory, and names with
// a colon, which Windows takes for an alternate data stream of the file
// named before the colon. A name with a NUL byte is invalid everywhere.
func TestSubFileSystem_LocalInvalidNames(t *testing.T) {
	ctx := t.Context()
	subFS, err := fs.NewSubFileSystem(fs.Local, t.TempDir(), "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(subFS))
	t.Cleanup(func() { _ = subFS.Close() })
	require.NoError(t, subFS.MakeDir("/dir", 0))

	invalid := []string{"a\x00b"}
	windowsInvalid := []string{"NUL", "con", "COM1", "CONIN$", "file.txt:stream"}
	if runtime.GOOS == "windows" {
		invalid = append(invalid, windowsInvalid...)
	}
	for _, name := range invalid {
		for _, filePath := range []string{"/" + name, "/dir/" + name, "/" + name + "/file.txt"} {
			_, err := subFS.Stat(filePath)
			assert.ErrorIs(t, err, os.ErrInvalid, "Stat(%+q)", filePath)
			_, err = subFS.OpenReader(filePath)
			assert.ErrorIs(t, err, os.ErrInvalid, "OpenReader(%+q)", filePath)
			err = subFS.WriteAll(ctx, filePath, []byte("data"), 0)
			assert.ErrorIs(t, err, os.ErrInvalid, "WriteAll(%+q)", filePath)
			assert.ErrorIs(t, subFS.MakeDir(filePath, 0), os.ErrInvalid, "MakeDir(%+q)", filePath)
			exists, err := subFS.Exists(filePath)
			assert.NoError(t, err, "Exists(%+q)", filePath)
			assert.False(t, exists, "a name that the parent can't represent does not exist: %+q", filePath)
		}
		// A relative link target is checked element by element too
		err := subFS.CreateSymbolicLink(name, "/link")
		assert.ErrorIs(t, err, os.ErrInvalid, "CreateSymbolicLink(%+q)", name)
	}

	if runtime.GOOS != "windows" {
		// The Windows names are files on other platforms
		for _, name := range windowsInvalid {
			require.NoError(t, subFS.WriteAll(ctx, "/dir/"+name, []byte(name), 0), "WriteAll(%q)", name)
			data, err := subFS.ReadAll(ctx, "/dir/"+name)
			require.NoError(t, err, "ReadAll(%q)", name)
			assert.Equal(t, name, string(data))
		}
	}
	files, err := subFS.Dir().ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{subFS.Dir().Join("dir")}, files, "nothing else written to the directory")
}

// TestSubFileSystem_MoveRoot verifies that the root directory of the view
// can't be moved or renamed, which would move the directory in the parent,
// and that it can't be replaced as an existing directory.
func TestSubFileSystem_MoveRoot(t *testing.T) {
	ctx := t.Context()
	subFS := newSubTestFS(t)
	rootDir := subFS.Dir()

	assert.ErrorIs(t, subFS.Move(ctx, "/", "/moved"), os.ErrInvalid, "Move of the root")
	_, err := subFS.RootDir().Rename(ctx, "renamed")
	assert.Error(t, err, "File.Rename of the root")
	assert.ErrorIs(t, subFS.Move(ctx, "/a.txt", "/"), os.ErrExist, "the root is an existing directory")
	require.NoError(t, subFS.Move(ctx, "/", "/"), "Move onto itself is a no-op")

	assert.True(t, rootDir.IsDir(), "the root directory must stay in the parent")
	assert.True(t, rootDir.Join("a.txt").Exists(), "the file must stay in the root directory")
	assert.False(t, rootDir.Dir().Join("renamed").Exists(), "nothing renamed in the parent")
}

// TestSubFileSystem_CopyFileOntoDirectory verifies that the destination of
// CopyFile is the final path, an existing directory is an fs.ErrIsDirectory.
// The view used fs.CopyFile, which copied into the directory instead.
func TestSubFileSystem_CopyFileOntoDirectory(t *testing.T) {
	ctx := t.Context()
	subFS := newSubTestFS(t)
	require.NoError(t, subFS.MakeDir("/dir", 0))

	err := subFS.CopyFile(ctx, "/a.txt", "/dir")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "CopyFile onto a directory")
	assert.Contains(t, err.Error(), subFS.Prefix(), "the error names the file of the view")
	exists, err := subFS.Exists("/dir/a.txt")
	require.NoError(t, err)
	assert.False(t, exists, "nothing copied into the directory")

	// An existing file is replaced, a missing one created
	require.NoError(t, subFS.WriteAll(ctx, "/b.txt", []byte("old"), 0))
	require.NoError(t, subFS.CopyFile(ctx, "/a.txt", "/b.txt"))
	data, err := subFS.ReadAll(ctx, "/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "a", string(data))
	require.NoError(t, subFS.CopyFile(ctx, "/a.txt", "/c.txt"))
	data, err = subFS.ReadAll(ctx, "/c.txt")
	require.NoError(t, err)
	assert.Equal(t, "a", string(data))
	require.NoError(t, subFS.CopyFile(ctx, "/a.txt", "/a.txt"), "CopyFile onto itself is a no-op")
}

// TestSubFileSystem_CreateSymbolicLink verifies the link targets stored in
// the parent: a relative target without ".." is stored verbatim, it used to be
// turned into an absolute path of the parent resolved against the root instead
// of the directory of the link, which also allowed targets outside of the root.
func TestSubFileSystem_CreateSymbolicLink(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "root")
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, "d"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "d", "target.txt"), []byte("target"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o600))
	subFS, err := fs.NewSubFileSystem(fs.Local, rootDir, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = subFS.Close() })

	err = subFS.CreateSymbolicLink("target.txt", "/d/link")
	if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("symbolic links not available: %v", err)
	}
	require.NoError(t, err)
	stored, err := os.Readlink(filepath.Join(rootDir, "d", "link"))
	require.NoError(t, err)
	assert.Equal(t, "target.txt", stored, "a relative target is stored verbatim")
	target, err := subFS.ReadSymbolicLink("/d/link")
	require.NoError(t, err)
	assert.Equal(t, "target.txt", target)
	data, err := subFS.ReadAll(ctx, "/d/link")
	require.NoError(t, err)
	assert.Equal(t, "target", string(data), "reading through the relative link")

	// A relative target may go up as long as it stays in the view,
	// it is stored as the parent path it resolves to in the view
	require.NoError(t, subFS.CreateSymbolicLink("../d/./target.txt", "/d/up"))
	stored, err = os.Readlink(filepath.Join(rootDir, "d", "up"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(rootDir, "d", "target.txt"), stored)
	data, err = subFS.ReadAll(ctx, "/d/up")
	require.NoError(t, err)
	assert.Equal(t, "target", string(data))

	// An absolute target is a path of the view
	require.NoError(t, subFS.CreateSymbolicLink("/d/target.txt", "/abs"))
	stored, err = os.Readlink(filepath.Join(rootDir, "abs"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(rootDir, "d", "target.txt"), stored, "stored as path of the parent")
	target, err = subFS.ReadSymbolicLink("/abs")
	require.NoError(t, err)
	assert.Equal(t, "/d/target.txt", target, "read as path of the view")

	// A relative target must not leave the view
	assert.ErrorIs(t, subFS.CreateSymbolicLink("../../secret.txt", "/d/escape"), os.ErrInvalid)
	assert.ErrorIs(t, subFS.CreateSymbolicLink("../secret.txt", "/escape"), os.ErrInvalid)
	assert.ErrorIs(t, subFS.CreateSymbolicLink("d/../../secret.txt", "/escape"), os.ErrInvalid)
	for _, link := range []string{filepath.Join(rootDir, "d", "escape"), filepath.Join(rootDir, "escape")} {
		_, err = os.Lstat(link)
		assert.ErrorIs(t, err, os.ErrNotExist, "no link created at %s", link)
	}
	assert.ErrorIs(t, subFS.CreateSymbolicLink("", "/empty"), fs.ErrEmptyPath)
}

// TestSubFileSystem_CreateSymbolicLinkChain is a regression test:
// a relative target with ".." was checked only lexically against the path
// of the link in the view and then stored verbatim, but the parent resolves
// ".." from the real directory of the link, which an earlier link can move.
// The link "/d/up" to ".." made the directory "/d/up/d/up" of the view
// the root directory itself, so "../../../secret.txt" reached a file
// three directories above the root.
func TestSubFileSystem_CreateSymbolicLinkChain(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	rootDir := filepath.Join(dir, "a", "b", "root")
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, "d"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "d", "secret.txt"), []byte("inside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o600))
	subFS, err := fs.NewSubFileSystem(fs.Local, rootDir, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = subFS.Close() })

	err = subFS.CreateSymbolicLink("..", "/d/up")
	if errors.Is(err, os.ErrPermission) || errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("symbolic links not available: %v", err)
	}
	require.NoError(t, err)
	stored, err := os.Readlink(filepath.Join(rootDir, "d", "up"))
	require.NoError(t, err)
	assert.Equal(t, rootDir, stored, `a target with ".." is stored as the parent path it resolves to in the view`)

	// The view resolves "/d/up/d/up" + "../../../secret.txt" to "/d/secret.txt"
	require.NoError(t, subFS.CreateSymbolicLink("../../../secret.txt", "/d/up/d/up/esc"))
	data, err := subFS.ReadAll(ctx, "/esc")
	require.NoError(t, err)
	assert.Equal(t, "inside", string(data), "the link chain must not leave the root directory")
	target, err := subFS.ReadSymbolicLink("/esc")
	require.NoError(t, err)
	assert.Equal(t, "/d/secret.txt", target, "read as path of the view")
}

// symlinkMockFileSystem is a MockFileSystem that also implements
// fs.SymbolicLinkFileSystem, which MockFileSystem alone does not.
type symlinkMockFileSystem struct {
	fstest.MockFileSystem
	readLink func(linkPath string) (targetPath string, err error)
}

func (m *symlinkMockFileSystem) Lstat(filePath string) (*fs.FileInfo, error) {
	target, err := m.readLink(filePath)
	if err != nil {
		return nil, err
	}
	return &fs.FileInfo{Name: path.Base(filePath), Exists: true, IsSymlink: target != ""}, nil
}

// isSymlink reports if filePath is a symbolic link in fileSystem.
func isSymlink(fileSystem fs.SymbolicLinkFileSystem, filePath string) bool {
	info, err := fileSystem.Lstat(filePath)
	return err == nil && info.IsSymlink
}

func (m *symlinkMockFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	return errors.ErrUnsupported
}

func (m *symlinkMockFileSystem) ReadSymbolicLink(linkPath string) (string, error) {
	return m.readLink(linkPath)
}
