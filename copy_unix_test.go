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
	require.ErrorContains(t, err, "a named pipe", "the error must name what is refused")
	requireNamedPipe(t, pipe)
}

// TestCopyRecursiveAndMove_OverlayBaseNamedPipe checks that a copy and a move
// into an OverlayFileSystem with a local base layer replace a named pipe of
// the base layer: the write goes to a new file of the upper layer, which
// hides the pipe, so the copy never writes into it. Only a special file at
// the path that the write goes to is an error, see isSpecialFile. With an
// upper layer that is not local, the copy and the move were refused with an
// error wrapping os.ErrExist, because the FileInfo of the overlay, which
// describes the pipe, told for every upper layer that is not local.
func TestCopyRecursiveAndMove_OverlayBaseNamedPipe(t *testing.T) {
	uppers := []struct {
		name string
		// upper returns the upper layer and its root directory
		upper func(t *testing.T) (fs.WriteFileSystem, fs.File)
	}{
		{
			name: "local upper layer",
			upper: func(t *testing.T) (fs.WriteFileSystem, fs.File) {
				upperDir := fs.File(t.TempDir())
				upper, err := fs.NewSubFileSystem(fs.Local, upperDir.LocalPath(), "")
				require.NoError(t, err)
				return upper, upperDir
			},
		},
		{
			name: "upper layer that is not local",
			upper: func(t *testing.T) (fs.WriteFileSystem, fs.File) {
				upper := newLinkMemFS(t)
				return upper, upper.RootDir()
			},
		},
	}
	for _, tt := range uppers {
		t.Run(tt.name, func(t *testing.T) {
			baseDir := fs.File(t.TempDir())
			mkfifo(t, baseDir.Join("f.txt"))
			mkfifo(t, baseDir.Join("g.txt"))
			base, err := fs.NewSubFileSystem(fs.Local, baseDir.LocalPath(), "")
			require.NoError(t, err)
			upper, upperDir := tt.upper(t)
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
		})
	}
}
