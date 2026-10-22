package fs_test

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
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
	base := fs.NewStdFileSystem(stdfstest.MapFS{
		"base.txt":            {Data: []byte("base")},
		"conformance/.keep":   {Data: nil},
		"other/base-only.txt": {Data: []byte("x")},
	}, "")
	require.NoError(t, fs.Register(base))
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.RegisteredTestMemFileSystem(nil)
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
	base := fs.NewStdFileSystem(stdfstest.MapFS{
		"docs/readme.md":  {Data: []byte("base readme")},
		"docs/notes.txt":  {Data: []byte("notes")},
		"docs/old/a.txt":  {Data: []byte("a")},
		"config.json":     {Data: []byte(`{}`)},
		"data/report.csv": {Data: []byte("1,2")},
	}, "")
	require.NoError(t, fs.Register(base))
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	readOnly, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readOnly.Close() })
	readOnly.SetReadOnly(true)
	_, err = fs.NewOverlayFileSystem(base, readOnly, "")
	require.Error(t, err, "upper layer must be writable")

	overlay, err := fs.NewOverlayFileSystem(base, upper, "layers")
	require.NoError(t, err)
	require.NoError(t, fs.Register(overlay))
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
	assert.ErrorIs(t, root.Join("docs", "old").Remove(), fs.ErrDirectoryNotEmpty{}, "non-empty directory")
	assert.ErrorIs(t, overlay.Remove("/docs/old"), fs.ErrDirectoryNotEmpty{}, "non-empty directory")
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
	assert.ErrorIs(t, overlay.Remove("/"), os.ErrInvalid, "the root can't be removed")
}

func mustRead(t *testing.T, f fs.File) []byte {
	t.Helper()
	data, err := f.ReadAll(t.Context())
	require.NoError(t, err)
	return data
}

// newOverlayTestFS returns a registered overlay of a mem upper layer
// on a read-only MapFS base containing "/base.txt" and "/basedir/x.txt".
func newOverlayTestFS(t *testing.T) *fs.OverlayFileSystem {
	t.Helper()
	return newOverlayOfMapFS(t, stdfstest.MapFS{
		"base.txt":      {Data: []byte("base")},
		"basedir/x.txt": {Data: []byte("x")},
	})
}

// newOverlayOfMapFS returns a registered overlay of
// a mem upper layer on a read-only base with the files of mapFS.
func newOverlayOfMapFS(t *testing.T, mapFS stdfstest.MapFS) *fs.OverlayFileSystem {
	t.Helper()
	base := fs.NewStdFileSystem(mapFS, "")
	require.NoError(t, fs.Register(base))
	t.Cleanup(func() { _ = base.Close() })
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(overlay))
	t.Cleanup(func() { _ = overlay.Close() })
	assert.Equal(t, fs.FileSystem(base), overlay.Base())
	assert.Equal(t, fs.WriteFileSystem(upper), overlay.Upper())
	return overlay
}

// TestOverlayFileSystem_NilLayers verifies that a missing layer is
// rejected at construction instead of panicking on the first operation.
func TestOverlayFileSystem_NilLayers(t *testing.T) {
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })

	_, err = fs.NewOverlayFileSystem(nil, upper, "")
	assert.Error(t, err, "nil base layer")
	_, err = fs.NewOverlayFileSystem(upper, nil, "")
	assert.Error(t, err, "nil upper layer")
	err = fs.NewOverlayFileSystemAndRegister(nil, upper, "nil-base")
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
	assert.ErrorIs(t, overlay.Truncate(t.Context(), "/a.txt", 0), fs.ErrFileSystemClosed)
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
	assert.ErrorIs(t, overlay.Truncate(t.Context(), "", 0), fs.ErrEmptyPath)
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
	assert.ErrorAs(t, overlay.Truncate(t.Context(), "/basedir", 1), new(fs.ErrIsDirectory))
	assert.ErrorAs(t, overlay.Touch("/basedir", 0), new(fs.ErrIsDirectory))
	err = overlay.ListDir(ctx, "/base.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorAs(t, err, new(fs.ErrIsNotDirectory), "listing a file")
	assert.ErrorAs(t, overlay.MakeAllDirs("/base.txt", 0), new(fs.ErrIsNotDirectory))

	// The root can't be removed recursively either
	assert.ErrorIs(t, overlay.RemoveAll(ctx, "/"), os.ErrInvalid, "the root can't be removed")
}

