package fs

import (
	"context"
	"os"
	"testing"
	"testing/fstest"
	"time"

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
		stdFS := NewStdFileSystemAndRegister(fixture, "")
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

// TestStdFSDirFileClose verifies that Close makes a directory opened
// through StdFS unusable, like os.File.Close. The no-op Close left
// ReadDir and Stat fully working after the file was closed.
func TestStdFSDirFileClose(t *testing.T) {
	mapFS := fstest.MapFS{
		"dir/a.txt": &fstest.MapFile{Data: []byte("a")},
		"dir/b.txt": &fstest.MapFile{Data: []byte("b")},
	}
	stdFS := NewStdFileSystemAndRegister(mapFS, "")
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
