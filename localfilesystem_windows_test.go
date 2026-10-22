package fs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalFileSystem_MoveShortName is a regression test: the 8.3 short
// name of a file, like LONGFI~1.TXT, and its long name are the same
// directory entry, which strings.EqualFold didn't match. The source was
// taken for another hard link and removed, so Move returned nil and no
// file was left. It must rename the file to its long name.
func TestLocalFileSystem_MoveShortName(t *testing.T) {
	dir := t.TempDir()
	long := filepath.Join(dir, "long file name.txt")
	require.NoError(t, os.WriteFile(long, []byte("source"), 0o600))
	short := localShortPath(t, long)
	if filepath.Base(short) == filepath.Base(long) {
		t.Skipf("the volume of the temp directory creates no 8.3 short names: %s", short)
	}

	require.NoError(t, Local.Move(t.Context(), short, long))
	assert.Equal(t, []string{"long file name.txt"}, localDirNames(t, dir), "the file must keep its long name")
	requireLocalContent(t, long, "source")
}

// localShortPath returns the 8.3 short form of the existing filePath,
// which is filePath itself for names that have no short name.
func localShortPath(t *testing.T, filePath string) string {
	t.Helper()
	long, err := syscall.UTF16FromString(filePath)
	require.NoError(t, err)
	buf := make([]uint16, syscall.MAX_PATH)
	n, err := syscall.GetShortPathName(&long[0], &buf[0], uint32(len(buf)))
	require.NoError(t, err, "GetShortPathName(%q)", filePath)
	require.Less(t, int(n), len(buf), "GetShortPathName(%q) needs a larger buffer", filePath)
	return syscall.UTF16ToString(buf[:n])
}