// TestOverlayFileSystem_RecreateRemovedDir verifies that a directory
// created at the path of a removed base directory is opaque. Creating it
// used to drop the whiteouts of the path and its ancestors, which brought
// back the removed base entries.
func TestOverlayFileSystem_RecreateRemovedDir(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayOfMapFS(t, stdfstest.MapFS{
		"made/old.txt":     {Data: []byte("old")},
		"all/old.txt":      {Data: []byte("old")},
		"all/sub/old.txt":  {Data: []byte("old")},
		"emptied/x.txt":    {Data: []byte("x")},
		"emptied/keep.txt": {Data: []byte("keep")},
	})
	listNames := func(dirPath string) (names []string) {
		t.Helper()
		err := overlay.ListDir(ctx, dirPath, nil, func(info *fs.FileInfo) error {
			names = append(names, info.Name)
			return nil
		})
		require.NoError(t, err, "ListDir(%q)", dirPath)
		return names
	}
	exists := func(filePath string) bool {
		_, err := overlay.Stat(filePath)
		return err == nil
	}

	// MakeDir after RemoveAll
	require.NoError(t, overlay.RemoveAll(ctx, "/made"))
	require.NoError(t, overlay.MakeDir("/made", 0))
	assert.False(t, exists("/made/old.txt"), "the removed base file must stay hidden")
	assert.Empty(t, listNames("/made"), "the recreated directory must be empty")

	// MakeAllDirs below a removed directory
	require.NoError(t, overlay.RemoveAll(ctx, "/all"))
	require.NoError(t, overlay.MakeAllDirs("/all/sub/deeper", 0))
	assert.False(t, exists("/all/old.txt"), "the removed base file must stay hidden")
	assert.False(t, exists("/all/sub/old.txt"), "the removed base file must stay hidden")
	assert.Equal(t, []string{"sub"}, listNames("/all"))
	assert.Equal(t, []string{"deeper"}, listNames("/all/sub"))

	// A directory that was emptied and removed entry by entry
	require.NoError(t, overlay.Remove("/emptied/x.txt"))
	require.NoError(t, overlay.Remove("/emptied/keep.txt"))
	require.NoError(t, overlay.Remove("/emptied"))
	require.NoError(t, overlay.MakeDir("/emptied", 0))
	assert.Empty(t, listNames("/emptied"))

	// Writing into the recreated directory works
	require.NoError(t, overlay.WriteAll(ctx, "/made/new.txt", []byte("new"), 0))
	assert.Equal(t, []string{"new.txt"}, listNames("/made"))
}

// TestOverlayFileSystem_DirOverRemovedFile verifies that a directory created
// at the path of a removed base file can be listed. The base file was not
// hidden anymore, so ListDir failed listing it as a directory of the base.
func TestOverlayFileSystem_DirOverRemovedFile(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)

	require.NoError(t, overlay.Remove("/base.txt"))
	require.NoError(t, overlay.MakeDir("/base.txt", 0))
	info, err := overlay.Stat("/base.txt")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	var names []string
	listNames := func(info *fs.FileInfo) error { names = append(names, info.Name); return nil }
	require.NoError(t, overlay.ListDir(ctx, "/base.txt", nil, listNames))
	assert.Empty(t, names)

	require.NoError(t, overlay.WriteAll(ctx, "/base.txt/inner.txt", []byte("inner"), 0))
	require.NoError(t, overlay.ListDir(ctx, "/base.txt", nil, listNames))
	assert.Equal(t, []string{"inner.txt"}, names)
}

