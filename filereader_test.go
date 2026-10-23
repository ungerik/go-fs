package fs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileReader_StatAndIsSymbolicLink(t *testing.T) {
	dir := File(t.TempDir())
	file := dir.Join("a.txt")
	require.NoError(t, file.WriteAllString(t.Context(), "abc"))

	for _, fileReader := range []FileReader{file, NewMemFile("a.txt", []byte("abc"))} {
		info, err := fileReader.Stat()
		require.NoError(t, err)
		require.Equal(t, "a.txt", info.Name())
		require.Equal(t, int64(3), info.Size())
		require.True(t, info.Mode().IsRegular())
		require.False(t, fileReader.IsSymbolicLink())
	}

	link := dir.Join("link")
	if err := os.Symlink(file.LocalPath(), link.LocalPath()); err != nil {
		t.Skipf("can't create symbolic link: %s", err)
	}
	var fileReader FileReader = link
	require.True(t, fileReader.IsSymbolicLink())
	info, err := fileReader.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "Stat follows the link")
}

// TestFileReaderWithName checks that the wrapper
// also renames the result of Stat.
func TestFileReaderWithName(t *testing.T) {
	fileReader := FileReaderWithName(NewMemFile("a.txt", []byte("abc")), "renamed.json")
	require.Equal(t, "renamed.json", fileReader.Name())
	require.Equal(t, ".json", fileReader.Ext())
	require.Contains(t, fileReader.String(), "renamed.json")

	info, err := fileReader.Stat()
	require.NoError(t, err)
	require.Equal(t, "renamed.json", info.Name())
	require.Equal(t, int64(3), info.Size())

	_, err = FileReaderWithName(File(t.TempDir()).Join("missing"), "x").Stat()
	require.ErrorIs(t, err, os.ErrNotExist)
}
