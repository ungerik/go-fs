package fs_test

import (
	"os"
	"testing"
	stdfstest "testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// TestOverlayFileSystem_Conformance runs the full suite over an overlay
// of a mem upper layer on a read-only MapFS base that has content
// outside the suite's directory.
func TestOverlayFileSystem_Conformance(t *testing.T) {
	base := fs.NewStdFileSystemAndRegister(stdfstest.MapFS{
		"base.txt":            {Data: []byte("base")},
		"conformance/.keep":   {Data: nil},
		"other/base-only.txt": {Data: []byte("x")},
	}, "")
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	overlay, err := fs.NewOverlayFileSystem(base, upper, "conformance")
	require.NoError(t, err)
	// The suite needs an empty TestDir: hide the base file in it
	require.NoError(t, overlay.Remove("/conformance/.keep"))

	fstest.RunConformance(t, overlay, fstest.Config{
		Name:    "overlay file system",
		Prefix:  "overlay://conformance",
		TestDir: "/conformance",
	})
}

// TestOverlayFileSystem_Layers verifies the layer semantics: read
// through, shadowing, whiteouts, copy up and the union listing.
func TestOverlayFileSystem_Layers(t *testing.T) {
	ctx := t.Context()
	base := fs.NewStdFileSystemAndRegister(stdfstest.MapFS{
		"docs/readme.md":  {Data: []byte("base readme")},
		"docs/notes.txt":  {Data: []byte("notes")},
		"docs/old/a.txt":  {Data: []byte("a")},
		"config.json":     {Data: []byte(`{}`)},
		"data/report.csv": {Data: []byte("1,2")},
	}, "")
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	readOnly, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = readOnly.Close() })
	readOnly.SetReadOnly(true)
	_, err = fs.NewOverlayFileSystem(base, readOnly, "")
	require.Error(t, err, "upper layer must be writable")

	overlay, err := fs.NewOverlayFileSystemAndRegister(base, upper, "layers")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, overlay.Close()) })
	root := fs.File("overlay://layers/")

	// Read through to the base
	data, err := root.Join("docs", "readme.md").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "base readme", data)
	assert.True(t, root.Join("docs", "old").IsDir(), "base directory")

	// A write shadows the base file and creates the parent in the upper layer
	require.NoError(t, root.Join("docs", "readme.md").WriteAllString(ctx, "upper readme"))
	data, err = root.Join("docs", "readme.md").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "upper readme", data)
	assert.True(t, upper.RootDir().Join("docs", "readme.md").Exists(), "written to the upper layer")
	assert.Equal(t, "base readme", string(mustRead(t, base.RootDir().Join("docs", "readme.md"))), "base untouched")

	// Listing is the union with the upper entry shadowing the base entry
	require.NoError(t, root.Join("docs", "new.txt").WriteAllString(ctx, "new"))
	names, err := root.Join("docs").ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{
		root.Join("docs", "new.txt"),
		root.Join("docs", "notes.txt"),
		root.Join("docs", "old"),
		root.Join("docs", "readme.md"),
	}, names)

	// Append copies the base file up before appending
	require.NoError(t, root.Join("docs", "notes.txt").AppendString(ctx, " more"))
	data, err = root.Join("docs", "notes.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "notes more", data)

	// Removing a base file hides it, removing a base directory hides its tree
	require.NoError(t, root.Join("config.json").Remove())
	assert.False(t, root.Join("config.json").Exists())
	_, err = root.Join("config.json").Stat()
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Error(t, root.Join("docs", "old").Remove(), "non-empty directory")
	require.NoError(t, root.Join("docs", "old").RemoveRecursive(ctx))
	assert.False(t, root.Join("docs", "old", "a.txt").Exists(), "hidden below a removed directory")
	names, err = root.Join("docs").ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.NotContains(t, names, root.Join("docs", "old"))
	assert.True(t, base.RootDir().Join("config.json").Exists(), "base untouched")

	// Writing to a hidden path makes it visible again
	require.NoError(t, root.Join("config.json").WriteAllString(ctx, "{new}"))
	data, err = root.Join("config.json").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "{new}", data)

	// MakeDir on an existing base directory reports os.ErrExist, MakeAllDirs is fine
	assert.ErrorIs(t, overlay.MakeDir("/data", 0), os.ErrExist)
	require.NoError(t, root.Join("data").MakeAllDirs())
	require.NoError(t, root.Join("data", "sub").MakeDir())
	assert.True(t, upper.RootDir().Join("data", "sub").IsDir())

	// The root can't be removed
	assert.Error(t, overlay.Remove("/"))
}

func mustRead(t *testing.T, f fs.File) []byte {
	t.Helper()
	data, err := f.ReadAll(t.Context())
	require.NoError(t, err)
	return data
}
