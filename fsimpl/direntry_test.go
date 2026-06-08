package fsimpl

import (
	iofs "io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testFileInfo is a minimal io/fs.FileInfo implementation for tests.
type testFileInfo struct {
	name string
	size int64
	mode iofs.FileMode
}

func (i testFileInfo) Name() string        { return i.name }
func (i testFileInfo) Size() int64         { return i.size }
func (i testFileInfo) Mode() iofs.FileMode { return i.mode }
func (i testFileInfo) ModTime() time.Time  { return time.Time{} }
func (i testFileInfo) IsDir() bool         { return i.mode.IsDir() }
func (i testFileInfo) Sys() any            { return nil }

func TestDirEntryFromFileInfo(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		info := testFileInfo{name: "file.txt", size: 123, mode: 0o644}
		de := DirEntryFromFileInfo(info)

		require.Equal(t, "file.txt", de.Name())
		require.False(t, de.IsDir())
		// Type returns only the type bits, which are zero for a regular file.
		require.Equal(t, iofs.FileMode(0), de.Type())

		got, err := de.Info()
		require.NoError(t, err)
		require.Equal(t, iofs.FileInfo(info), got)
	})

	t.Run("directory", func(t *testing.T) {
		info := testFileInfo{name: "dir", mode: iofs.ModeDir | 0o755}
		de := DirEntryFromFileInfo(info)

		require.Equal(t, "dir", de.Name())
		require.True(t, de.IsDir())
		// Type returns only the type bits, so the permission bits are dropped.
		require.Equal(t, iofs.ModeDir, de.Type())

		got, err := de.Info()
		require.NoError(t, err)
		require.Equal(t, iofs.FileInfo(info), got)
	})
}
