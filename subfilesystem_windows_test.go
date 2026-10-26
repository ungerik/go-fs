package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewSubFileSystem_WindowsRootWithoutDrive checks that a local root
// without a drive, like `\x` or "/", for which filepath.IsAbs is false on
// Windows, is a path of the current drive, not one below the current
// directory.
func TestNewSubFileSystem_WindowsRootWithoutDrive(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	if !strings.EqualFold(filepath.VolumeName(dir), filepath.VolumeName(cwd)) {
		t.Skip("the temp directory is on another drive than the current directory")
	}
	withoutDrive := dir[len(filepath.VolumeName(dir)):]

	for _, root := range []string{withoutDrive, filepath.ToSlash(withoutDrive), Local.URI(dir)} {
		view, err := NewSubFileSystem(Local, root, "")
		require.NoError(t, err, "NewSubFileSystem(Local, %q)", root)
		require.True(t, strings.EqualFold(dir, view.dir), "root of %q is %q, not %q", root, view.dir, dir)
	}
	view, err := NewSubFileSystem(Local, "/", "")
	require.NoError(t, err)
	require.True(t, strings.EqualFold(filepath.VolumeName(cwd)+`\`, view.dir), "root of / is %q", view.dir)
}
