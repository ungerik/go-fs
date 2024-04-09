package sftpfs

import (
	"context"
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
	assert.NoError(t, err)
	return data
}

func TestDialAndRegister(t *testing.T) {
	// https://www.sftp.net/public-online-sftp-servers
	{
		sftpFS, err := DialAndRegister(context.Background(), "demo@test.rebex.net:22", Password("password"), AcceptAnyHostKey)
		require.NoError(t, err, "Dial")

		require.Equal(t, "sftp://demo@test.rebex.net", sftpFS.Prefix())
		id, err := sftpFS.ID()
		require.NoError(t, err)
		require.Equal(t, "sftp://demo@test.rebex.net", id)
		require.Equal(t, "sftp://demo@test.rebex.net file system", sftpFS.String())
		require.Equal(t, "SFTP", sftpFS.Name())
		require.Equal(t, "/a/b", sftpFS.JoinCleanPath("a", "skip", "..", "/", "b", "/"))
		require.Equal(t, fs.File("sftp://demo@test.rebex.net/a/b"), sftpFS.JoinCleanFile("a", "skip", "..", "/", "b", "/"))

		f := fs.File("sftp://demo@test.rebex.net/readme.txt")
		assert.Equal(t, "readme.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		// files, err := fs.File("sftp://test.rebex.net:22/").ListDirMax(-1)
		// fmt.Println(files)
		// t.Fatal("todo")

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	}
	{
		// http://demo.wftpserver.com/main.html
		sftpFS, err := DialAndRegister(context.Background(), "demo.wftpserver.com:2222", UsernameAndPassword("demo", "demo"), AcceptAnyHostKey)
		require.NoError(t, err, "Dial")
		require.Equal(t, "sftp://demo@demo.wftpserver.com:2222", sftpFS.Prefix())

		f := fs.File("sftp://demo@demo.wftpserver.com:2222/download/version.txt")
		assert.Equal(t, "version.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	}
}

func TestPasswordURL(t *testing.T) {
	{
		// http://demo.wftpserver.com/main.html
		f := fs.File("sftp://demo:demo@demo.wftpserver.com:2222/download/version.txt")
		assert.Equal(t, "version.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)
	}
}
