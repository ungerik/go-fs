package fs_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestMoveEmulation_WindowsInvalidLocalNames is the Windows part of
// TestMoveEmulation_InvalidLocalName: a move of a file to a local name that
// Windows writes to another entry, like "NUL" to the device, which discards
// the data, or refuses, like "report?.pdf", must fail with an error wrapping
// os.ErrInvalid and keep the source, see TestCopyRecursive_WindowsInvalidNames.
func TestMoveEmulation_WindowsInvalidLocalNames(t *testing.T) {
	for _, name := range []string{"NUL", "con", "COM1", "NUL.txt", "a:b.txt", "dot.", "space ", "report?.pdf", "a|b", "a\x1fb"} {
		t.Run(name, func(t *testing.T) {
			src := newLinkMemFS(t, fs.NewMemFile("/"+name, []byte("data"))).RootDir().Join(name)
			dir := fs.File(t.TempDir())

			err := fs.Move(t.Context(), src, dir)
			require.ErrorIs(t, err, os.ErrInvalid)
			requireNames(t, dir)
			requireContent(t, src, "data")
		})
	}
}
