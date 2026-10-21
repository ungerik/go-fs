package fs_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

func TestCopyFile_ReturnsCloseError(t *testing.T) {
	m := newMinimalMemFS(t)
	m.closeErr = errors.New("upload failed")

	err := fs.CopyFile(t.Context(), localFile(t, "src.txt", "data"), m.RootDir().Join("a.txt"))
	require.ErrorIs(t, err, m.closeErr, "copying a File")

	err = fs.CopyFile(t.Context(), fs.NewMemFile("src.txt", []byte("data")), m.RootDir().Join("b.txt"))
	require.ErrorIs(t, err, m.closeErr, "copying a MemFile")
}

func TestCopyFile_AbortsOnReadError(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/src.txt", []byte("new")),
		fs.NewMemFile("/dest.txt", []byte("old")),
	)
	m.uploads = true
	m.readErr = errors.New("connection lost")

	err := fs.CopyFile(t.Context(), m.RootDir().Join("src.txt"), m.RootDir().Join("dest.txt"))
	require.ErrorIs(t, err, m.readErr)
	require.Equal(t, int32(1), m.aborts.Load(), "the writer must be aborted")
	require.Zero(t, m.commits.Load(), "nothing must be committed")
	m.readErr = nil
	requireContent(t, m.RootDir().Join("dest.txt"), "old")
}

func TestCopyFile_AbortsOnCanceledContext(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/src.txt", []byte("new")),
		fs.NewMemFile("/dest.txt", []byte("old")),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.uploads = true
	m.onRead = cancel

	err := fs.CopyFile(ctx, m.RootDir().Join("src.txt"), m.RootDir().Join("dest.txt"))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), m.aborts.Load(), "the writer must be aborted")
	require.Zero(t, m.commits.Load(), "nothing must be committed")
	m.onRead = nil
	requireContent(t, m.RootDir().Join("dest.txt"), "old")
}

func TestCopyFileBuf_Buffer(t *testing.T) {
	m := newMinimalMemFS(t, fs.NewMemFile("/src.txt", []byte("some longer content")))
	src := m.RootDir().Join("src.txt")

	require.NoError(t, fs.CopyFileBuf(t.Context(), src, m.RootDir().Join("nil.txt"), nil), "nil buffer is allocated")
	requireContent(t, m.RootDir().Join("nil.txt"), "some longer content")

	require.NoError(t, fs.CopyFileBuf(t.Context(), src, m.RootDir().Join("small.txt"), make([]byte, 3)), "small buffer")
	requireContent(t, m.RootDir().Join("small.txt"), "some longer content")
}

func TestCopyFile_DirectorySource(t *testing.T) {
	m := newMinimalMemFS(t, fs.NewMemFile("/dir/file.txt", []byte("x")))

	err := fs.CopyFile(t.Context(), fs.NewMemFile("sub/", nil), m.RootDir().Join("x"))
	require.ErrorIs(t, err, fs.ErrIsDirectory{}, "MemFile directory")
	require.False(t, m.RootDir().Join("x").Exists(), "nothing must be written")

	err = fs.CopyFile(t.Context(), m.RootDir().Join("dir"), m.RootDir().Join("y"))
	require.ErrorIs(t, err, fs.ErrIsDirectory{}, "File directory")
	require.False(t, m.RootDir().Join("y").Exists(), "nothing must be written")
}

