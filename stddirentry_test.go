package fs

import (
	iofs "io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFile_StdDirEntry(t *testing.T) {
	dir := File(t.TempDir())
	require.NoError(t, dir.Join("a.txt").WriteAllString(t.Context(), "abc"))
	require.NoError(t, dir.Join("sub").MakeDir())

	entry := dir.Join("a.txt").StdDirEntry()
	require.Equal(t, "a.txt", entry.Name())
	require.False(t, entry.IsDir())
	require.Equal(t, iofs.FileMode(0), entry.Type())
	info, err := entry.Info()
	require.NoError(t, err)
	require.Equal(t, int64(3), info.Size())

	entry = dir.Join("sub").StdDirEntry()
	require.True(t, entry.IsDir())
	require.Equal(t, iofs.ModeDir, entry.Type())

	entry = dir.Join("missing").StdDirEntry()
	require.False(t, entry.IsDir())
	require.Equal(t, iofs.FileMode(0), entry.Type())
	_, err = entry.Info()
	require.ErrorIs(t, err, os.ErrNotExist)

	link := dir.Join("link")
	if err := os.Symlink(dir.Join("sub").LocalPath(), link.LocalPath()); err != nil {
		t.Skipf("can't create symbolic link: %s", err)
	}
	// Regression: the entry of a symbolic link described the link target
	// and had the type "dL"
	entry = link.StdDirEntry()
	require.False(t, entry.IsDir(), "a link to a directory is no directory entry")
	require.Equal(t, iofs.ModeSymlink, entry.Type())
	info, err = entry.Info()
	require.NoError(t, err)
	require.Equal(t, iofs.ModeSymlink, info.Mode().Type())
	require.False(t, info.IsDir())

	stat, err := link.Stat()
	require.NoError(t, err)
	require.True(t, stat.IsDir(), "Stat follows the link")
	require.Equal(t, iofs.ModeDir, stat.Mode().Type(), "Stat has no ModeSymlink")
}

// TestNewStdDirEntry checks that an entry from the FileInfo
// of a directory listing doesn't access the file system again.
func TestNewStdDirEntry(t *testing.T) {
	missing := File(t.TempDir()).Join("removed.txt")
	tests := []struct {
		name      string
		info      *FileInfo
		wantIsDir bool
		wantType  iofs.FileMode
	}{
		{
			name:     "regular file",
			info:     &FileInfo{File: missing, Name: "removed.txt", Exists: true, IsRegular: true, Size: 3, Permissions: 0o644},
			wantType: 0,
		},
		{
			name:      "directory",
			info:      &FileInfo{File: missing, Name: "dir", Exists: true, IsDir: true, Permissions: 0o755},
			wantIsDir: true,
			wantType:  iofs.ModeDir,
		},
		{
			name:     "link to file",
			info:     &FileInfo{File: missing, Name: "link", Exists: true, IsRegular: true, IsSymlink: true, Permissions: 0o777},
			wantType: iofs.ModeSymlink,
		},
		{
			name:     "link to directory",
			info:     &FileInfo{File: missing, Name: "link", Exists: true, IsDir: true, IsSymlink: true, Permissions: 0o777},
			wantType: iofs.ModeSymlink,
		},
		{
			name:     "link with unknown target",
			info:     &FileInfo{File: missing, Name: "link", Exists: true, IsSymlink: true, Permissions: 0o777},
			wantType: iofs.ModeSymlink,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The file does not exist, so every value comes from info
			entry := NewStdDirEntry(tt.info)
			require.Equal(t, missing, entry.File)
			require.Equal(t, tt.info.Name, entry.Name())
			require.Equal(t, tt.wantIsDir, entry.IsDir())
			require.Equal(t, tt.wantType, entry.Type())
			info, err := entry.Info()
			require.NoError(t, err)
			require.Equal(t, tt.wantType, info.Mode().Type())
			require.Equal(t, tt.wantIsDir, info.IsDir())
			require.Equal(t, tt.info.Permissions.FileMode(false).Perm(), info.Mode().Perm())
			require.Equal(t, tt.info.Size, info.Size())
		})
	}

	// The Name falls back to the name of the File
	entry := NewStdDirEntry(&FileInfo{File: missing, Exists: true, IsRegular: true})
	require.Equal(t, "removed.txt", entry.Name())
}

// TestStdFS_WalkDirSymlinkLoop is a regression test: the StdDirEntry of a
// symbolic link to a parent directory reported a directory, so
// io/fs.WalkDir followed the link in an endless loop.
func TestStdFS_WalkDirSymlinkLoop(t *testing.T) {
	dir := File(t.TempDir())
	require.NoError(t, dir.Join("a.txt").WriteAllString(t.Context(), "a"))
	if err := os.Symlink(".", dir.Join("loop").LocalPath()); err != nil {
		t.Skipf("can't create symbolic link: %s", err)
	}
	var visited []string
	err := iofs.WalkDir(dir.StdFS(), ".", func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, p)
		if len(visited) > 100 {
			return iofs.SkipAll
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{".", "a.txt", "loop"}, visited)
}
