package sftpfs

import (
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

func TestDial(t *testing.T) {
	// https://www.sftp.net/public-online-sftp-servers
	{
		sftpFS, err := Dial("test.rebex.net:22", "demo", "password", nil)
		require.NoError(t, err, "Dial")

		f := fs.File("sftp://test.rebex.net:22/readme.txt")
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
		sftpFS, err := Dial("demo.wftpserver.com:2222", "demo", "demo", nil)
		require.NoError(t, err, "Dial")

		f := fs.File("sftp://demo.wftpserver.com:2222/download/version.txt")
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
