//go:build unix

package fs_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ungerik/go-fs"
)

func init() {
	makeNamedPipe = mkfifo
}

// mkfifo creates a named pipe at the local file and skips the test if it
// can't be created. The pipe is opened for reading until the end of the
// test, so that a writer that opens it doesn't block but writes into the
// pipe, which a test can tell from a failed copy.
func mkfifo(t *testing.T, file fs.File) {
	t.Helper()
	if err := unix.Mkfifo(file.LocalPath(), 0o600); err != nil {
		t.Skipf("can't create a named pipe: %v", err)
	}
	reader, err := os.OpenFile(file.LocalPath(), os.O_RDONLY|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
}

// TestCopyRecursive_NamedPipeDestination is a regression test: a tree copy
// over a named pipe of the destination opened the pipe for writing and wrote
// the file into it, which blocks until the pipe has a reader. A file of the
// destination that is no regular file must be an error wrapping os.ErrExist.
func TestCopyRecursive_NamedPipeDestination(t *testing.T) {
	dest := fs.File(t.TempDir())
	pipe := dest.Join("f.txt")
	mkfifo(t, pipe)
	src := newLinkMemFS(t, fs.NewMemFile("/src/f.txt", []byte("data"))).RootDir().Join("src")

	err := fs.CopyRecursive(t.Context(), src, dest)
	require.ErrorIs(t, err, os.ErrExist)
	requireNamedPipe(t, pipe)
}

// TestCopyRecursiveAndMove_OverlayBaseNamedPipe checks that a copy and a move
// into an OverlayFileSystem with local layers replace a named pipe of the
// base layer: the write goes to a new file of the upper layer, which hides
// the pipe, so the copy never writes into it. Only a special file at the
// local path that the write goes to is an error, see isSpecialFile.
func TestCopyRecursiveAndMove_OverlayBaseNamedPipe(t *testing.T) {
	baseDir, upperDir := fs.File(t.TempDir()), fs.File(t.TempDir())
	mkfifo(t, baseDir.Join("f.txt"))
	mkfifo(t, baseDir.Join("g.txt"))
	base, err := fs.NewSubFileSystem(fs.Local, baseDir.LocalPath(), "")
	require.NoError(t, err)
	upper, err := fs.NewSubFileSystem(fs.Local, upperDir.LocalPath(), "")
	require.NoError(t, err)
	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	register(t, overlay)
	mem := newLinkMemFS(t, fs.NewMemFile("/src/f.txt", []byte("copied")), fs.NewMemFile("/g.txt", []byte("moved")))

	require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), overlay.RootDir()))
	require.NoError(t, fs.Move(t.Context(), mem.RootDir().Join("g.txt"), overlay.RootDir()))
	requireContent(t, overlay.RootDir().Join("f.txt"), "copied")
	requireContent(t, overlay.RootDir().Join("g.txt"), "moved")
	requireNames(t, upperDir, "f.txt", "g.txt")
	requireNamedPipe(t, baseDir.Join("f.txt"))
	requireNamedPipe(t, baseDir.Join("g.txt"))
	requireMissing(t, mem.RootDir().Join("g.txt"))
}

// TestCopyRecursive_HardLinks is a regression test: in a local directory that
// existed before, two hard links of one file were taken for a name collision.
// The copy wrote the first name, which also changed the file of the second,
// and then found the second name to be a file that it wrote for another name
// of the source, like another case, and failed with an error wrapping
// os.ErrExist. Two hard links are two entries of the directory, the copy must
// write both. They stay links of one file, which has the data written last.
func TestCopyRecursive_HardLinks(t *testing.T) {
	dest := fs.File(t.TempDir())
	writeFiles(t, dest, map[string]string{"x.txt": "old"})
	if err := os.Link(dest.Join("x.txt").LocalPath(), dest.Join("y.txt").LocalPath()); err != nil {
		t.Skipf("can't create a hard link: %v", err)
	}
	mem := newLinkMemFS(t, fs.NewMemFile("/src/x.txt", []byte("new")), fs.NewMemFile("/src/y.txt", []byte("new")))

	require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest))
	requireNames(t, dest, "x.txt", "y.txt")
	requireContent(t, dest.Join("x.txt"), "new")
	requireContent(t, dest.Join("y.txt"), "new")
}
