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
