package fs_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestCopyRecursive_WindowsInvalidNames is the Windows part of
// TestCopyRecursive_InvalidLocalNames: names that Windows writes to another
// entry than the one with that name must fail the copy and the move with
// an error wrapping os.ErrInvalid before they write anything, and the move
// must keep its source. A reserved device name, also with an extension,
// writes to the device, NUL discards the data, a name with a colon writes
// to an alternate data stream of another file, and Windows removes a dot
// or a space at the end of a name, so "dot." would be written to "dot".
func TestCopyRecursive_WindowsInvalidNames(t *testing.T) {
	badPaths := []string{
		"NUL", "nul", "CON", "AUX", "PRN", "COM1", "LPT1", "NUL.txt",
		"a:b.txt", "dot.", "space ", "...",
		"NUL/f.txt", "dot./f.txt", "sub/COM1",
	}
	for _, badPath := range badPaths {
		t.Run(fmt.Sprintf("%q", badPath), func(t *testing.T) {
			mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/"+badPath, []byte("bad")))
			src := mem.RootDir().Join("src")
			dir := fs.File(t.TempDir())
			sub, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
			require.NoError(t, err)
			register(t, sub)

			err = fs.CopyRecursive(t.Context(), src, dir.Join("copy"))
			require.ErrorIs(t, err, os.ErrInvalid)
			err = fs.CopyRecursive(t.Context(), src, sub.RootDir().Join("sub-copy"))
			require.ErrorIs(t, err, os.ErrInvalid, "through a SubFileSystem")
			err = fs.Move(t.Context(), src, dir)
			require.ErrorIs(t, err, os.ErrInvalid)
			requireNames(t, dir)
			requireContent(t, src.Join("a.txt"), "a")
			requireContent(t, src.Join(badPath), "bad")
		})
	}
}