// TestOverlayFileSystem_WriteOnDirectory verifies that writing a file over
// a directory of either layer is an fs.ErrIsDirectory. Writes to a directory
// that only exists in the base created a file in the upper layer that hid
// the directory and its content.
func TestOverlayFileSystem_WriteOnDirectory(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)
	require.NoError(t, overlay.MakeDir("/upperdir", 0))

	for _, dir := range []string{"/basedir", "/upperdir"} {
		assert.ErrorIs(t, overlay.WriteAll(ctx, dir, []byte("data"), 0), fs.ErrIsDirectory{}, "WriteAll(%q)", dir)
		w, err := overlay.OpenWriter(dir, 0)
		if err == nil {
			_ = w.Close()
		}
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenWriter(%q)", dir)
		assert.ErrorIs(t, overlay.Append(ctx, dir, []byte("data"), 0), fs.ErrIsDirectory{}, "Append(%q)", dir)
		aw, err := overlay.OpenAppendWriter(dir, 0)
		if err == nil {
			_ = aw.Close()
		}
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenAppendWriter(%q)", dir)
		rw, err := overlay.OpenReadWriter(dir, 0)
		if err == nil {
			_ = rw.Close()
		}
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenReadWriter(%q)", dir)
		assert.ErrorIs(t, overlay.Truncate(ctx, dir, 0), fs.ErrIsDirectory{}, "Truncate(%q)", dir)
		err = fs.File(overlay.Prefix()+dir).WriteAllString(ctx, "data")
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "File.WriteAll(%q)", dir)

		info, err := overlay.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir, "%s must stay a directory", dir)
	}
	data, err := overlay.ReadAll(ctx, "/basedir/x.txt")
	require.NoError(t, err)
	assert.Equal(t, "x", string(data), "the content of the base directory must stay visible")
	assert.False(t, overlay.Upper().RootDir().Join("basedir").Exists(), "nothing written to the upper layer")

	// Touch of a directory in the upper layer is forwarded to it
	assert.NoError(t, overlay.Touch("/upperdir", 0))
}

// TestOverlayFileSystem_MakeAllDirsBelowFile verifies that MakeAllDirs
// below a file of either layer is an fs.ErrIsNotDirectory. Below a base
// file it created a directory in the upper layer that hid the file.
func TestOverlayFileSystem_MakeAllDirsBelowFile(t *testing.T) {
	ctx := t.Context()
	overlay := newOverlayTestFS(t)
	require.NoError(t, overlay.WriteAll(ctx, "/upper.txt", []byte("upper"), 0))

	for _, file := range []string{"/base.txt", "/upper.txt"} {
		assert.ErrorIs(t, overlay.MakeAllDirs(file+"/sub", 0), fs.ErrIsNotDirectory{}, "MakeAllDirs below %s", file)
		assert.ErrorIs(t, overlay.MakeAllDirs(file+"/sub/deeper", 0), fs.ErrIsNotDirectory{}, "MakeAllDirs below %s", file)
		assert.ErrorIs(t, overlay.MakeDir(file+"/sub", 0), fs.ErrIsNotDirectory{}, "MakeDir below %s", file)
		info, err := overlay.Stat(file)
		require.NoError(t, err)
		assert.False(t, info.IsDir, "%s must stay a file", file)
	}
	data, err := overlay.ReadAll(ctx, "/base.txt")
	require.NoError(t, err)
	assert.Equal(t, "base", string(data))
	assert.False(t, overlay.Upper().RootDir().Join("base.txt").Exists(), "nothing created in the upper layer")
}

