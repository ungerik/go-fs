package tests

import (
	"testing"

	"github.com/ungerik/go-fs"

	"github.com/stretchr/testify/require"
)

func TestLocalFileSystem(t *testing.T) {
	dir := fs.TempDir()
	require.True(t, dir.Exists(), "TempDir exists")

	fileContent := []byte("TestLocalFileSystem")

	file := fs.TempFile("read_test")
	err := file.WriteAll(fileContent)
	require.NoError(t, err)
	t.Cleanup(func() { file.Remove() })

	TestFileReads(t, fileContent, file)
}
