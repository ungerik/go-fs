package fs_test

import (
	"fmt"
	"os"
	"path/filepath"
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
// Windows refuses the characters < > " | ? * and control characters in
// names, so the copy of a name like the S3 key "report?.pdf" failed at that
// file, after it had written the files before it.
func TestCopyRecursive_WindowsInvalidNames(t *testing.T) {
	badPaths := []string{
		"NUL", "nul", "CON", "AUX", "PRN", "COM1", "LPT1", "NUL.txt",
		"a:b.txt", "dot.", "space ", "...",
		"report?.pdf", "a<b", "a>b", `a"b`, "a|b", "a*b", "a\x01b", "a\x1fb",
		"NUL/f.txt", "dot./f.txt", "sub/COM1", "q?/f.txt", "sub/a|b",
	}
	for _, badPath := range badPaths {
		t.Run(fmt.Sprintf("%q", badPath), func(t *testing.T) {
			skipUnlessReservedName(t, badPath)
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

// TestCopyRecursive_WindowsInvalidEmptyDirNames is the Windows part of
// TestCopyRecursive_InvalidLocalNamesWithoutFiles: an empty directory with
// a name that Windows writes to another entry must fail the copy and the move
// before they write anything, and the move must keep its source.
func TestCopyRecursive_WindowsInvalidEmptyDirNames(t *testing.T) {
	for _, name := range []string{"NUL", "COM1", "a:b", "dot.", "space ", "q?", "a*b", "a\x01b"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			requireInvalidLocalDirName(t, name)
		})
	}
}

// skipUnlessReservedName skips the test of name if it is a device name with
// an extension, like "NUL.txt", that this version of Windows doesn't treat
// as the device: Windows 11 and Windows Server 2025 write "NUL.txt" to an
// ordinary file, which filepath.Localize, asking Windows, accepts.
func skipUnlessReservedName(t *testing.T, name string) {
	t.Helper()
	if name != "NUL.txt" {
		return
	}
	if _, err := filepath.Localize(name); err == nil {
		t.Skipf("this version of Windows doesn't treat %q as a device", name)
	}
}