// TestOverlayFileSystem_ConcurrentAppend verifies that concurrent appends
// to a base-only file are serialized with its copy up. Every append copied
// the base file up and the later copies overwrote the earlier appends.
func TestOverlayFileSystem_ConcurrentAppend(t *testing.T) {
	ctx := t.Context()
	const writers = 8
	for range 20 {
		overlay := newOverlayOfMapFS(t, stdfstest.MapFS{"log.txt": {Data: []byte("base\n")}})
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				assert.NoError(t, overlay.Append(ctx, "/log.txt", []byte("line\n"), 0))
			})
		}
		wg.Wait()
		data, err := overlay.ReadAll(ctx, "/log.txt")
		require.NoError(t, err)
		require.Equal(t, "base\n"+strings.Repeat("line\n", writers), string(data), "no append may be lost")
	}

	// The same for the append writers
	overlay := newOverlayOfMapFS(t, stdfstest.MapFS{"log.txt": {Data: []byte("base\n")}})
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			w, err := overlay.OpenAppendWriter("/log.txt", 0)
			if !assert.NoError(t, err) {
				return
			}
			_, err = io.WriteString(w, "line\n")
			assert.NoError(t, err)
			assert.NoError(t, w.Close())
		})
	}
	wg.Wait()
	data, err := overlay.ReadAll(ctx, "/log.txt")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "base\nline\n"), "the base content must be copied up once: %q", data)
}

