package fs_test

import (
	"context"
	"errors"
	"os"
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
		memFS, err := fs.NewMemFileSystem("/")
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
	memFS, err := fs.NewMemFileSystem("/")
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

	subFS, err := fs.NewSubFileSystemAndRegister(memFS, "/data", "view")
	require.NoError(t, err)
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
	assert.False(t, subFS.IsSymbolicLink("/inner"))

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
	memFS, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	require.NoError(t, memFS.MakeDir("/root", 0))
	require.NoError(t, memFS.WriteAll(t.Context(), "/root/a.txt", []byte("a"), 0))

	subFS, err := fs.NewSubFileSystemAndRegister(memFS, "/root", "")
	require.NoError(t, err)
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
	_, err = subFS.ListDirMax(ctx, "/", -1, nil)
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
	assert.ErrorIs(t, subFS.Truncate("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Touch("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.MakeAllDirs("/dir/sub", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Remove("/a.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.RemoveAll(ctx, "/a.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "/a.txt", "/b.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.Move("/a.txt", "/b.txt"), fs.ErrFileSystemClosed)
	_, err = subFS.Rename("/a.txt", "b.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.SetPermissions("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, subFS.CreateSymbolicLink("/a.txt", "/link"), fs.ErrFileSystemClosed)
	_, err = subFS.ReadSymbolicLink("/link")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = subFS.Watch("/", func(fs.File, fs.Event) {})
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.False(t, subFS.IsSymbolicLink("/a.txt"), "a closed view has no symbolic links")

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
	_, err = subFS.ListDirMax(ctx, "", -1, nil)
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
	assert.ErrorIs(t, subFS.Truncate("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.MakeAllDirs("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.RemoveAll(ctx, ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "", "/b.txt"), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.CopyFile(ctx, "/a.txt", ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Move("", "/b.txt"), fs.ErrEmptyPath)
	assert.ErrorIs(t, subFS.Move("/a.txt", ""), fs.ErrEmptyPath)
	_, err = subFS.Rename("", "b.txt")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)

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
	assert.False(t, subFS.IsSymbolicLink("/a.txt"), "a parent without symlink support has none")
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

// symlinkMockFileSystem is a MockFileSystem that also implements
// fs.SymbolicLinkFileSystem, which MockFileSystem alone does not.
type symlinkMockFileSystem struct {
	fstest.MockFileSystem
	readLink func(linkPath string) (targetPath string, err error)
}

func (m *symlinkMockFileSystem) IsSymbolicLink(filePath string) bool {
	target, err := m.readLink(filePath)
	return err == nil && target != ""
}

func (m *symlinkMockFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	return errors.ErrUnsupported
}

func (m *symlinkMockFileSystem) ReadSymbolicLink(linkPath string) (string, error) {
	return m.readLink(linkPath)
}
