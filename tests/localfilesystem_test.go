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

	file := fs.TempFile(".read_test.txt")
	err := file.WriteAll(fileContent)
	require.NoError(t, err)
	t.Cleanup(func() { file.Remove() })

	TestFileReads(t, fileContent, file)

	info := fs.FileInfo{
		File:        file,
		Name:        file.Name(),
		Exists:      true,
		IsDir:       false,
		IsRegular:   true,
		IsHidden:    false,
		Size:        int64(len(fileContent)),
		Modified:    file.Modified(),
		Permissions: file.Permissions(), // TODO why not fs.Local.DefaultCreatePermissions,
	}
	TestFileMetadata(t, info, file)
}
