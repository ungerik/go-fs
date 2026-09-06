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

// newOverlayTestFS returns a registered overlay of a mem upper layer
// on a read-only MapFS base containing "/base.txt".
func newOverlayTestFS(t *testing.T) *fs.OverlayFileSystem {
	t.Helper()
	base := fs.NewStdFileSystemAndRegister(stdfstest.MapFS{
		"base.txt":      {Data: []byte("base")},
		"basedir/x.txt": {Data: []byte("x")},
	}, "")
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	overlay, err := fs.NewOverlayFileSystemAndRegister(base, upper, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = overlay.Close() })
	assert.Equal(t, fs.FileSystem(base), overlay.Base())
	assert.Equal(t, fs.WriteFileSystem(upper), overlay.Upper())
	return overlay
}

// TestOverlayFileSystem_NilLayers verifies that a missing layer is
// rejected at construction instead of panicking on the first operation.
func TestOverlayFileSystem_NilLayers(t *testing.T) {
	upper, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	_, err = fs.NewOverlayFileSystem(nil, upper, "")
	assert.Error(t, err, "nil base layer")
	_, err = fs.NewOverlayFileSystem(upper, nil, "")
	assert.Error(t, err, "nil upper layer")
	_, err = fs.NewOverlayFileSystemAndRegister(nil, upper, "")
	assert.Error(t, err, "nil base layer must not be registered")
}

// TestOverlayFileSystem_Closed verifies that every method of a closed
// overlay reports fs.ErrFileSystemClosed instead of writing through to
// the still-open upper layer.
func TestOverlayFileSystem_Closed(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)
	require.NoError(t, overlay.Close())
	require.NoError(t, overlay.Close(), "Close must be idempotent")

	_, err := overlay.Stat("/base.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.Exists("/base.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = overlay.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.OpenReader("/base.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.ReadAll(ctx, "/base.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.OpenWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.OpenAppendWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = overlay.OpenReadWriter("/a.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.WriteAll(ctx, "/a.txt", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.Append(ctx, "/a.txt", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.Truncate("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.Touch("/a.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.MakeAllDirs("/dir/sub", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.Remove("/base.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, overlay.RemoveAll(ctx, "/base.txt"), fs.ErrFileSystemClosed)

	assert.False(t, overlay.Upper().RootDir().Join("a.txt").Exists(), "nothing was written to the upper layer")
}

// TestOverlayFileSystem_EmptyPath verifies that an empty path is
// rejected with fs.ErrEmptyPath instead of being cleaned to the root,
// which would make a typo operate on the whole overlay.
func TestOverlayFileSystem_EmptyPath(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)

	_, err := overlay.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	// Exists reports false without an error, because ErrEmptyPath
	// wraps os.ErrNotExist
	exists, err := overlay.Exists("")
	assert.NoError(t, err)
	assert.False(t, exists)
	err = overlay.ListDir(ctx, "", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = overlay.OpenReader("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = overlay.ReadAll(ctx, "")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = overlay.OpenWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = overlay.OpenAppendWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = overlay.OpenReadWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.WriteAll(ctx, "", []byte("x"), 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.Append(ctx, "", []byte("x"), 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.Truncate("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.MakeAllDirs("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, overlay.RemoveAll(ctx, ""), fs.ErrEmptyPath)
}

// TestOverlayFileSystem_IsDirectoryErrors verifies that operations
// which need a file report a directory as such instead of corrupting
// it by copying it up or opening it as a stream.
func TestOverlayFileSystem_IsDirectoryErrors(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)
	require.NoError(t, overlay.MakeDir("/dir", 0))

	_, err := overlay.OpenReader("/dir")
	assert.ErrorAs(t, err, new(fs.ErrIsDirectory))
	_, err = overlay.ReadAll(ctx, "/dir")
	assert.ErrorAs(t, err, new(fs.ErrIsDirectory))
	// Copy up of a base-only directory must fail instead of
	// materializing the directory as a file in the upper layer
	assert.ErrorAs(t, overlay.Truncate("/basedir", 1), new(fs.ErrIsDirectory))
	assert.ErrorAs(t, overlay.Touch("/basedir", 0), new(fs.ErrIsDirectory))
	err = overlay.ListDir(ctx, "/base.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorAs(t, err, new(fs.ErrIsNotDirectory), "listing a file")
	assert.ErrorAs(t, overlay.MakeAllDirs("/base.txt", 0), new(fs.ErrIsNotDirectory))

	// The root can't be removed recursively either
	assert.Error(t, overlay.RemoveAll(ctx, "/"))
}