func TestCopyFile_ChecksSourceBeforeMakingDirs(t *testing.T) {
	m := newMinimalMemFS(t)

	err := fs.CopyFile(t.Context(), m.RootDir().Join("missing.txt"), m.RootDir().Join("new", "dir", "x.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.False(t, m.RootDir().Join("new").Exists(), "no directories must be created for a missing source")
}

func TestCopyFile_ReadOnlyDestinationDirectory(t *testing.T) {
	n := newNativeMemFS(t,
		fs.NewMemFile("/src.txt", []byte("x")),
		fs.NewMemFile("/dir/other.txt", []byte("y")),
	)
	n.readOnly = true

	err := fs.CopyFile(t.Context(), n.RootDir().Join("src.txt"), n.RootDir().Join("dir"))
	require.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	require.Empty(t, n.calls, "native CopyFile must not be called on a read-only file system")
}

func TestCopyFile_NativeCopyGetsFinalPath(t *testing.T) {
	n := newNativeMemFS(t,
		fs.NewMemFile("/src.txt", []byte("x")),
		fs.NewMemFile("/dir/other.txt", []byte("y")),
		fs.NewMemFile("/full/src.txt/inner.txt", []byte("z")),
	)
	root := n.RootDir()

	require.NoError(t, fs.CopyFile(t.Context(), root.Join("src.txt"), root.Join("dir")))
	require.Equal(t, []string{"CopyFile(/src.txt, /dir/src.txt)"}, n.calls)
	requireContent(t, root.Join("dir", "src.txt"), "x")

	n.calls = nil
	err := fs.CopyFile(t.Context(), root.Join("src.txt"), root.Join("full"))
	require.ErrorIs(t, err, fs.ErrIsDirectory{}, "the final path is a directory")
	require.Empty(t, n.calls, "native CopyFile must not get a directory as destination")
}

func TestCopyFile_PermWithoutPermissionsFileSystem(t *testing.T) {
	// nativeFileSystem has a native CopyFile, but can't set permissions,
	// so the file is copied through OpenWriter with perm.
	n := newNativeMemFS(t, fs.NewMemFile("/src.txt", []byte("x")))

	require.NoError(t, fs.CopyFile(t.Context(), n.RootDir().Join("src.txt"), n.RootDir().Join("dest.txt"), fs.UserReadWrite))
	require.Empty(t, n.calls, "native CopyFile can't apply perm")
	requireContent(t, n.RootDir().Join("dest.txt"), "x")
}

func TestCopyFile_PermOnNativeLocalCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permissions")
	}
	src := localFile(t, "src.txt", "x")
	require.NoError(t, src.SetPermissions(0o600))
	dir := src.Dir()

	require.NoError(t, fs.CopyFile(t.Context(), src, dir.Join("with-perm.txt"), 0o640))
	require.Equal(t, fs.Permissions(0o640), dir.Join("with-perm.txt").Permissions(), "perm must be applied after the native copy")

	require.NoError(t, fs.CopyFile(t.Context(), src, dir.Join("without-perm.txt")))
	require.Equal(t, fs.Permissions(0o600), dir.Join("without-perm.txt").Permissions(), "the source permissions are kept without perm")
}

func TestCopyFile_Destinations(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/src.txt", []byte("new")),
		fs.NewMemFile("/existing.txt", []byte("old")),
		fs.NewMemFile("/dir/sub/inner.txt", []byte("inner")),
	)
	root := m.RootDir()

	require.NoError(t, fs.CopyFile(t.Context(), root.Join("src.txt"), root.Join("existing.txt")))
	requireContent(t, root.Join("existing.txt"), "new")

	require.NoError(t, fs.CopyFile(t.Context(), root.Join("src.txt"), root.Join("dir")), "into existing directory")
	requireContent(t, root.Join("dir", "src.txt"), "new")

	require.NoError(t, fs.CopyFile(t.Context(), root.Join("src.txt"), root.Join("a", "b", "c.txt")), "with missing parent directories")
	requireContent(t, root.Join("a", "b", "c.txt"), "new")

	require.NoError(t, fs.CopyFile(t.Context(), root.Join("src.txt"), root), "into the directory of the file is a no-op")
	requireContent(t, root.Join("src.txt"), "new")

	err := fs.CopyFile(t.Context(), fs.NewMemFile("sub", []byte("x")), root.Join("dir"))
	require.ErrorIs(t, err, fs.ErrIsDirectory{}, "the final path dir/sub is a directory")
	requireContent(t, root.Join("dir", "sub", "inner.txt"), "inner")
}

