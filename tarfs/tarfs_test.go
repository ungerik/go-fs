package tarfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// tarEntry is an entry of an archive written by makeTar.
type tarEntry struct {
	header tar.Header
	data   string
}

// regular returns a regular file entry.
func regular(name, data string) tarEntry {
	return tarEntry{header: tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644}, data: data}
}

// makeTar returns an uncompressed tar archive with the entries,
// written directly with archive/tar like a foreign tool would.
func makeTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		header := e.header
		header.Size = int64(len(e.data))
		require.NoError(t, tw.WriteHeader(&header))
		_, err := io.WriteString(tw, e.data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// newMemFS returns a registered MemFileSystem that is closed with the test.
func newMemFS(t *testing.T, separator string) *fs.MemFileSystem {
	t.Helper()
	memFS, err := fs.NewMemFileSystem(&fs.MemFileSystemOptions{Separator: separator})
	require.NoError(t, err)
	require.NoError(t, fs.Register(memFS))
	t.Cleanup(func() { _ = memFS.Close() })
	return memFS
}

// openReader returns a registered Reader for the archive data
// that is closed with the test.
func openReader(t *testing.T, name string, data []byte) *Reader {
	t.Helper()
	reader, err := NewReader(fs.NewMemFile(name, data), "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(reader))
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

// TestReader_DuplicateEntries verifies that a later entry replaces an
// earlier one with the same path. Stat used to report the size of the
// first entry while the content was read at the offset of the last one,
// which returned "a muc" for the archive below.
func TestReader_DuplicateEntries(t *testing.T) {
	ctx := t.Context()
	reader := openReader(t, "dup.tar", makeTar(t,
		regular("a.txt", "short"),
		regular("a.txt", "a much longer replacement"),
		regular("b.txt", "b"),
	))
	f := reader.RootDir().Join("a.txt")
	assert.Equal(t, int64(len("a much longer replacement")), f.Size())
	data, err := f.ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "a much longer replacement", data)

	r, err := f.OpenReader()
	require.NoError(t, err)
	streamed, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, "a much longer replacement", string(streamed))

	names, err := reader.RootDir().ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{reader.RootDir().Join("a.txt"), reader.RootDir().Join("b.txt")}, names, "no duplicate listing")
}

// TestReader_Concurrent verifies that the files of a Reader can be
// listed and read concurrently, run it with -race.
func TestReader_Concurrent(t *testing.T) {
	reader := openReader(t, "concurrent.tar", makeTar(t,
		regular("a.txt", "a"),
		regular("dir/b.txt", "bb"),
	))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			names, err := reader.RootDir().ListDirRecursiveMax(t.Context(), -1)
			assert.NoError(t, err)
			assert.Len(t, names, 2)
			data, err := reader.RootDir().Join("dir", "b.txt").ReadAllString(t.Context())
			assert.NoError(t, err)
			assert.Equal(t, "bb", data)
			r, err := reader.RootDir().Join("a.txt").OpenReader()
			if assert.NoError(t, err) {
				data, err := io.ReadAll(r)
				assert.NoError(t, err)
				assert.Equal(t, "a", string(data))
				assert.NoError(t, r.Close())
			}
		})
	}
	wg.Wait()
}