// TestOverlayFileSystem_LayerSeparator verifies that a name containing the
// separator of a layer is rejected. A base with the separator `\` resolved
// "/d/..\x" to "/x", which made a removed base file readable again.
func TestOverlayFileSystem_LayerSeparator(t *testing.T) {
	ctx := t.Context()
	base, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`})
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.WriteAll(ctx, `\removed.txt`, []byte("removed"), 0))
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })
	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	require.NoError(t, overlay.Remove("/removed.txt"))

	_, err = overlay.ReadAll(ctx, `/d/..\removed.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "ReadAll of a name with the separator of the base")
	_, err = overlay.Stat(`/d/..\removed.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "Stat of a name with the separator of the base")
	err = overlay.ListDir(ctx, `/d/..\`, nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrInvalid, "ListDir of a name with the separator of the base")
	assert.ErrorIs(t, overlay.WriteAll(ctx, `/a\b.txt`, []byte("x"), 0), os.ErrInvalid, "WriteAll of a name with the separator of the base")
	_, err = overlay.Stat("/removed.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "the removed file stays hidden")
}

// TestOverlayFileSystem_ListDirRecursiveSymlinkLoop is a regression test:
// the recursive listing followed a link of the base layer to a directory
// of the listed path again and again until the path had too many links,
// because the overlay couldn't tell where the link points to.
// The links of the layers are listed as entries and not followed,
// and a copy fails before it writes anything, because the overlay
// can't create links and another file system never gets them.
func TestOverlayFileSystem_ListDirRecursiveSymlinkLoop(t *testing.T) {
	base, err := fs.RegisteredTestMemFileSystem(nil, fs.NewMemFile("/dir/a.txt", []byte("a")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.CreateSymbolicLink(".", "/dir/loop"))
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })
	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(overlay))
	t.Cleanup(func() { _ = overlay.Close() })
	listFiles := func() []string {
		var files []string
		err := overlay.RootDir().Join("dir").ListDirRecursive(t.Context(), func(file fs.File) error {
			files = append(files, file.Path())
			return nil
		})
		require.NoError(t, err)
		return files
	}

	assert.Equal(t, []string{"/dir/a.txt", "/dir/loop"}, listFiles(), "a link of the base")

	require.NoError(t, upper.MakeAllDirs("/dir/sub", 0))
	require.NoError(t, upper.CreateSymbolicLink("..", "/dir/sub/up"))
	assert.Equal(t, []string{"/dir/a.txt", "/dir/loop", "/dir/sub/up"}, listFiles(), "a relative link of the upper layer")

	// A link to a directory is never copied to another file system
	dest, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dest.Close() })
	err = fs.CopyRecursive(t.Context(), overlay.RootDir().Join("dir"), dest.RootDir().Join("copy"))
	require.ErrorIs(t, err, errors.ErrUnsupported)
	assert.False(t, dest.RootDir().Join("copy").Exists(), "nothing must be copied")

	// The overlay can't create links, the copy fails before it writes anything
	err = fs.CopyRecursive(t.Context(), overlay.RootDir().Join("dir"), overlay.RootDir().Join("copy"))
	require.ErrorIs(t, err, errors.ErrUnsupported)
	assert.False(t, overlay.RootDir().Join("copy").Exists(), "nothing must be copied")
}

// TestOverlayFileSystem_CopyUpPermissions is a regression test: a base file
// was copied up with its exact permissions, so the copy up of a file of a
// read-only base like an embedded file was read-only, and appending to it
// failed in a local upper layer, while the setuid bit of a base file was
// copied up.
func TestOverlayFileSystem_CopyUpPermissions(t *testing.T) {
	newOverlay := func(t *testing.T, upper fs.WriteFileSystem) *fs.OverlayFileSystem {
		base := fs.NewStdFileSystem(stdfstest.MapFS{
			"read-only.txt": {Data: []byte("r"), Mode: 0o444},
			"suid":          {Data: []byte("s"), Mode: os.ModeSetuid | 0o755},
		}, "")
		require.NoError(t, fs.Register(base))
		t.Cleanup(func() { _ = base.Close() })
		overlay, err := fs.NewOverlayFileSystem(base, upper, "")
		require.NoError(t, err)
		return overlay
	}

	t.Run("MemFileSystem upper layer", func(t *testing.T) {
		upper, err := fs.RegisteredTestMemFileSystem(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = upper.Close() })
		overlay := newOverlay(t, upper)

		for name, want := range map[string]fs.Permissions{"read-only.txt": 0o644, "suid": 0o755} {
			require.NoError(t, overlay.Append(t.Context(), "/"+name, []byte("+"), 0))
			info, err := upper.Stat("/" + name)
			require.NoError(t, err)
			assert.Equal(t, want, info.Permissions, "permissions of the copy up of %s", name)
		}
	})
	t.Run("local upper layer", func(t *testing.T) {
		upper, err := fs.NewSubFileSystem(fs.Local, t.TempDir(), "")
		require.NoError(t, err)
		require.NoError(t, fs.Register(upper))
		t.Cleanup(func() { _ = upper.Close() })
		overlay := newOverlay(t, upper)

		require.NoError(t, overlay.Append(t.Context(), "/read-only.txt", []byte("+"), 0))
		data, err := overlay.ReadAll(t.Context(), "/read-only.txt")
		require.NoError(t, err)
		assert.Equal(t, "r+", string(data))
	})
}

// TestOverlayFileSystem_LayerVolume is a regression test: the overlay kept
// the volume of a layer path, so over a MemFileSystem with a volume ListDir
// returned files like overlay://id/C:/dir/a.txt that didn't resolve.
func TestOverlayFileSystem_LayerVolume(t *testing.T) {
	base, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`, Volume: "C:"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	require.NoError(t, base.MakeDir(`C:\dir`, 0))
	require.NoError(t, base.WriteAll(t.Context(), `C:\dir\a.txt`, []byte("a"), 0))
	upper, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })
	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(overlay))
	t.Cleanup(func() { _ = overlay.Close() })
	want := overlay.RootDir().Join("dir", "a.txt")

	files, err := overlay.RootDir().Join("dir").ListDirMax(t.Context(), -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{want}, files)
	data, err := want.ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "a", data, "the listed file must resolve")

	info, err := overlay.Stat("/dir/a.txt")
	require.NoError(t, err)
	assert.Equal(t, want, info.File)
	_, err = overlay.OpenReader("/dir")
	var isDir fs.ErrIsDirectory
	require.ErrorAs(t, err, &isDir)
	errFile, _ := isDir.File()
	assert.Equal(t, overlay.RootDir().Join("dir"), errFile, "errors name the file of the overlay")
}
