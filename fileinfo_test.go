package fs

import (
	iofs "io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFileInfo_StdFileInfo is a regression test: StdFileInfo added
// io/fs.ModeSymlink to the mode of a FileInfo from Stat, which describes
// the link target, so Mode().IsRegular() was false for a link to a regular
// file and the mode of a link to a directory was "dL".
func TestFileInfo_StdFileInfo(t *testing.T) {
	modified := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name     string
		info     FileInfo
		wantType iofs.FileMode
	}{
		{name: "regular file", info: FileInfo{IsRegular: true}, wantType: 0},
		{name: "directory", info: FileInfo{IsDir: true}, wantType: iofs.ModeDir},
		{name: "link to regular file", info: FileInfo{IsRegular: true, IsSymlink: true}, wantType: 0},
		{name: "link to directory", info: FileInfo{IsDir: true, IsSymlink: true}, wantType: iofs.ModeDir},
		{name: "irregular file", info: FileInfo{}, wantType: iofs.ModeIrregular},
		{name: "link with unknown target", info: FileInfo{IsSymlink: true}, wantType: iofs.ModeIrregular},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.info.Name = "x"
			tt.info.Exists = true
			tt.info.Size = 7
			tt.info.Modified = modified
			tt.info.Permissions = 0o640
			tt.info.Sys = "sys"
			info := tt.info.StdFileInfo()

			require.Equal(t, tt.wantType, info.Mode().Type())
			require.Equal(t, tt.info.IsDir, info.IsDir())
			require.Equal(t, tt.info.IsDir, info.Mode().IsDir(), "Mode().IsDir() equals IsDir")
			require.Equal(t, tt.info.IsRegular, info.Mode().IsRegular(), "Mode().IsRegular() equals IsRegular")
			require.Equal(t, iofs.FileMode(0o640), info.Mode().Perm())
			require.Equal(t, "x", info.Name())
			require.Equal(t, int64(7), info.Size())
			require.Equal(t, modified, info.ModTime())
			require.Equal(t, "sys", info.Sys())
		})
	}
}

func TestNewFileInfo(t *testing.T) {
	memFile := NewMemFile("a.txt", []byte("abc"))
	stat, err := memFile.Stat()
	require.NoError(t, err)
	info := NewFileInfo("a.txt", stat, false)
	require.Equal(t, &FileInfo{
		File:        "a.txt",
		Name:        "a.txt",
		Exists:      true,
		IsRegular:   true,
		Size:        3,
		Permissions: 0o666,
	}, info)
	require.NoError(t, info.Validate())

	require.Error(t, (*FileInfo)(nil).Validate())
	require.ErrorIs(t, (&FileInfo{}).Validate(), ErrEmptyPath)

	nonExisting := NewNonExistingFileInfo(File("/tmp/.hidden"))
	require.False(t, nonExisting.Exists)
	require.True(t, nonExisting.IsHidden)
	require.Equal(t, ".hidden", nonExisting.Name)
}
