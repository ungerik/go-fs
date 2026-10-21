package fs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTempDir(t *testing.T) {
	require.True(t, TempDir().IsDir(), "temp directory exists")
}

func TestTempFile(t *testing.T) {
	file := TempFile(".txt")
	require.Equal(t, TempDir().LocalPath(), file.Dir().LocalPath())
	require.Equal(t, ".txt", file.Ext())
	require.False(t, file.Exists(), "TempFile only returns a path")
	require.NotEqual(t, file, TempFile(".txt"), "random names")

	require.Equal(t, "", TempFile("").Ext())
}

func TestCreateTempFile(t *testing.T) {
	file, err := CreateTempFile(".txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Remove() })
	require.True(t, file.IsRegular(), "the file is created")
	require.Equal(t, int64(0), file.Size())
	require.Equal(t, ".txt", file.Ext())
	require.Equal(t, TempDir().LocalPath(), file.Dir().LocalPath())

	_, err = CreateTempFile("/sub.txt")
	require.Error(t, err, "path separator in ext")
}

// TestTempFileCopy checks that TempFileCopy creates the temp file
// atomically with CreateTempFile and removes it if copying fails.
// It used to write to a path from TempFile, which another process
// could create in the meantime.
func TestTempFileCopy(t *testing.T) {
	source := NewMemFile("report.pdf", []byte("%PDF-1.7"))
	file, err := TempFileCopy(t.Context(), source)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Remove() })
	require.Equal(t, ".pdf", file.Ext())
	require.Equal(t, TempDir().LocalPath(), file.Dir().LocalPath())
	assertFileContent(t, file, "%PDF-1.7")

	// A File source is streamed
	copied, err := TempFileCopy(t.Context(), file)
	require.NoError(t, err)
	t.Cleanup(func() { _ = copied.Remove() })
	require.NotEqual(t, file, copied)
	assertFileContent(t, copied, "%PDF-1.7")

	// A failed copy removes the temp file
	before := tempFilesWithExt(t, ".failedcopy")
	file, err = TempFileCopy(t.Context(), File(t.TempDir()).Join("missing.failedcopy"))
	require.Error(t, err)
	require.Equal(t, InvalidFile, file)
	require.Equal(t, before, tempFilesWithExt(t, ".failedcopy"), "no temp file left behind")
}

// tempFilesWithExt returns the names of the files in TempDir with ext.
func tempFilesWithExt(t *testing.T, ext string) []string {
	t.Helper()
	var names []string
	err := TempDir().ListDir(t.Context(), func(f File) error {
		if strings.HasSuffix(f.Name(), ext) {
			names = append(names, f.Name())
		}
		return nil
	})
	require.NoError(t, err)
	return names
}
