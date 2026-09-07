package tarfs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// TestConformance runs the suite over a writer, then over readers of
// the written plain and gzip compressed archives.
func TestConformance(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() { _ = tempDir.RemoveRecursive(context.Background()) })

	for _, name := range []string{"conformance.tar", "conformance.tar.gz"} {
		t.Run(name, func(t *testing.T) {
			tarFile := tempDir.Join(name)

			t.Run("Writer", func(t *testing.T) {
				writer, err := NewWriter(tarFile)
				require.NoError(t, err, "NewWriter")
				fstest.RunConformance(t, writer, fstest.Config{
					Name:    "Tar writer filesystem",
					Prefix:  writer.Prefix(),
					TestDir: "/seed",
				})
			})

			t.Run("Reader", func(t *testing.T) {
				if !tarFile.Exists() {
					writeSeedArchive(t, tarFile, "/seed")
				}
				reader, err := NewReader(tarFile)
				require.NoError(t, err, "NewReader")
				fstest.RunConformance(t, reader, fstest.Config{
					Name:    "Tar reader filesystem",
					Prefix:  reader.Prefix(),
					TestDir: "/seed",
				})
			})
		})
	}
}

// writeSeedArchive writes the default seed below dir into a new archive.
func writeSeedArchive(t *testing.T, tarFile fs.File, dir string) {
	t.Helper()
	writer, err := NewWriter(tarFile)
	require.NoError(t, err, "NewWriter")
	for name, content := range fstest.DefaultSeed() {
		w, err := writer.OpenWriter(writer.CleanPath(dir, name), 0)
		require.NoError(t, err, "OpenWriter")
		_, err = w.Write(content)
		require.NoError(t, err, "Write")
		require.NoError(t, w.Close(), "Close entry")
	}
	require.NoError(t, writer.Close(), "Close archive")
}

// TestImplicitDirectories verifies that directories only implied by
// entry paths exist, and that the modes are read after Close.
func TestImplicitDirectories(t *testing.T) {
	ctx := t.Context()
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() { _ = tempDir.RemoveRecursive(context.Background()) })
	tarFile := tempDir.Join("implicit.tgz")

	writer, err := NewWriter(tarFile)
	require.NoError(t, err)
	readable, writable := writer.ReadableWritable()
	assert.False(t, readable)
	assert.True(t, writable)
	require.NoError(t, writer.WriteAll(ctx, "/a/b/c.txt", []byte("c"), 0))
	require.NoError(t, writer.Touch("/a/empty.txt", 0))
	_, err = writer.Stat("/a")
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "no reads in writer mode")
	require.NoError(t, writer.Close())
	assert.NoError(t, writer.Close(), "Close is idempotent")
	_, err = writer.Stat("/a")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	reader, err := NewReader(tarFile)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reader.Close()) })
	readable, writable = reader.ReadableWritable()
	assert.True(t, readable)
	assert.False(t, writable)

	root := reader.RootDir()
	assert.True(t, root.Join("a").IsDir(), "implicit directory a")
	assert.True(t, root.Join("a", "b").IsDir(), "implicit directory a/b")
	data, err := root.Join("a", "b", "c.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "c", data)
	assert.Zero(t, root.Join("a", "empty.txt").Size())

	names, err := root.Join("a").ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{root.Join("a", "b"), root.Join("a", "empty.txt")}, names, "directories first")

	// Reader has no write methods, the package rejects writes
	// based on ReadableWritable.
	err = root.Join("a", "b", "c.txt").Remove()
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	err = root.Join("newdir").MakeDir()
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
}
