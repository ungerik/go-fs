package fs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMemFileSystem(t *testing.T) {
	for _, sep := range []string{`/`, `\`} {
		fs, err := NewMemFileSystem(sep)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
		require.True(t, fs.RootDir().Exists(), "root dir exists")
		require.True(t, fs.RootDir().IsDir(), "root is dir")

		fs = fs.WithVolume("C:")
		require.True(t, strings.HasSuffix(fs.Prefix(), "C:"))
		require.True(t, strings.HasSuffix(string(fs.RootDir()), "C:"+sep))

		err = fs.Close()
		require.NoError(t, err)
		require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
		require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")
	}
}

func TestNewSingleMemFileSystem(t *testing.T) {
	fs, f, err := NewSingleMemFileSystem(NewMemFile("test.txt", []byte("Hello, World!")))
	require.NoError(t, err, "NewSingleMemFileSystem")

	t.Cleanup(func() { _ = fs.Close() })

	// Check fs
	require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
	require.True(t, fs.RootDir().Exists(), "root directory exists")
	require.True(t, fs.RootDir().IsDir(), "root is a directory")
	// TODO: ListDirMax is not implemented
	// files, err := fs.RootDir().ListDirMax(-1)
	// require.NoError(t, err, "ListDirMax")
	// require.Len(t, files, 1, "root directory contains one file")
	// require.Equal(t, "test.txt", files[0].Name(), "root directory contains test.txt")

	// Check non-existent file
	require.False(t, fs.RootDir().Join("non-existent.txt").Exists(), "non-existent.txt does not exists")
	require.False(t, fs.RootDir().Join("non-existent.txt").IsDir(), "non-existent.txt is not a directory")
	require.False(t, f.Join("non-existent.txt").Exists(), "test.txt/non-existent.txt does not exists")

	// Check test.txt
	require.True(t, f.Exists(), "test.txt exists")
	require.False(t, f.IsDir(), "test.txt is not a directory")
	require.True(t, f.Dir().Exists(), "root directory exists")
	require.True(t, f.Dir().IsDir(), "root is a directory")
	content, err := f.ReadAllString()
	require.NoError(t, err, "ReadAllString")
	require.Equal(t, "Hello, World!", content)

	err = fs.Close()
	require.NoError(t, err, "Close")
	require.False(t, f.Exists(), "test.txt does not exist after close")
	require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
	require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")
}