func TestCopyRecursive_IntoItself(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	m := newMinimalMemFS(t, fs.NewMemFile("/a/f.txt", []byte("x")))
	root := m.RootDir()

	err := fs.CopyRecursive(ctx, root.Join("a"), root.Join("a", "b"))
	require.ErrorIs(t, err, os.ErrInvalid)
	require.False(t, root.Join("a", "b").Exists(), "nothing must be copied")

	require.NoError(t, fs.CopyRecursive(ctx, root.Join("a"), root.Join("a")), "copying onto itself is a no-op")
	requireContent(t, root.Join("a", "f.txt"), "x")
}

func TestCopyRecursive_PatternsFilterFilesOnly(t *testing.T) {
	mem, err := fs.RegisteredTestMemFileSystem(nil,
		fs.NewMemFile("/src/a.txt", []byte("a")),
		fs.NewMemFile("/src/b.bin", []byte("b")),
		fs.NewMemFile("/src/sub/c.txt", []byte("c")),
		fs.NewMemFile("/src/sub/deeper/d.txt", []byte("d")),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	root := mem.RootDir()

	require.NoError(t, fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dst"), "*.txt"))
	requireContent(t, root.Join("dst", "a.txt"), "a")
	requireContent(t, root.Join("dst", "sub", "c.txt"), "c")
	requireContent(t, root.Join("dst", "sub", "deeper", "d.txt"), "d")
	require.False(t, root.Join("dst", "b.bin").Exists(), "b.bin doesn't match the pattern")
}

func TestCopyRecursive_RejectsEscapingNames(t *testing.T) {
	// The name `..\..\evil.txt` is a valid file name with the separator /,
	// like an archive entry, but would escape dest with the separator \.
	src, err := fs.RegisteredTestMemFileSystem(nil, fs.NewMemFile(`/src/..\..\evil.txt`, []byte("evil")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	dest, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dest.Close() })

	err = fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest.RootDir().Join("dest", "x"))
	require.ErrorIs(t, err, os.ErrInvalid)
	var written []string
	require.NoError(t, dest.RootDir().ListDirRecursive(t.Context(), func(file fs.File) error {
		written = append(written, file.Path())
		return nil
	}))
	require.Empty(t, written, "no file must be written")
}

func TestCopyRecursive_MergesIntoExistingDirectory(t *testing.T) {
	m := newMinimalMemFS(t)
	root := m.RootDir()
	writeFiles(t, root, map[string]string{
		"src/a.txt":     "new a",
		"src/sub/b.txt": "new b",
		"dst/a.txt":     "old a",
		"dst/keep.txt":  "keep",
	})

	require.NoError(t, fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dst")))
	requireContent(t, root.Join("dst", "a.txt"), "new a")
	requireContent(t, root.Join("dst", "sub", "b.txt"), "new b")
	requireContent(t, root.Join("dst", "keep.txt"), "keep")

	writeFiles(t, root, map[string]string{"file-over-dir/a.txt/x.txt": "x", "dir-over-file/sub": "file"})
	err := fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("file-over-dir"))
	require.ErrorIs(t, err, fs.ErrIsDirectory{})
	err = fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dir-over-file"))
	require.ErrorIs(t, err, fs.ErrIsNotDirectory{})
	err = fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dst", "keep.txt"))
	require.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "directory over a file")
}

func TestCopyRecursive_UsesOneBuffer(t *testing.T) {
	const numFiles = 16
	files := make([]fs.MemFile, numFiles)
	for i := range files {
		files[i] = fs.NewMemFile(fmt.Sprintf("/src/file%d.txt", i), []byte(strings.Repeat("x", 100)))
	}
	src := newMinimalMemFS(t, files...)
	dest := newMinimalMemFS(t)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest.RootDir().Join("dst"))
	runtime.ReadMemStats(&after)
	require.NoError(t, err)

	// A copy buffer is 4 MiB, a buffer per file would allocate 64 MiB
	const bufferSize = 4 * 1024 * 1024
	allocated := after.TotalAlloc - before.TotalAlloc
	require.Less(t, allocated, uint64(3*bufferSize), "CopyRecursive must reuse one copy buffer")
	requireContent(t, dest.RootDir().Join("dst", "file0.txt"), strings.Repeat("x", 100))
}
