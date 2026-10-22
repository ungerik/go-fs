package fs_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

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

// TestMove_CrossFileSystemSymlinks checks the move of a directory with a
// symbolic link to a directory to another file system: the link is copied
// as link if the destination can create links and its target is relative
// and stays inside the moved directory, else the move fails before anything
// is copied. It used to copy the directory that the link points to.
func TestMove_CrossFileSystemSymlinks(t *testing.T) {
	t.Run("destination without symbolic links", func(t *testing.T) {
		// The source file system has no RemoveAll,
		// so the emulation would remove the source.
		m := newMinimalLocalFS(t)
		tree, outside := symlinkTree(t, fs.File(m.URL(t.TempDir())), os.Symlink)
		dest := newMinimalMemFS(t).RootDir().Join("moved")

		err := tree.MoveTo(t.Context(), dest)
		require.ErrorIs(t, err, errors.ErrUnsupported)
		requireMissing(t, dest)
		requireContent(t, tree.Join("file.txt"), "file")
		require.True(t, tree.Join("link").IsSymbolicLink(), "the link must be kept")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("absolute target", func(t *testing.T) {
		// The absolute local path means another directory
		// in the destination, the link must not be copied
		tree, outside := symlinkTree(t, fs.File(t.TempDir()), os.Symlink)
		dest := newLinkMemFS(t).RootDir().Join("moved")

		err := tree.MoveTo(t.Context(), dest)
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, tree.Join("link").String(), "the error must name the link")
		requireMissing(t, dest)
		requireContent(t, tree.Join("file.txt"), "file")
		requireLink(t, tree.Join("link"), outside.Path())
	})
	t.Run("relative target inside the moved directory", func(t *testing.T) {
		tree := fs.File(t.TempDir()).Join("tree")
		writeFiles(t, tree, map[string]string{"file.txt": "file", "sub/a.txt": "a"})
		if err := os.Symlink("sub", tree.Join("link").LocalPath()); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		dest := newLinkMemFS(t).RootDir().Join("moved")

		require.NoError(t, tree.MoveTo(t.Context(), dest))
		requireMissing(t, tree)
		requireContent(t, dest.Join("file.txt"), "file")
		requireLink(t, dest.Join("link"), "sub")
		requireFiles(t, dest.Join("link"), "a.txt")
	})
}

// TestMove_CrossFileSystemFollowedLink checks the move of a symbolic link
// to another file system: for a link to a file the file is copied and only
// the link is removed. A link to a directory can't be moved, because its
// target is outside of the moved link, so the destination would resolve
// it to another directory. A failed copy keeps the link and its target.
func TestMove_CrossFileSystemFollowedLink(t *testing.T) {
	setup := func(t *testing.T) (dirLink, fileLink, outside fs.File) {
		dir := fs.File(t.TempDir())
		writeFiles(t, dir, map[string]string{"outside/precious.txt": "precious"})
		outside = dir.Join("outside")
		dirLink, fileLink = dir.Join("dir-link"), dir.Join("file-link")
		if err := os.Symlink(outside.Path(), dirLink.Path()); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		require.NoError(t, os.Symlink(outside.Join("precious.txt").Path(), fileLink.Path()))
		return dirLink, fileLink, outside
	}

	t.Run("commit", func(t *testing.T) {
		dirLink, fileLink, outside := setup(t)
		dest := newLinkMemFS(t).RootDir()

		err := fs.Move(t.Context(), dirLink, dest.Join("dir"))
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, dirLink.String(), "the error must name the link")
		requireMissing(t, dest.Join("dir"))
		requireLink(t, dirLink, outside.Path())
		require.NoError(t, fs.Move(t.Context(), fileLink, dest.Join("file.txt")))
		requireContent(t, dest.Join("file.txt"), "precious")
		require.False(t, dest.Join("file.txt").IsSymbolicLink(), "the file must be copied")
		requireMissing(t, fileLink)
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("destination without symbolic links", func(t *testing.T) {
		dirLink, fileLink, outside := setup(t)
		dest := newMinimalMemFS(t).RootDir()

		err := fs.Move(t.Context(), dirLink, dest.Join("dir"))
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, dirLink.String(), "the error must name the link")
		requireMissing(t, dest.Join("dir"))
		require.True(t, dirLink.IsSymbolicLink(), "the link must be kept")
		require.NoError(t, fs.Move(t.Context(), fileLink, dest.Join("file.txt")))
		requireContent(t, dest.Join("file.txt"), "precious")
		requireMissing(t, fileLink)
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("abort", func(t *testing.T) {
		_, fileLink, outside := setup(t)
		m := newMinimalMemFS(t)
		m.closeErr = errors.New("upload failed")
		dest := m.RootDir()

		require.ErrorIs(t, fs.Move(t.Context(), fileLink, dest.Join("file.txt")), m.closeErr)
		require.True(t, fileLink.IsSymbolicLink(), "the link must be kept")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
}

// TestMove_CrossFileSystemSymlinkLoop is a regression test: moving a directory
// with a symbolic link to itself to another file system copied the directory
// into itself until the path had too many links.
func TestMove_CrossFileSystemSymlinkLoop(t *testing.T) {
	setup := func(t *testing.T) fs.File {
		s := newSymlinkMemFS(t)
		src := s.RootDir()
		writeFiles(t, src, map[string]string{"dir/a.txt": "a", "dir/sub/b.txt": "b"})
		require.NoError(t, s.CreateSymbolicLink(".", "/dir/loop"))
		return src
	}

	t.Run("destination with symbolic links", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		src := setup(t)
		dest := newLinkMemFS(t).RootDir()

		require.NoError(t, fs.Move(ctx, src.Join("dir"), dest.Join("dir")))
		requireFiles(t, dest.Join("dir"), "a.txt", "loop", "sub/b.txt")
		requireLink(t, dest.Join("dir", "loop"), ".")
		requireMissing(t, src.Join("dir"))
	})
	t.Run("destination without symbolic links", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		src := setup(t)
		dest := newMinimalMemFS(t).RootDir()

		err := fs.Move(ctx, src.Join("dir"), dest.Join("dir"))
		require.ErrorIs(t, err, errors.ErrUnsupported)
		requireMissing(t, dest.Join("dir"))
		requireFiles(t, src.Join("dir"), "a.txt", "loop", "sub/b.txt")
	})
}

// TestMove_SameFileThroughSubFileSystem is a regression test: moving a local
// file onto itself through a SubFileSystem truncated the file with the copy
// and then removed it as the source of the move, and moving a directory into
// itself through a SubFileSystem copied it into itself again and again.
func TestMove_SameFileThroughSubFileSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := fs.File(t.TempDir())
	writeFiles(t, dir, map[string]string{"file.txt": "precious", "sub/a.txt": "a"})
	sub, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
	require.NoError(t, err)
	register(t, sub)

	require.NoError(t, fs.Move(ctx, sub.RootDir().Join("file.txt"), dir.Join("file.txt")), "a no-op")
	requireContent(t, dir.Join("file.txt"), "precious")

	// The destination is an existing directory, the source
	// itself, so the source is moved into itself
	err = fs.Move(ctx, dir.Join("sub"), sub.RootDir().Join("sub"))
	require.ErrorIs(t, err, os.ErrInvalid)
	requireFiles(t, dir.Join("sub"), "a.txt")
}

// TestMove_LinkOntoItsTarget is a regression test: moving a symbolic link
// onto the file it points to replaced the file with the link, which then
// pointed to itself, and Move returned nil. Like mv, which reports that
// they are the same file, Move refuses with an error wrapping os.ErrInvalid.
func TestMove_LinkOntoItsTarget(t *testing.T) {
	t.Run("LocalFileSystem", func(t *testing.T) {
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		file := dir.Join("file.txt")
		require.NoError(t, file.WriteAllString(t.Context(), "precious"))
		for _, target := range []string{"file.txt", file.LocalPath()} {
			link := dir.Join("link")
			require.NoError(t, os.Symlink(target, link.LocalPath()))

			err := fs.Move(t.Context(), link, file)
			require.ErrorIs(t, err, os.ErrInvalid, "link to %s", target)
			requireContent(t, file, "precious")
			requireLink(t, link, target)
			require.NoError(t, link.Remove())
		}
	})
	t.Run("through a SubFileSystem", func(t *testing.T) {
		// The move emulation between the view and the local file system
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		file := dir.Join("file.txt")
		require.NoError(t, file.WriteAllString(t.Context(), "precious"))
		require.NoError(t, os.Symlink("file.txt", dir.Join("link").LocalPath()))
		sub, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
		require.NoError(t, err)
		register(t, sub)

		err = fs.Move(t.Context(), sub.RootDir().Join("link"), file)
		require.ErrorIs(t, err, os.ErrInvalid)
		requireContent(t, file, "precious")
		requireLink(t, dir.Join("link"), "file.txt")
	})
	t.Run("move emulation", func(t *testing.T) {
		s := newSymlinkMemFS(t)
		root := s.RootDir()
		writeFiles(t, root, map[string]string{"file.txt": "precious", "real/other.txt": "other"})
		links := map[string]string{
			"/link":     "/file.txt",
			"/relative": "file.txt",
			"/chain":    "/link", // a link to the link
			"/alias":    "/real", // a link to the directory of the next link's target
			"/via":      "/alias/other.txt",
		}
		for link, target := range links {
			require.NoError(t, s.CreateSymbolicLink(target, link))
		}

		for _, link := range []string{"link", "relative", "chain"} {
			_, err := root.Join(link).Rename(t.Context(), "file.txt")
			require.ErrorIs(t, err, os.ErrInvalid, "Rename of %s", link)
			requireContent(t, root.Join("file.txt"), "precious")
			requireLink(t, root.Join(link), links["/"+link])
		}
		err := fs.Move(t.Context(), root.Join("via"), root.Join("real", "other.txt"))
		require.ErrorIs(t, err, os.ErrInvalid, "Move of a link through a link to the directory")
		requireContent(t, root.Join("real", "other.txt"), "other")
		requireLink(t, root.Join("via"), "/alias/other.txt")
	})
}

// TestMove_CaseOnlyRenameThroughSubFileSystem is a regression test: a move
// between the local file system and a SubFileSystem of the same directory
// to a name that only differs in case did nothing on a case-insensitive file
// system and returned nil, while LocalFileSystem.Move renames the file.
func TestMove_CaseOnlyRenameThroughSubFileSystem(t *testing.T) {
	dir := caseInsensitiveTempDir(t)
	writeFiles(t, dir, map[string]string{"file.txt": "file"})
	sub, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
	require.NoError(t, err)
	register(t, sub)

	require.NoError(t, fs.Move(t.Context(), dir.Join("file.txt"), sub.RootDir().Join("File.txt")))
	requireNames(t, dir, "File.txt")
	require.NoError(t, fs.Move(t.Context(), sub.RootDir().Join("File.txt"), dir.Join("FILE.txt")))
	requireNames(t, dir, "FILE.txt")
	requireContent(t, dir.Join("FILE.txt"), "file")

	// The same name is still a no-op
	require.NoError(t, fs.Move(t.Context(), sub.RootDir().Join("FILE.txt"), dir.Join("FILE.txt")))
	requireNames(t, dir, "FILE.txt")
	requireContent(t, dir.Join("FILE.txt"), "file")
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
