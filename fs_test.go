package fs_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

func TestMove_CrossFileSystemKeepsSourceWhenUploadFails(t *testing.T) {
	m := newMinimalMemFS(t)
	m.closeErr = errors.New("upload failed")

	src := localFile(t, "precious.txt", "precious")
	err := fs.Move(t.Context(), src, m.RootDir().Join("precious.txt"))
	require.ErrorIs(t, err, m.closeErr, "Move must report the failed upload")
	requireContent(t, src, "precious")

	srcDir := src.Dir().Join("dir")
	writeFiles(t, srcDir, map[string]string{"a.txt": "a", "sub/b.txt": "b"})
	err = srcDir.MoveTo(t.Context(), m.RootDir().Join("dir"))
	require.ErrorIs(t, err, m.closeErr, "MoveTo of a directory must report the failed upload")
	requireContent(t, srcDir.Join("a.txt"), "a")
	requireContent(t, srcDir.Join("sub", "b.txt"), "b")
}

func TestMove_CrossFileSystemAbortsOnCanceledContext(t *testing.T) {
	src := newMinimalMemFS(t, fs.NewMemFile("/src.txt", []byte("new")))
	dest := newMinimalMemFS(t, fs.NewMemFile("/dest.txt", []byte("old")))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src.onRead = cancel
	dest.uploads = true

	err := fs.Move(ctx, src.RootDir().Join("src.txt"), dest.RootDir().Join("dest.txt"))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), dest.aborts.Load(), "the writer must be aborted")
	src.onRead = nil
	requireContent(t, src.RootDir().Join("src.txt"), "new")
	requireContent(t, dest.RootDir().Join("dest.txt"), "old")
}

func TestMove_CrossFileSystemOverwriteSemantics(t *testing.T) {
	setup := func(t *testing.T) (src, dest fs.File) {
		src, dest = newMinimalMemFS(t).RootDir(), newMinimalMemFS(t).RootDir()
		writeFiles(t, src, map[string]string{"file.txt": "new", "dir/a.txt": "a"})
		writeFiles(t, dest, map[string]string{"file.txt": "old", "into/dir/b.txt": "b"})
		return src, dest
	}

	t.Run("file replaces existing file", func(t *testing.T) {
		src, dest := setup(t)
		require.NoError(t, fs.Move(t.Context(), src.Join("file.txt"), dest.Join("file.txt")))
		requireContent(t, dest.Join("file.txt"), "new")
		require.False(t, src.Join("file.txt").Exists())
	})
	t.Run("directory onto existing file", func(t *testing.T) {
		src, dest := setup(t)
		err := fs.Move(t.Context(), src.Join("dir"), dest.Join("file.txt"))
		require.ErrorIs(t, err, fs.ErrIsNotDirectory{})
		requireContent(t, dest.Join("file.txt"), "old")
		requireContent(t, src.Join("dir", "a.txt"), "a")
	})
	t.Run("directory into directory with a directory of the same name", func(t *testing.T) {
		src, dest := setup(t)
		err := fs.Move(t.Context(), src.Join("dir"), dest.Join("into"))
		require.ErrorIs(t, err, os.ErrExist)
		require.False(t, dest.Join("into", "dir", "a.txt").Exists(), "directories must not be merged")
		requireContent(t, dest.Join("into", "dir", "b.txt"), "b")
		requireContent(t, src.Join("dir", "a.txt"), "a")
	})
	t.Run("directory into existing directory", func(t *testing.T) {
		src, dest := setup(t)
		require.NoError(t, fs.Move(t.Context(), src.Join("dir"), dest))
		requireContent(t, dest.Join("dir", "a.txt"), "a")
		require.False(t, src.Join("dir").Exists())
	})
}

func TestMove_CrossFileSystemSymlinks(t *testing.T) {
	// The source file system has no RemoveAll,
	// so the emulation removes the source.
	m := newMinimalLocalFS(t)
	tree, outside := symlinkTree(t, fs.File(m.URL(t.TempDir())), os.Symlink)
	dest := newMinimalMemFS(t).RootDir().Join("moved")

	require.NoError(t, tree.MoveTo(t.Context(), dest))
	require.False(t, tree.Exists(), "the source must be removed")
	requireContent(t, dest.Join("file.txt"), "file")
	requireContent(t, dest.Join("link", "precious.txt"), "precious")
	requireContent(t, outside.Join("precious.txt"), "precious")
}

func TestRemoveFiles(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/a.tmp", []byte("a")),
		fs.NewMemFile("/b.tmp", []byte("b")),
		fs.NewMemFile("/c.txt", []byte("c")),
	)
	root := m.RootDir()

	require.NoError(t, fs.RemoveFiles(root.Join("a.tmp"), root.Join("missing.tmp")), "missing files are skipped")
	require.False(t, root.Join("a.tmp").Exists())

	// File.Remove as method expression, like in the RemoveFiles doc
	require.NoError(t, root.ListDir(t.Context(), fs.File.Remove, "*.tmp"))
	require.False(t, root.Join("b.tmp").Exists())
	requireContent(t, root.Join("c.txt"), "c")
}
