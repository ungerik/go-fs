//go:build unix

package fs_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestMoveEmulation_NamedPipeDestination is a regression test: a move of
// a file between file systems onto a named pipe wrote the file into the
// pipe, which blocks until the pipe has a reader, and then removed the
// source. The move must fail with an error wrapping os.ErrExist and keep
// the source, see mkfifo.
func TestMoveEmulation_NamedPipeDestination(t *testing.T) {
	pipe := fs.File(t.TempDir()).Join("f.txt")
	mkfifo(t, pipe)
	src := newLinkMemFS(t, fs.NewMemFile("/f.txt", []byte("data"))).RootDir().Join("f.txt")

	err := fs.Move(t.Context(), src, pipe)
	require.ErrorIs(t, err, os.ErrExist)
	requireContent(t, src, "data")
	requireNamedPipe(t, pipe)
}
