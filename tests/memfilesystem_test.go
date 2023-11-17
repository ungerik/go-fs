package tests

// import (
// 	"testing"
// 	"time"

// 	"github.com/ungerik/go-fs"

// 	"github.com/stretchr/testify/require"
// )

// func TestMemFileSystem(t *testing.T) {
// 	memFS, err := fs.NewMemFileSystem("/")
// 	require.NoError(t, err)

// 	memFile := fs.NewMemFile("read_test", []byte("TestMemFileSystem"))

// 	file, err := memFS.AddMemFile(memFile, time.Now())

// 	TestFileReads(t, memFile.FileData, file)

// 	require.NoError(t, memFS.Close())
// }
