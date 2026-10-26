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
// directory, and that a long-path root keeps the trailing dot of a name,
// which filepath.Abs would remove.
func TestNewSubFileSystem_WindowsRootWithoutDrive(t *testing.T) {
	dir := t.TempDir()
	// The temp directory and the working directory of the GitHub runners
	// are on different drives, a root without a drive is on the current one
	t.Chdir(dir)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	withoutDrive := dir[len(filepath.VolumeName(dir)):]

	for _, root := range []string{withoutDrive, filepath.ToSlash(withoutDrive), Local.URI(dir), "file://" + filepath.ToSlash(withoutDrive)} {
		view, err := NewSubFileSystem(Local, root, "")
		require.NoError(t, err, "NewSubFileSystem(Local, %q)", root)
		require.True(t, strings.EqualFold(dir, view.dir), "root of %q is %q, not %q", root, view.dir, dir)
	}
	view, err := NewSubFileSystem(Local, "/", "")
	require.NoError(t, err)
	require.True(t, strings.EqualFold(filepath.VolumeName(cwd)+`\`, view.dir), "root of / is %q", view.dir)

	longRoot := `\\?\` + filepath.Join(dir, "name.")
	require.NoError(t, os.Mkdir(longRoot, 0o700))
	view, err = NewSubFileSystem(Local, longRoot, "")
	require.NoError(t, err, "NewSubFileSystem(Local, %q)", longRoot)
	require.Equal(t, longRoot, view.dir, "the long-path root keeps the trailing dot")
}