// TestReader_UnsafeNames verifies that entry names can't address a path
// outside of the archive. `..\..\evil.txt` passed the old check for "/"
// separated ".." elements, and CopyRecursive wrote it outside of the
// destination on a file system with a backslash separator.
func TestReader_UnsafeNames(t *testing.T) {
	ctx := t.Context()
	reader := openReader(t, "unsafe.tar", makeTar(t,
		regular(`..\..\evil.txt`, "pwned"),
		regular("../up.txt", "up"),
		regular("/abs/x.txt", "abs"),
		regular(`dir\file.txt`, "backslash"),
	))

	memFS := newMemFS(t, `\`)
	dest := memFS.RootDir().Join("a", "extract")
	require.NoError(t, dest.MakeAllDirs())
	require.NoError(t, fs.CopyRecursive(ctx, reader.RootDir(), dest))

	var extracted []string
	err := memFS.RootDir().ListDirRecursive(ctx, func(f fs.File) error {
		extracted = append(extracted, f.Path())
		return nil
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		`\a\extract\evil.txt`,
		`\a\extract\up.txt`,
		`\a\extract\abs\x.txt`,
		`\a\extract\dir\file.txt`,
	}, extracted, "every entry is extracted below the destination")
}

// TestReader_LenientIndex verifies that one unusual entry doesn't make the
// whole archive unreadable. The reader used to fail for a "../" entry and
// for a file that is also used as directory, while zipfs read such
// archives. Now both read them with the same rules.
func TestReader_LenientIndex(t *testing.T) {
	ctx := t.Context()
	reader := openReader(t, "lenient.tar", makeTar(t,
		regular("good.txt", "ok"),
		regular("../bad.txt", "x"),
		regular("a", "file"),
		regular("a/b", "below the file"),
	))
	root := reader.RootDir()
	data, err := root.Join("good.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ok", data)
	assert.True(t, root.Join("bad.txt").Exists(), "a .. element is removed")
	assert.True(t, root.Join("a").IsDir(), "the later entry a/b replaces the file a with a directory")
	data, err = root.Join("a", "b").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "below the file", data)
}

// TestReader_GzipByContent verifies that a gzip compressed archive is
// recognized by its magic bytes, not only by a .gz or .tgz file name.
func TestReader_GzipByContent(t *testing.T) {
	ctx := t.Context()
	plain := makeTar(t, regular("a.txt", "content"))
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(plain)
	require.NoError(t, err)
	require.NoError(t, gz.Close())

	for name, data := range map[string][]byte{
		"compressed-without-extension.tar": compressed.Bytes(),
		"compressed.tgz":                   compressed.Bytes(),
		"plain-with-extension.tar.gz":      plain,
	} {
		t.Run(name, func(t *testing.T) {
			reader := openReader(t, name, data)
			content, err := reader.RootDir().Join("a.txt").ReadAllString(ctx)
			require.NoError(t, err)
			assert.Equal(t, "content", content)
		})
	}

	empty := openReader(t, "empty.tar", nil)
	names, err := empty.RootDir().ListDirMax(ctx, -1)
	require.NoError(t, err)
	assert.Empty(t, names, "an empty file is an empty archive")
}

// TestReader_Links verifies that a hard link is read as a regular file
// with the content of its target, and that symbolic links are not exposed.
func TestReader_Links(t *testing.T) {
	ctx := t.Context()
	reader := openReader(t, "links.tar", makeTar(t,
		regular("target.txt", "shared content"),
		tarEntry{header: tar.Header{Typeflag: tar.TypeLink, Name: "hard.txt", Linkname: "target.txt"}},
		tarEntry{header: tar.Header{Typeflag: tar.TypeLink, Name: "dangling.txt", Linkname: "missing.txt"}},
		tarEntry{header: tar.Header{Typeflag: tar.TypeSymlink, Name: "soft.txt", Linkname: "target.txt"}},
	))
	root := reader.RootDir()
	data, err := root.Join("hard.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "shared content", data)
	assert.Equal(t, int64(len("shared content")), root.Join("hard.txt").Size())
	assert.False(t, root.Join("dangling.txt").Exists(), "a hard link without target is skipped")
	assert.False(t, root.Join("soft.txt").Exists(), "symbolic links are not exposed")
}

// TestReader_HeaderMetadata verifies that the permissions and the
// modification time of an entry are read from its header.
func TestReader_HeaderMetadata(t *testing.T) {
	modified := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	exec := regular("run.sh", "#!/bin/sh")
	exec.header.Mode = 0o750
	exec.header.ModTime = modified
	noMode := regular("nomode.txt", "x")
	noMode.header.Mode = 0
	reader := openReader(t, "meta.tar", makeTar(t,
		exec,
		noMode,
		tarEntry{header: tar.Header{Typeflag: tar.TypeDir, Name: "dir/", Mode: 0o700, ModTime: modified}},
	))
	info, err := reader.Stat("/run.sh")
	require.NoError(t, err)
	assert.Equal(t, fs.Permissions(0o750), info.Permissions)
	assert.True(t, modified.Equal(info.Modified), "Modified %s", info.Modified)
	header, ok := info.Sys.(*tar.Header)
	require.True(t, ok, "Sys is the tar header")
	assert.Equal(t, "run.sh", header.Name)

	info, err = reader.Stat("/nomode.txt")
	require.NoError(t, err)
	assert.Equal(t, DefaultPermissions, info.Permissions, "default for entries without permission bits")

	info, err = reader.Stat("/dir")
	require.NoError(t, err)
	assert.Equal(t, fs.Permissions(0o700), info.Permissions)
}

// TestWriter_WriteOnce verifies that a path can be written only once.
// A second write of the same path used to create a duplicate entry.
func TestWriter_WriteOnce(t *testing.T) {
	ctx := t.Context()
	tarFile := newMemFS(t, "/").RootDir().Join("once.tar")
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)

	require.NoError(t, writer.WriteAll(ctx, "/a.txt", []byte("v1"), 0))
	err = writer.WriteAll(ctx, "/a.txt", []byte("version two"), 0)
	assert.ErrorIs(t, err, os.ErrExist, "WriteAll of a written file")
	_, err = writer.OpenWriter("/a.txt", 0)
	assert.ErrorIs(t, err, os.ErrExist, "OpenWriter of a written file")
	assert.ErrorIs(t, writer.Touch("/a.txt", 0), os.ErrExist, "Touch of a written file")

	require.NoError(t, writer.MakeDir("/d", 0))
	assert.ErrorIs(t, writer.MakeDir("/d", 0), os.ErrExist, "MakeDir of a written directory")
	assert.ErrorIs(t, writer.MakeDir("/", 0), os.ErrExist, "MakeDir of the root")
	err = writer.WriteAll(ctx, "/d", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "a file over a directory")
	err = writer.WriteAll(ctx, "/a.txt/below", []byte("x"), 0)
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "a file below a file")

	require.NoError(t, writer.WriteAll(ctx, "/implied/x.txt", []byte("x"), 0))
	assert.ErrorIs(t, writer.MakeDir("/implied", 0), os.ErrExist, "MakeDir of an implied directory")

	err = writer.WriteAll(ctx, `/back\slash.txt`, []byte("x"), 0)
	assert.ErrorIs(t, err, os.ErrInvalid, "a backslash would be a separator when read")

	// Two writers for the same path: the second Close fails
	w1, err := writer.OpenWriter("/race.txt", 0)
	require.NoError(t, err)
	w2, err := writer.OpenWriter("/race.txt", 0)
	require.NoError(t, err)
	_, err = io.WriteString(w1, "first")
	require.NoError(t, err)
	require.NoError(t, w1.Close())
	assert.ErrorIs(t, w2.Close(), os.ErrExist)

	require.NoError(t, writer.Close())

	reader := openReader(t, "once.tar", readAll(t, tarFile))
	data, err := reader.RootDir().Join("a.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "v1", data)
	data, err = reader.RootDir().Join("race.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "first", data)
	assertEntryNames(t, tarFile, "a.txt", "d/", "implied/x.txt", "race.txt")
}

// TestWriter_Abort verifies that an aborted entry writer writes no entry,
// and that writes after Close fail instead of being dropped.
func TestWriter_Abort(t *testing.T) {
	ctx := t.Context()
	tarFile := newMemFS(t, "/").RootDir().Join("abort.tar")
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)

	w, err := writer.OpenWriter("/aborted.txt", 0)
	require.NoError(t, err)
	aborter, ok := w.(fs.WriteAborter)
	require.True(t, ok, "entry writers implement fs.WriteAborter")
	_, err = io.WriteString(w, "partial")
	require.NoError(t, err)
	require.NoError(t, aborter.Abort())
	require.NoError(t, w.Close(), "Close after Abort is a no-op")

	w, err = writer.OpenWriter("/kept.txt", 0)
	require.NoError(t, err)
	_, err = io.WriteString(w, "kept")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	_, err = io.WriteString(w, "lost")
	assert.ErrorIs(t, err, os.ErrClosed, "Write after Close")

	// The aborted path is still free
	require.NoError(t, writer.WriteAll(ctx, "/aborted.txt", []byte("later"), 0))
	require.NoError(t, writer.Close())

	assertEntryNames(t, tarFile, "kept.txt", "aborted.txt")
}

// TestWriter_MakeAllDirs verifies the native MakeAllDirs of the write-only
// file system. The emulation of the fs package needs Stat, so it failed.
func TestWriter_MakeAllDirs(t *testing.T) {
	tarFile := newMemFS(t, "/").RootDir().Join("dirs.tar")
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(writer))

	require.NoError(t, writer.RootDir().Join("a", "b", "c").MakeAllDirs())
	require.NoError(t, writer.RootDir().Join("a", "b", "d").MakeAllDirs(), "existing parents are no error")
	require.NoError(t, writer.RootDir().Join("a").MakeAllDirs(), "an existing directory is no error")
	require.NoError(t, writer.RootDir().MakeAllDirs(), "the root is no error")
	require.NoError(t, writer.RootDir().Join("a", "f.txt").Touch())
	assert.ErrorIs(t, writer.RootDir().Join("a", "f.txt", "x").MakeAllDirs(), fs.ErrIsNotDirectory{})
	require.NoError(t, writer.Close())

	assertEntryNames(t, tarFile, "a/", "a/b/", "a/b/c/", "a/b/d/", "a/f.txt")
}

// TestWriter_ValidateName verifies that the writer rejects the names that
// a tar archive can't hold, also with ValidateName, so a copy into the
// archive fails before it writes anything.
// Regression: a NUL character failed in archive/tar without os.ErrInvalid.
func TestWriter_ValidateName(t *testing.T) {
	ctx := t.Context()
	memFS := newMemFS(t, "/")
	src := memFS.RootDir().Join("src")
	require.NoError(t, src.MakeDir())
	for _, name := range []string{"a.txt", "b\x00c.txt", "z.txt"} {
		require.NoError(t, src.Join(name).WriteAllString(ctx, name))
	}
	tarFile := memFS.RootDir().Join("copy.tar")
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(writer))

	assert.NoError(t, writer.ValidateName("a.txt"))
	assert.NoError(t, writer.ValidateName("Größe: what?.txt"), "only Windows can't extract it")
	for _, name := range []string{`a\b.txt`, "a\x00b", "\xff.txt"} {
		assert.ErrorIs(t, writer.ValidateName(name), os.ErrInvalid, "%q", name)
		assert.ErrorIs(t, writer.Touch("/"+name, 0), os.ErrInvalid, "Touch(%q)", name)
		assert.ErrorIs(t, writer.MakeAllDirs("/dir/"+name, 0), os.ErrInvalid, "MakeAllDirs(%q)", name)
	}

	err = fs.CopyRecursive(ctx, src, writer.RootDir().Join("copy"))
	assert.ErrorIs(t, err, os.ErrInvalid)
	assert.ErrorContains(t, err, "NUL character", "the error tells why")
	assert.ErrorIs(t, fs.Move(ctx, src, writer.RootDir().Join("moved")), os.ErrInvalid)
	assert.True(t, src.Join("a.txt").Exists(), "the source of a failed move is kept")
	assert.ErrorIs(t, fs.CopyFile(ctx, src.Join("a.txt"), writer.RootDir().Join("dir\xff", "a.txt")), os.ErrInvalid)
	require.NoError(t, writer.Close())
	assertEntryNames(t, tarFile)
}

// TestWriter_EntryMetadata verifies that written entries get the current
// time and the passed permissions, or the defaults.
func TestWriter_EntryMetadata(t *testing.T) {
	ctx := t.Context()
	tarFile := newMemFS(t, "/").RootDir().Join("meta.tar")
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)
	before := time.Now().Add(-2 * time.Second)
	require.NoError(t, writer.WriteAll(ctx, "/exec.sh", []byte("x"), 0o750))
	require.NoError(t, writer.WriteAll(ctx, "/default.txt", []byte("x"), 0))
	require.NoError(t, writer.MakeDir("/private", 0o700))
	require.NoError(t, writer.Close())

	reader := openReader(t, "meta.tar", readAll(t, tarFile))
	for path, perm := range map[string]fs.Permissions{
		"/exec.sh":     0o750,
		"/default.txt": DefaultPermissions,
		"/private":     0o700,
	} {
		info, err := reader.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, perm, info.Permissions, "permissions of %s", path)
		assert.True(t, info.Modified.After(before), "modification time of %s is the time of writing: %s", path, info.Modified)
	}
}

// readAll returns the content of a file.
func readAll(t *testing.T, file fs.File) []byte {
	t.Helper()
	data, err := file.ReadAll(t.Context())
	require.NoError(t, err)
	return data
}

// assertEntryNames checks the entry names of a tar archive in their order.
func assertEntryNames(t *testing.T, tarFile fs.File, want ...string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(readAll(t, tarFile)))
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, header.Name)
	}
	assert.Equal(t, want, names, "entries of %s", tarFile.Name())
}

// TestConformance runs the suite over a writer, then over readers of
// the written plain and gzip compressed archives.
func TestConformance(t *testing.T) {
	tempDir := fs.MustMakeTempDir()
	t.Cleanup(func() { _ = tempDir.RemoveRecursive(context.Background()) })

	for _, name := range []string{"conformance.tar", "conformance.tar.gz"} {
		t.Run(name, func(t *testing.T) {
			tarFile := tempDir.Join(name)

			t.Run("Writer", func(t *testing.T) {
				writer, err := NewWriter(tarFile, "")
				require.NoError(t, err, "NewWriter")
				require.NoError(t, fs.Register(writer))
				// A Reader and a Writer of an archive must report their
				// id in the same form, so that neither can be mistaken
				// for the other when file systems are compared by id.
				require.Equal(t, writer.Prefix(), writer.ID(), "ID is the unique prefix of the archive")
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
				reader, err := NewReader(tarFile, "")
				require.NoError(t, err, "NewReader")
				require.NoError(t, fs.Register(reader))
				require.Equal(t, reader.Prefix(), reader.ID(), "ID is the unique prefix of the archive")
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
	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err, "NewWriter")
	require.NoError(t, fs.Register(writer))
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

	writer, err := NewWriter(tarFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(writer))
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

	reader, err := NewReader(tarFile, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(reader))
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
