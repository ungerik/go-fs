package fs

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLocalFileSystem_LongPathURI checks that the file URI of a long path
// keeps the prefix `\\?\`, which keeps the trailing dot of the name "name."
// that Windows would remove from a normal path, so that the URI resolves
// to the same file, with the content written through the long path.
func TestLocalFileSystem_LongPathURI(t *testing.T) {
	long := `\\?\` + filepath.Join(t.TempDir(), "name.")
	require.NoError(t, os.WriteFile(long, []byte("long"), 0o600))

	uri := File(long).URI()
	assert.True(t, strings.HasPrefix(uri, LocalPrefix+"//?/"), "the URI %q keeps the long path", uri)
	assert.Equal(t, long, File(uri).LocalPath(), "the URI resolves to the long path")
	data, err := File(uri).ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "long", string(data))
}
