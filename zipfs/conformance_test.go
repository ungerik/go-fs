package zipfs

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() { _ = tempDir.RemoveRecursive() })
	zipFile := tempDir.Join("conformance.zip")

	t.Run("Writer", func(t *testing.T) {
		writer, err := NewWriterFileSystem(zipFile)
		require.NoError(t, err, "NewWriterFileSystem")

		// The writer is write-only: the suite writes the seed
		// and checks that reads are rejected, then closes the archive.
		fstest.RunConformance(t, writer, fstest.Config{
			Prefix:  writer.Prefix(),
			TestDir: "/seed",
		})
	})

	t.Run("Reader", func(t *testing.T) {
		if !zipFile.Exists() {
			// Running without the Writer subtest
			writeSeedArchive(t, zipFile, "/seed")
		}
		reader, err := NewReaderFileSystem(zipFile)
		require.NoError(t, err, "NewReaderFileSystem")

		fstest.RunConformance(t, reader, fstest.Config{
			Prefix:  reader.Prefix(),
			TestDir: "/seed",
		})
	})
}

// writeSeedArchive writes the default seed below dir into a new ZIP archive.
func writeSeedArchive(t *testing.T, zipFile fs.File, dir string) {
	t.Helper()
	writer, err := NewWriterFileSystem(zipFile)
	require.NoError(t, err, "NewWriterFileSystem")
	for name, content := range fstest.DefaultSeed() {
		w, err := writer.OpenWriter(writer.JoinCleanPath(dir, name), nil)
		require.NoError(t, err, "OpenWriter")
		_, err = w.Write(content)
		require.NoError(t, err, "Write")
		require.NoError(t, w.Close(), "Close entry")
	}
	require.NoError(t, writer.Close(), "Close archive")
}
