package sftpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

func TestDial(t *testing.T) {
	// https://www.sftp.net/public-online-sftp-servers
	ftpFS, err := Dial("test.rebex.net:22", "demo", "password")
	require.NoError(t, err, "Dial")

	f := fs.File("sftp://test.rebex.net:22/readme.txt")
	assert.True(t, f.Exists(), "Exists")
	assert.False(t, f.IsDir(), "not IsDir")
	data, err := f.ReadAll()
	assert.NoError(t, err)
	assert.True(t, len(data) > 0)

	// files, err := fs.File("sftp://test.rebex.net:22/").ListDirMax(-1)
	// fmt.Println(files)
	// t.Fatal("todo")

	err = ftpFS.Close()
	require.NoError(t, err, "Close")
}
