//go:build unix

package fs_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ungerik/go-fs"
)

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

// requireNamedPipe requires the local file to be a named pipe.
func requireNamedPipe(t *testing.T, file fs.File) {
	t.Helper()
	info, err := os.Lstat(file.LocalPath())
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe, "%s must still be a named pipe", file)
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
