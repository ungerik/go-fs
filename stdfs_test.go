package fs

import (
	"context"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStdFS(t *testing.T) {
	// A controlled tree instead of the repository checkout, which
	// makes the walk deterministic and fast on every platform.
	dir := MustMakeTempDir()
	t.Cleanup(func() { dir.RemoveRecursive(context.Background()) })
	require.NoError(t, dir.Join("sub").MakeDir())
	files := []string{"a.txt", "b.txt", "sub/c.txt", "sub/.hidden"}
	for _, name := range files {
		require.NoError(t, dir.Join(name).WriteAllString(t.Context(), name))
	}
	// NTFS updates the modification time cached in the parent's directory
	// entry lazily, so a directory listing can report an older ModTime for
	// "sub" than Stat until a handle to the directory is written and closed.
	now := time.Now()
	require.NoError(t, os.Chtimes(dir.Join("sub").LocalPath(), now, now))

	err := fstest.TestFS(dir.StdFS(), files...)
	if err != nil {
		t.Fatal(err)
	}
}
