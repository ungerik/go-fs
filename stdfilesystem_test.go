package fs_test

import (
	"embed"
	"os"
	"testing"
	stdfstest "testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

//go:embed docs/*.md
var embeddedDocs embed.FS

// seedMapFS returns a testing/fstest.MapFS with the seed below dir.
func seedMapFS(dir string, seed fstest.Seed) stdfstest.MapFS {
	mapFS := make(stdfstest.MapFS, len(seed))
	for name, data := range seed {
		mapFS[dir+"/"+name] = &stdfstest.MapFile{Data: data}
	}
	return mapFS
}

// TestStdFileSystem_Conformance runs the read-only conformance suite
// over a MapFS: the adapter only implements the primitives, so this
// also verifies the generic emulations from a minimal backend.
func TestStdFileSystem_Conformance(t *testing.T) {
	seed := fstest.DefaultSeed()
	stdFS := fs.NewStdFileSystem(seedMapFS("seed", seed), "conformance")
	require.Equal(t, "stdfs://conformance", stdFS.Prefix())
	require.Equal(t, "conformance", stdFS.ID())

	fstest.RunConformance(t, stdFS, fstest.Config{
		Name:    "io/fs file system",
		Prefix:  "stdfs://conformance",
		TestDir: "/seed",
		Seed:    seed,
	})
}

// TestStdFileSystem_Embed makes an embed.FS usable through the File API.
func TestStdFileSystem_Embed(t *testing.T) {
	stdFS := fs.NewStdFileSystemAndRegister(embeddedDocs, "embedded-docs")
	t.Cleanup(func() { assert.NoError(t, stdFS.Close()) })

	dir := fs.File("stdfs://embedded-docs/docs")
	require.True(t, dir.IsDir(), "embedded directory")
	files, err := dir.ListDirMax(t.Context(), -1, "*.md")
	require.NoError(t, err)
	assert.NotEmpty(t, files, "embedded markdown files")

	data, err := dir.Join("MIGRATION_v1.md").ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Contains(t, data, "# Migrating to go-fs v1.0")

	// Read-only: writes are rejected by the fs package
	err = dir.Join("new.md").WriteAllString(t.Context(), "x")
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)

	// Closed: unregistered and every method fails
	require.NoError(t, stdFS.Close())
	assert.False(t, fs.IsRegistered(stdFS))
	_, err = stdFS.Stat("/docs")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// TestStdFileSystem_DirFS exposes a local directory as a sandboxed
// read-only file system via os.DirFS.
func TestStdFileSystem_DirFS(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() { _ = os.RemoveAll(tempDir.LocalPath()) })
	require.NoError(t, tempDir.Join("a.txt").WriteAllString(t.Context(), "a"))

	stdFS := fs.NewStdFileSystemAndRegister(os.DirFS(tempDir.LocalPath()), "")
	t.Cleanup(func() { assert.NoError(t, stdFS.Close()) })

	root := stdFS.RootDir()
	data, err := root.Join("a.txt").ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "a", data)

	// Paths can't escape the directory
	_, err = root.Join("..", "..", "etc", "passwd").Stat()
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
