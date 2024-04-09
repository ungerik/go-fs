package ftpfs

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

func checkAndReadFile(t *testing.T, f fs.File) []byte {
	t.Helper()

	assert.True(t, f.Exists(), "Exists")
	assert.False(t, f.IsDir(), "not IsDir")
	data, err := f.ReadAll()
	require.NoError(t, err)
	return data
}

func TestDialAndRegister(t *testing.T) {
	{
		ftpFS, err := DialAndRegister(context.Background(), "ftp://demo@test.rebex.net", Password("password"), os.Stdout)
		require.NoError(t, err, "Dial")

		require.Equal(t, "ftp://demo@test.rebex.net", ftpFS.Prefix())
		id, err := ftpFS.ID()
		require.NoError(t, err)
		require.Equal(t, "ftp://demo@test.rebex.net", id)
		require.Equal(t, "ftp://demo@test.rebex.net file system", ftpFS.String())
		require.Equal(t, "FTP", ftpFS.Name())
		require.Equal(t, "/a/b", ftpFS.JoinCleanPath("a", "skip", "..", "/", "b", "/"))
		require.Equal(t, fs.File("ftp://demo@test.rebex.net/a/b"), ftpFS.JoinCleanFile("a", "skip", "..", "/", "b", "/"))

		f := fs.File("ftp://demo@test.rebex.net/readme.txt")
		assert.Equal(t, "readme.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0, "read more than zero bytes from readme.txt")

		// files, err := fs.File("ftp://test.rebex.net:21/").ListDirMax(-1)
		// fmt.Println(files)
		// t.Fatal("todo")

		err = ftpFS.Close()
		require.NoError(t, err, "Close")
	}
	// {
	// 	ftpFS, err := DialAndRegister(context.Background(), "ftps://demo@test.rebex.net", Password("password"), os.Stdout)
	// 	require.NoError(t, err, "Dial")

	// 	require.Equal(t, "ftps://demo@test.rebex.net", ftpFS.Prefix())
	// 	id, err := ftpFS.ID()
	// 	require.NoError(t, err)
	// 	require.Equal(t, "ftps://demo@test.rebex.net", id)
	// 	require.Equal(t, "ftps://demo@test.rebex.net file system", ftpFS.String())
	// 	require.Equal(t, "FTPS", ftpFS.Name())
	// 	require.Equal(t, "/a/b", ftpFS.JoinCleanPath("a", "skip", "..", "/", "b", "/"))
	// 	require.Equal(t, fs.File("ftps://demo@test.rebex.net/a/b"), ftpFS.JoinCleanFile("a", "skip", "..", "/", "b", "/"))

	// 	f := fs.File("ftps://demo@test.rebex.net/readme.txt")
	// 	assert.Equal(t, "readme.txt", f.Name())
	// 	data := checkAndReadFile(t, f)
	// 	assert.True(t, len(data) > 0, "read more than zero bytes from readme.txt")

	// 	// files, err := fs.File("ftp://test.rebex.net:21/").ListDirMax(-1)
	// 	// fmt.Println(files)
	// 	// t.Fatal("todo")

	// 	err = ftpFS.Close()
	// 	require.NoError(t, err, "Close")
	// }
}
