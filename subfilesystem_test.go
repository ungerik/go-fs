package fs_test

import (
	"context"
	"os"
	"testing"

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
