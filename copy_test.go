package fs_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	stdfstest "testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/tarfs"
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

// TestCopy_RedactsDestinationPassword verifies that the errors of CopyFile
// and CopyRecursive don't contain the password of the destination URI.
// The destination has no registered file system, so making its directory
// fails with an error that names the directory.
func TestCopy_RedactsDestinationPassword(t *testing.T) {
	src := newMinimalMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")))
	dest := fs.File("ftpunregistered://user:secret@host/dir/sub")

	err := fs.CopyFile(t.Context(), src.RootDir().Join("src", "a.txt"), dest.Join("a.txt"))
	require.ErrorIs(t, err, fs.ErrInvalidFileSystem)
	require.Contains(t, err.Error(), "can't make directory", "the error names the directory")
	require.NotContains(t, err.Error(), "secret", "CopyFile must redact the password")

	err = fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest)
	require.ErrorIs(t, err, fs.ErrInvalidFileSystem)
	require.Contains(t, err.Error(), "can't make directory", "the error names the directory")
	require.NotContains(t, err.Error(), "secret", "CopyRecursive must redact the password")
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

// tarEntry is an entry of a test tar archive,
// a directory if the name ends with a slash.
type tarEntry struct {
	name    string
	mode    int64
	content string
}

// newTarFS returns a registered tar file system with the entries,
// which is closed at the end of the test.
func newTarFS(t *testing.T, entries ...tarEntry) *tarfs.Reader {
	t.Helper()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	for _, entry := range entries {
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entry.name,
			Mode:     entry.mode,
			Size:     int64(len(entry.content)),
			ModTime:  time.Now(),
		}
		if strings.HasSuffix(entry.name, "/") {
			header.Typeflag = tar.TypeDir
		}
		require.NoError(t, w.WriteHeader(header))
		_, err := w.Write([]byte(entry.content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	tarFS, err := tarfs.NewReader(fs.NewMemFile("test.tar", archive.Bytes()), "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(tarFS))
	t.Cleanup(func() { _ = tarFS.Close() })
	return tarFS
}

// localSpecialBits returns the setuid, setgid and sticky bits of a local file
// read with os.Stat, independent of how the package reports permissions.
func localSpecialBits(t *testing.T, file fs.File) os.FileMode {
	t.Helper()
	info, err := os.Stat(file.LocalPath())
	require.NoError(t, err)
	return info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

// TestCopyFile_DropsSpecialPermissionBits is a regression test: a copy took
// over the setuid, setgid and sticky bits of an archive entry, so extracting
// an archive as root created setuid files. The archive entries are empty,
// because a write by a user without root rights clears the setuid and setgid
// bits, which would hide the bits that the copy was created with.
func TestCopyFile_DropsSpecialPermissionBits(t *testing.T) {
	src := newTarFS(t, tarEntry{name: "suid", mode: 0o6755}, tarEntry{name: "sticky", mode: 0o1755}).RootDir()

	t.Run("to a MemFileSystem", func(t *testing.T) {
		dest := newMinimalMemFS(t).RootDir()
		for _, name := range []string{"suid", "sticky"} {
			require.NoError(t, fs.CopyFile(t.Context(), src.Join(name), dest.Join(name)))
			require.Equal(t, fs.Permissions(0o755), dest.Join(name).Permissions(), "permissions of the copy of %s", name)
		}

		require.NoError(t, fs.CopyFile(t.Context(), src.Join("suid"), dest.Join("explicit"), 0o4755))
		require.Equal(t, fs.Permissions(0o4755), dest.Join("explicit").Permissions(), "special bits passed as perm are kept")
	})
	t.Run("native copy within a MemFileSystem", func(t *testing.T) {
		mem, err := fs.RegisteredTestMemFileSystem(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.Close() })
		require.NoError(t, mem.WriteAll(t.Context(), "/suid", nil, 0o6755))

		require.NoError(t, fs.CopyFile(t.Context(), mem.RootDir().Join("suid"), mem.RootDir().Join("copy")))
		require.Equal(t, fs.Permissions(0o755), mem.RootDir().Join("copy").Permissions())
	})
	t.Run("to the local file system", func(t *testing.T) {
		// Linux creates the file with the setuid and setgid bits,
		// macOS drops them for users without root rights
		if runtime.GOOS == "windows" {
			t.Skip("Windows has no Unix permissions")
		}
		dest := fs.File(t.TempDir())
		require.NoError(t, fs.CopyFile(t.Context(), src.Join("suid"), dest.Join("suid")))
		require.Zero(t, localSpecialBits(t, dest.Join("suid")), "the copy must not be setuid or setgid")
		require.True(t, dest.Join("suid").Permissions().Can(fs.UserExecute), "the execute bits are kept")
	})
}

// TestCopyRecursive_DropsSpecialPermissionBits is the CopyRecursive
// variant of TestCopyFile_DropsSpecialPermissionBits.
func TestCopyRecursive_DropsSpecialPermissionBits(t *testing.T) {
	src := newTarFS(t,
		tarEntry{name: "tmp/", mode: 0o1777},
		tarEntry{name: "tmp/suid", mode: 0o4755},
	).RootDir()
	dest := newMinimalMemFS(t).RootDir().Join("extracted")

	require.NoError(t, fs.CopyRecursive(t.Context(), src, dest))
	require.Equal(t, fs.Permissions(0o755), dest.Join("tmp", "suid").Permissions())
	require.Zero(t, dest.Join("tmp").Permissions()&0o7000, "directories are created with the default permissions")
}

// TestCopyFile_FromReadOnlyFileSystem is a regression test: the copy of a file
// of a read-only file system took over the missing write permissions, which
// describe the file system and not the file, so a copied upload or embedded
// file was read-only and writing it failed.
func TestCopyFile_FromReadOnlyFileSystem(t *testing.T) {
	std := fs.NewStdFileSystem(stdfstest.MapFS{
		"read-only.txt": {Data: []byte("r"), Mode: 0o444},
		"tool":          {Data: []byte("t"), Mode: 0o555},
		"no-perm.txt":   {Data: []byte("n")}, // Reported as fs.AllRead
	}, "")
	require.NoError(t, fs.Register(std))
	t.Cleanup(func() { _ = std.Close() })
	dest := newMinimalMemFS(t).RootDir()

	for name, want := range map[string]fs.Permissions{"read-only.txt": 0o644, "tool": 0o755, "no-perm.txt": 0o644} {
		require.NoError(t, fs.CopyFile(t.Context(), std.RootDir().Join(name), dest.Join(name)))
		require.Equal(t, want, dest.Join(name).Permissions(), "the copy of %s gets the write permission of the owner", name)
	}

	// The permissions of a file of a writable file system are kept
	src := newMinimalMemFS(t).RootDir().Join("read-only.txt")
	require.NoError(t, src.WriteAllString(t.Context(), "r", fs.AllRead))
	require.NoError(t, fs.CopyFile(t.Context(), src, dest.Join("copy.txt")))
	require.Equal(t, fs.AllRead, dest.Join("copy.txt").Permissions())
}

// requireFiles requires the files below dir to have exactly the paths relative to dir.
func requireFiles(t *testing.T, dir fs.File, relPaths ...string) {
	t.Helper()
	var listed []string
	err := dir.ListDirRecursive(t.Context(), func(file fs.File) error {
		relPath, err := dir.RelPathOf(file)
		listed = append(listed, filepath.ToSlash(relPath))
		return err
	})
	require.NoError(t, err)
	require.ElementsMatch(t, relPaths, listed, "files below %s", dir)
}

// newLinkMemFS returns a registered MemFileSystem with the initialFiles.
func newLinkMemFS(t *testing.T, initialFiles ...fs.MemFile) *fs.MemFileSystem {
	t.Helper()
	mem, err := fs.RegisteredTestMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// TestCopyRecursive_SymlinkLoop is a regression test: CopyRecursive followed
// symbolic links to directories, a link to the copied directory into itself
// writing nested copies until the path had too many links. A link to a
// directory is copied as link with the same target within one file system
// that can create links, else the copy fails before it writes anything.
// TestCopyRecursive_DirLinksBetweenFileSystems checks the copies
// to another file system.
func TestCopyRecursive_SymlinkLoop(t *testing.T) {
	t.Run("MemFileSystem", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		mem := newLinkMemFS(t,
			fs.NewMemFile("/src/a.txt", []byte("a")),
			fs.NewMemFile("/src/sub/b.txt", []byte("b")),
			fs.NewMemFile("/outside/c.txt", []byte("c")),
		)
		require.NoError(t, mem.CreateSymbolicLink("/src", "/src/loop"))
		require.NoError(t, mem.CreateSymbolicLink("..", "/src/sub/up"))
		require.NoError(t, mem.CreateSymbolicLink("/outside", "/src/ext"))
		dest := mem.RootDir().Join("dest")

		require.NoError(t, fs.CopyRecursive(ctx, mem.RootDir().Join("src"), dest))
		requireFiles(t, dest, "a.txt", "ext", "loop", "sub/b.txt", "sub/up")
		requireLink(t, dest.Join("loop"), "/src")
		requireLink(t, dest.Join("sub", "up"), "..")
		requireLink(t, dest.Join("ext"), "/outside")
	})
	t.Run("LocalFileSystem", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		dir := fs.File(t.TempDir())
		src := dir.Join("src")
		writeFiles(t, dir, map[string]string{"src/a.txt": "a", "src/sub/b.txt": "b", "outside/c.txt": "c"})
		if err := os.Symlink(src.LocalPath(), src.Join("loop").LocalPath()); err != nil {
			t.Skipf("can't create symbolic link: %v", err)
		}
		require.NoError(t, os.Symlink("..", src.Join("sub", "up").LocalPath()))
		require.NoError(t, os.Symlink(dir.Join("outside").LocalPath(), src.Join("ext").LocalPath()))
		dest := dir.Join("dest")

		require.NoError(t, fs.CopyRecursive(ctx, src, dest))
		requireFiles(t, dest, "a.txt", "ext", "loop", "sub/b.txt", "sub/up")
		requireLink(t, dest.Join("loop"), src.LocalPath())
		requireLink(t, dest.Join("sub", "up"), "..")
		requireLink(t, dest.Join("ext"), dir.Join("outside").LocalPath())
	})
	t.Run("destination without symbolic links", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/sub/b.txt", []byte("b")))
		require.NoError(t, mem.CreateSymbolicLink("..", "/src/sub/up"))
		dest := newMinimalMemFS(t).RootDir().Join("dest")

		err := fs.CopyRecursive(ctx, mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, mem.RootDir().Join("src", "sub", "up").String(), "the error must name the link")
		requireMissing(t, dest)
	})
}

// TestCopyRecursive_LinksToFiles checks that a symbolic link to a file
// is copied as a file with the content and permissions of its target,
// and that the patterns also select the links to directories.
func TestCopyRecursive_LinksToFiles(t *testing.T) {
	mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/outside/doc.txt", []byte("doc")))
	require.NoError(t, mem.SetPermissions("/src/a.txt", fs.UserReadWrite|fs.UserExecute))
	require.NoError(t, mem.CreateSymbolicLink("a.txt", "/src/link.txt"))
	require.NoError(t, mem.CreateSymbolicLink("/outside", "/src/docs"))
	dest := newMinimalMemFS(t).RootDir().Join("dest")

	// The link to a directory doesn't match, so the destination doesn't need links
	require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest, "*.txt"))
	requireFiles(t, dest, "a.txt", "link.txt")
	requireContent(t, dest.Join("link.txt"), "a")
	require.Equal(t, fs.UserReadWrite|fs.UserExecute, dest.Join("link.txt").Permissions(), "the copy gets the permissions of the target")
}

// TestCopyRecursive_LinkErrorRedactsPassword checks that the error
// about a link that can't be copied names it without the password
// of the URI of its file system.
func TestCopyRecursive_LinkErrorRedactsPassword(t *testing.T) {
	src := newSymlinkMemFS(t)
	fs.Unregister(src)
	src.URIPrefix = "minimal://user:secret@" + src.id
	register(t, src)
	writeFiles(t, src.RootDir(), map[string]string{"dir/a.txt": "a"})
	require.NoError(t, src.CreateSymbolicLink("/dir", "/dir/loop"))
	dest := newMinimalMemFS(t).RootDir().Join("dest")

	err := fs.CopyRecursive(t.Context(), src.RootDir().Join("dir"), dest)
	require.ErrorIs(t, err, errors.ErrUnsupported)
	require.NotContains(t, err.Error(), "secret")
	require.ErrorContains(t, err, "minimal://user:xxxxx@"+src.id+"/dir/loop")
	requireMissing(t, dest)
}

// TestCopyRecursive_IntoItselfThroughLink is a regression test: copying a
// local directory into itself through a symbolic link to a parent directory
// passed the check of the paths and copied the directory into its copy
// again and again until the path was too long.
func TestCopyRecursive_IntoItselfThroughLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := fs.File(t.TempDir())
	writeFiles(t, dir, map[string]string{"real/sub/a.txt": "a"})
	alias := dir.Join("alias")
	if err := os.Symlink(dir.Join("real").LocalPath(), alias.LocalPath()); err != nil {
		t.Skipf("can't create symbolic link: %v", err)
	}

	err := fs.CopyRecursive(ctx, alias.Join("sub"), dir.Join("real", "sub", "copy"))
	require.ErrorIs(t, err, os.ErrInvalid)
	requireFiles(t, dir.Join("real", "sub"), "a.txt")
}

// caseInsensitiveTempDir returns a new temp directory and skips the test
// if its file system tells apart names that only differ in case.
func caseInsensitiveTempDir(t *testing.T) fs.File {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	require.NoError(t, os.WriteFile(probe, nil, 0o600))
	if _, err := os.Stat(filepath.Join(dir, "PROBE")); err != nil {
		t.Skip("the file system of the temp directory is case-sensitive")
	}
	require.NoError(t, os.Remove(probe))
	return fs.File(dir)
}

// requireLocalLinks skips the test if no symbolic link can be created in dir.
func requireLocalLinks(t *testing.T, dir fs.File) {
	t.Helper()
	probe := dir.Join("probe-link").LocalPath()
	if err := os.Symlink(dir.LocalPath(), probe); err != nil {
		t.Skipf("can't create symbolic link: %v", err)
	}
	require.NoError(t, os.Remove(probe))
}

// TestCopyRecursive_NeverWritesThroughDestinationLinks is a regression test:
// CopyRecursive checked the existing directories below the destination with
// Stat, which follows symbolic links, so it merged a directory of the source
// into the directory that a link at the destination points to, outside of
// the destination, and it wrote a file of the source into the file that a
// link at the destination points to. Below the destination a link where a
// directory or a file is to be written is an error wrapping os.ErrExist.
func TestCopyRecursive_NeverWritesThroughDestinationLinks(t *testing.T) {
	t.Run("link replaced by a directory between two copies", func(t *testing.T) {
		// Like a sync that runs again after the source changed:
		// the first copy creates the link, the second must not
		// copy the directory that replaced it through the link
		mem := newLinkMemFS(t, fs.NewMemFile("/outside/keep.txt", []byte("keep")), fs.NewMemFile("/src/a.txt", []byte("a")))
		require.NoError(t, mem.CreateSymbolicLink("/outside", "/src/x"))
		root := mem.RootDir()
		require.NoError(t, fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dest")))
		requireLink(t, root.Join("dest", "x"), "/outside")
		require.NoError(t, root.Join("src", "x").Remove())
		writeFiles(t, root, map[string]string{"src/x/f.txt": "f"})

		err := fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dest"))
		require.ErrorIs(t, err, os.ErrExist)
		require.ErrorContains(t, err, root.Join("dest", "x").String(), "the error must name the link")
		requireFiles(t, root.Join("outside"), "keep.txt")
		requireLink(t, root.Join("dest", "x"), "/outside")
	})
	t.Run("link replaced by a directory between two local copies", func(t *testing.T) {
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		writeFiles(t, dir, map[string]string{"outside/keep.txt": "keep", "src/a.txt": "a"})
		outside, src, dest := dir.Join("outside"), dir.Join("src"), dir.Join("dest")
		require.NoError(t, os.Symlink(outside.LocalPath(), src.Join("x").LocalPath()))
		require.NoError(t, fs.CopyRecursive(t.Context(), src, dest))
		requireLink(t, dest.Join("x"), outside.LocalPath())
		require.NoError(t, src.Join("x").Remove())
		writeFiles(t, src, map[string]string{"x/f.txt": "f"})

		err := fs.CopyRecursive(t.Context(), src, dest)
		require.ErrorIs(t, err, os.ErrExist)
		require.ErrorContains(t, err, dest.Join("x").String(), "the error must name the link")
		requireFiles(t, outside, "keep.txt")
	})
	t.Run("file over a link", func(t *testing.T) {
		mem := newLinkMemFS(t, fs.NewMemFile("/outside/precious.txt", []byte("precious")), fs.NewMemFile("/src/f.txt", []byte("new")))
		require.NoError(t, mem.MakeAllDirs("/dest", 0))
		require.NoError(t, mem.CreateSymbolicLink("/outside/precious.txt", "/dest/f.txt"))
		root := mem.RootDir()

		err := fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dest"))
		require.ErrorIs(t, err, os.ErrExist)
		require.ErrorContains(t, err, root.Join("dest", "f.txt").String(), "the error must name the link")
		requireContent(t, root.Join("outside", "precious.txt"), "precious")
		requireLink(t, root.Join("dest", "f.txt"), "/outside/precious.txt")
	})
	t.Run("local file over a link", func(t *testing.T) {
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		writeFiles(t, dir, map[string]string{"outside/precious.txt": "precious", "src/f.txt": "new", "dest/keep.txt": "keep"})
		precious := dir.Join("outside", "precious.txt")
		require.NoError(t, os.Symlink(precious.LocalPath(), dir.Join("dest", "f.txt").LocalPath()))

		err := fs.CopyRecursive(t.Context(), dir.Join("src"), dir.Join("dest"))
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, precious, "precious")
		requireLink(t, dir.Join("dest", "f.txt"), precious.LocalPath())
	})
	t.Run("case-insensitive destination", func(t *testing.T) {
		// The link "A" of the destination and the directory "a" of
		// a case-sensitive source are the same name in a case-insensitive
		// destination, the files of "a" must not be written through the link
		dir := caseInsensitiveTempDir(t)
		requireLocalLinks(t, dir)
		writeFiles(t, dir, map[string]string{"outside/keep.txt": "keep", "dest/keep.txt": "keep"})
		outside, dest := dir.Join("outside"), dir.Join("dest")
		require.NoError(t, os.Symlink(outside.LocalPath(), dest.Join("A").LocalPath()))
		mem := newLinkMemFS(t, fs.NewMemFile("/src/a/f.txt", []byte("f")))

		err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireFiles(t, outside, "keep.txt")
		requireLink(t, dest.Join("A"), outside.LocalPath())
	})
	t.Run("destination root is a link", func(t *testing.T) {
		// The destination itself is followed, like for CopyFile
		mem := newLinkMemFS(t, fs.NewMemFile("/src/sub/a.txt", []byte("a")), fs.NewMemFile("/real/keep.txt", []byte("keep")))
		require.NoError(t, mem.CreateSymbolicLink("/real", "/dest"))
		root := mem.RootDir()

		require.NoError(t, fs.CopyRecursive(t.Context(), root.Join("src"), root.Join("dest")))
		requireFiles(t, root.Join("real"), "keep.txt", "sub/a.txt")
		requireLink(t, root.Join("dest"), "/real")
	})
}

// TestCopyRecursive_DirLinksBetweenFileSystems is a regression test:
// the target of a symbolic link to a directory was copied verbatim to another
// file system, where it means another path, like an absolute path of
// a SubFileSystem that became a path of the host. Then only relative targets
// that stay inside the copied directory were copied, but the destination
// resolves the names of a target with its own rules: it follows a link in
// the target before the ".." after it, so two links that each stayed inside
// combined to a link above the copy. Between file systems a link to
// a directory is never copied, the copy fails before anything is written.
func TestCopyRecursive_DirLinksBetweenFileSystems(t *testing.T) {
	t.Run("absolute target of a SubFileSystem", func(t *testing.T) {
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		writeFiles(t, dir, map[string]string{"view/releases/v2/app": "app", "view/tree/a.txt": "a"})
		sub, err := fs.NewSubFileSystem(fs.Local, dir.Join("view").LocalPath(), "")
		require.NoError(t, err)
		register(t, sub)
		require.NoError(t, sub.CreateSymbolicLink("/releases/v2", "/tree/current"))
		link := sub.RootDir().Join("tree", "current")
		target, err := link.ReadSymbolicLink()
		require.NoError(t, err)
		require.Equal(t, "/releases/v2", target, "the view translates the target")
		dest := dir.Join("dest")

		err = fs.CopyRecursive(t.Context(), sub.RootDir().Join("tree"), dest)
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, link.String(), "the error must name the link")
		requireMissing(t, dest)
	})
	t.Run("chained relative links", func(t *testing.T) {
		// Resolved by their names against the directory of the link, both
		// targets stay inside the copy, but the local file system follows
		// the copy of "sub" before the ".." after it: the copy of "x"
		// pointed three levels above the copied directory
		src := newLinkMemFS(t, fs.NewMemFile("/root/a/b/f.txt", []byte("f")))
		require.NoError(t, src.CreateSymbolicLink("../..", "/root/a/b/sub"))
		require.NoError(t, src.CreateSymbolicLink("sub/../../..", "/root/a/b/x"))
		dir := fs.File(t.TempDir())
		requireLocalLinks(t, dir)
		dest := dir.Join("dest")

		err := fs.CopyRecursive(t.Context(), src.RootDir().Join("root"), dest)
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, src.RootDir().Join("root", "a", "b", "sub").String(), "the error must name the link")
		requireMissing(t, dest)
	})
	t.Run("relative targets", func(t *testing.T) {
		// Also a target inside the copy, and also with another
		// separator of the destination
		for _, target := range []string{"sub", ".", "..", "sub/deeper", "sub/deeper/../..", "../outside", "../src/sub"} {
			for _, sep := range []string{"/", `\`} {
				src := newLinkMemFS(t, fs.NewMemFile("/src/sub/deeper/d.txt", []byte("d")), fs.NewMemFile("/outside/c.txt", []byte("c")))
				require.NoError(t, src.CreateSymbolicLink(target, "/src/link"))
				dest, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: sep})
				require.NoError(t, err)
				t.Cleanup(func() { _ = dest.Close() })

				err = fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest.RootDir().Join("dest"))
				require.ErrorIs(t, err, errors.ErrUnsupported, "target %q, separator %q", target, sep)
				require.ErrorContains(t, err, src.RootDir().Join("src", "link").String(), "the error must name the link")
				requireMissing(t, dest.RootDir().Join("dest"))
			}
		}
	})
	t.Run("within one file system", func(t *testing.T) {
		// The same file system resolves the same target to the same directory
		mem := newLinkMemFS(t, fs.NewMemFile("/src/sub/a.txt", []byte("a")), fs.NewMemFile("/outside/c.txt", []byte("c")))
		require.NoError(t, mem.CreateSymbolicLink("/outside", "/src/ext"))
		require.NoError(t, mem.CreateSymbolicLink("../outside", "/src/rel"))
		require.NoError(t, mem.CreateSymbolicLink("sub", "/src/inside"))

		require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), mem.RootDir().Join("dest")))
		requireLink(t, mem.RootDir().Join("dest", "ext"), "/outside")
		requireLink(t, mem.RootDir().Join("dest", "rel"), "../outside")
		requireLink(t, mem.RootDir().Join("dest", "inside"), "sub")
		requireContent(t, mem.RootDir().Join("dest", "sub", "a.txt"), "a")
	})
}

// TestCopyFile_SameFileThroughSubFileSystem is a regression test: copying
// a local file onto itself through a SubFileSystem truncated it, because
// the copy opened the destination for writing before reading the source.
func TestCopyFile_SameFileThroughSubFileSystem(t *testing.T) {
	dir := t.TempDir()
	file := fs.File(dir).Join("file.txt")
	require.NoError(t, file.WriteAllString(t.Context(), "precious"))
	sub, err := fs.NewSubFileSystem(fs.Local, dir, "")
	require.NoError(t, err)
	register(t, sub)

	require.NoError(t, fs.CopyFile(t.Context(), sub.RootDir().Join("file.txt"), file), "a no-op")
	requireContent(t, file, "precious")
	require.NoError(t, fs.CopyFile(t.Context(), file, sub.RootDir().Join("file.txt")), "a no-op")
	requireContent(t, file, "precious")
}

// statCountingFS is a MockFileSystem with the files of a map that counts
// the calls of Stat by path, see newStatCountingFS.
type statCountingFS struct {
	*fstest.MockFileSystem

	files map[string][]byte
	stats map[string]int
}

// newStatCountingFS returns a registered statCountingFS
// with the root directory and the passed files.
func newStatCountingFS(t *testing.T, files map[string][]byte) *statCountingFS {
	t.Helper()
	s := &statCountingFS{files: files, stats: make(map[string]int)}
	prefix := "stat-count-" + fsimpl.RandomString() + "://"
	s.MockFileSystem = &fstest.MockFileSystem{
		MockPrefix: prefix,
		MockStat: func(filePath string) (*fs.FileInfo, error) {
			s.stats[filePath]++
			if filePath == "/" {
				return &fs.FileInfo{Name: "/", Exists: true, IsDir: true}, nil
			}
			data, ok := s.files[filePath]
			if !ok {
				return nil, fs.NewErrDoesNotExist(fs.File(prefix + filePath))
			}
			return &fs.FileInfo{Name: path.Base(filePath), Exists: true, IsRegular: true, Size: int64(len(data))}, nil
		},
		MockOpenReader: func(filePath string) (io.ReadCloser, error) {
			data, ok := s.files[filePath]
			if !ok {
				return nil, fs.NewErrDoesNotExist(fs.File(prefix + filePath))
			}
			return io.NopCloser(bytes.NewReader(data)), nil
		},
		MockOpenWriter: func(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
			return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
				s.files[filePath] = data
				return nil
			}), nil
		},
		MockRemove: func(filePath string) error {
			delete(s.files, filePath)
			return nil
		},
	}
	register(t, s.MockFileSystem)
	return s
}

// TestCopyFile_StatsDestinationOnce is a regression test: CopyFile checked
// twice if the destination is a directory, a round trip each on remote file systems.
func TestCopyFile_StatsDestinationOnce(t *testing.T) {
	s := newStatCountingFS(t, map[string][]byte{})

	err := fs.CopyFile(t.Context(), fs.NewMemFile("a.txt", []byte("data")), s.RootDir().Join("a.txt"))
	require.NoError(t, err)
	require.Equal(t, "data", string(s.files["/a.txt"]))
	require.Equal(t, 1, s.stats["/a.txt"], "the destination must be stated once")
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

// nfcName and nfdName are the composed and the decomposed
// Unicode normalization of the name "é.txt".
const (
	nfcName = "\u00e9.txt"
	nfdName = "e\u0301.txt"
)

// normalizationInsensitiveTempDir returns a new temp directory and skips the
// test if it is not on macOS or its file system tells apart nfcName and
// nfdName, which APFS and HFS+ don't.
func normalizationInsensitiveTempDir(t *testing.T) fs.File {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("only the file systems of macOS match names by Unicode normalization")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, nfcName)
	require.NoError(t, os.WriteFile(probe, nil, 0o600))
	if _, err := os.Stat(filepath.Join(dir, nfdName)); err != nil {
		t.Skip("the file system of the temp directory tells apart Unicode normalizations")
	}
	require.NoError(t, os.Remove(probe))
	return fs.File(dir)
}

// requireOneOf requires the directory dir to have exactly one entry, with
// one of the names of contents and the content for that name, because the
// destination doesn't tell the names apart: the entry that the copy wrote
// first must not have been replaced by the second.
func requireOneOf(t *testing.T, dir fs.File, contents map[string]string) {
	t.Helper()
	var names []string
	require.NoError(t, dir.ListDirInfo(t.Context(), func(info *fs.FileInfo) error {
		names = append(names, info.Name)
		return nil
	}))
	require.Len(t, names, 1, "entries of %s", dir)
	content, ok := contents[names[0]]
	require.True(t, ok, "unexpected entry %q in %s", names[0], dir)
	requireContent(t, dir.Join(names[0]), content)
}

// TestCopyRecursive_NameCollisions is a regression test: two names of
// a case-sensitive source that the destination doesn't tell apart, like
// "A.txt" and "a.txt" on a case-insensitive local file system, were copied
// to the same entry, the second replacing the first without an error.
// The copy must fail with an error wrapping os.ErrExist instead, in a new
// destination directory and in one that existed before.
func TestCopyRecursive_NameCollisions(t *testing.T) {
	caseContents := map[string]string{"A.txt": "upper", "a.txt": "lower"}
	newCaseSource := func(t *testing.T) fs.File {
		mem := newLinkMemFS(t, fs.NewMemFile("/src/A.txt", []byte("upper")), fs.NewMemFile("/src/a.txt", []byte("lower")))
		return mem.RootDir().Join("src")
	}

	t.Run("into a new directory", func(t *testing.T) {
		dest := caseInsensitiveTempDir(t).Join("dest")

		err := fs.CopyRecursive(t.Context(), newCaseSource(t), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireOneOf(t, dest, caseContents)
	})
	t.Run("into an existing directory", func(t *testing.T) {
		dest := caseInsensitiveTempDir(t)

		err := fs.CopyRecursive(t.Context(), newCaseSource(t), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireOneOf(t, dest, caseContents)
	})
	t.Run("over an existing file", func(t *testing.T) {
		// The first name replaces the existing file like every merge,
		// the second must not replace what the first wrote.
		// MemFileSystem lists sorted, so A.txt is copied first.
		dest := caseInsensitiveTempDir(t)
		writeFiles(t, dest, map[string]string{"a.txt": "old"})

		err := fs.CopyRecursive(t.Context(), newCaseSource(t), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, dest.Join("a.txt"), "upper")
	})
	t.Run("directories", func(t *testing.T) {
		dest := caseInsensitiveTempDir(t)
		mem := newLinkMemFS(t, fs.NewMemFile("/src/Sub/x.txt", []byte("x")), fs.NewMemFile("/src/sub/y.txt", []byte("y")))

		err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireFiles(t, dest, "Sub/x.txt")

		dest = dest.Join("new")
		err = fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireFiles(t, dest, "Sub/x.txt")
	})
	t.Run("Unicode normalizations", func(t *testing.T) {
		dest := normalizationInsensitiveTempDir(t)
		mem := newLinkMemFS(t, fs.NewMemFile("/src/"+nfcName, []byte("nfc")), fs.NewMemFile("/src/"+nfdName, []byte("nfd")))

		err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireOneOf(t, dest, map[string]string{nfcName: "nfc", nfdName: "nfd"})
	})
	t.Run("into a new directory of a file system that is not local", func(t *testing.T) {
		// MemFileSystem lists sorted, so A.txt is copied first
		dest := newCaseFoldingFS(t).RootDir().Join("dest")

		err := fs.CopyRecursive(t.Context(), newCaseSource(t), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, dest.Join("a.txt"), "upper")
	})
	t.Run("case-sensitive destination", func(t *testing.T) {
		dest := newLinkMemFS(t).RootDir()

		require.NoError(t, fs.CopyRecursive(t.Context(), newCaseSource(t), dest))
		requireContent(t, dest.Join("A.txt"), "upper")
		requireContent(t, dest.Join("a.txt"), "lower")
	})
	t.Run("merge into an existing local directory", func(t *testing.T) {
		// Every file of a second copy replaces the file of the first,
		// none of them is a file that the second copy wrote
		dest := fs.File(t.TempDir())
		mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/sub/b.txt", []byte("b")))
		require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest))
		require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest))
		requireFiles(t, dest, "a.txt", "sub/b.txt")
	})
}

// TestCopyRecursive_HardLinks is a regression test: in a local directory that
// existed before, two hard links of one file were taken for a name collision.
// The copy wrote the first name, which also changed the file of the second,
// and then found the second name to be a file that it wrote for another name
// of the source, like another case, and failed with an error wrapping
// os.ErrExist. Two hard links are two entries of the directory, the copy must
// write both into the one file, which keeps the data written last, like cp.
func TestCopyRecursive_HardLinks(t *testing.T) {
	dest := fs.File(t.TempDir())
	writeFiles(t, dest, map[string]string{"x.txt": "old"})
	if err := os.Link(dest.Join("x.txt").LocalPath(), dest.Join("y.txt").LocalPath()); err != nil {
		t.Skipf("can't create a hard link: %v", err)
	}
	// MemFileSystem lists sorted, so y.txt is written last
	mem := newLinkMemFS(t, fs.NewMemFile("/src/x.txt", []byte("from x")), fs.NewMemFile("/src/y.txt", []byte("from y")))

	require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest))
	requireNames(t, dest, "x.txt", "y.txt")
	requireContent(t, dest.Join("x.txt"), "from y")
	requireContent(t, dest.Join("y.txt"), "from y")
	xInfo, err := os.Lstat(dest.Join("x.txt").LocalPath())
	require.NoError(t, err)
	yInfo, err := os.Lstat(dest.Join("y.txt").LocalPath())
	require.NoError(t, err)
	require.True(t, os.SameFile(xInfo, yInfo), "x.txt and y.txt must stay hard links of one file")
}

// TestCopyRecursive_InvalidLocalNames is a regression test: a tree copy to
// the local file system only checked the names of the source for separators,
// "." and "..", so it wrote a name that filepath.Localize rejects to another
// entry than the one with that name, like on Windows the name "NUL" to the
// device, which discards the data. The copy and the move must fail with
// an error wrapping os.ErrInvalid before they write anything, and the move
// must keep its source. filepath.Localize rejects a name with a NUL byte on
// every platform, copy_windows_test.go has the Windows names.
func TestCopyRecursive_InvalidLocalNames(t *testing.T) {
	for _, badPath := range []string{"z\x00.txt", "z\x00dir/f.txt", "sub/z\x00.txt"} {
		t.Run(fmt.Sprintf("%q", badPath), func(t *testing.T) {
			// MemFileSystem lists sorted, so the valid file is copied first
			mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/"+badPath, []byte("bad")))
			src := mem.RootDir().Join("src")
			dir := fs.File(t.TempDir())
			sub, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
			require.NoError(t, err)
			register(t, sub)

			err = fs.CopyRecursive(t.Context(), src, dir.Join("copy"))
			require.ErrorIs(t, err, os.ErrInvalid)
			err = fs.CopyRecursive(t.Context(), src, sub.RootDir().Join("sub-copy"))
			require.ErrorIs(t, err, os.ErrInvalid, "through a SubFileSystem")
			err = fs.Move(t.Context(), src, dir)
			require.ErrorIs(t, err, os.ErrInvalid)
			requireNames(t, dir)
			requireContent(t, src.Join("a.txt"), "a")
			requireContent(t, src.Join(badPath), "bad")
		})
	}
}

// requireInvalidLocalDirName requires CopyRecursive and Move of the directory
// /src of a MemFileSystem with the file a.txt and the empty directory dirName,
// whose name is not valid for the local file system, to a local directory to
// fail with an error wrapping os.ErrInvalid before they write anything, and
// the move to keep its source.
func requireInvalidLocalDirName(t *testing.T, dirName string) {
	t.Helper()
	mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")))
	require.NoError(t, mem.MakeAllDirs("/src/"+dirName, 0))
	src := mem.RootDir().Join("src")
	dir := fs.File(t.TempDir())

	err := fs.CopyRecursive(t.Context(), src, dir.Join("copy"))
	require.ErrorIs(t, err, os.ErrInvalid)
	err = fs.Move(t.Context(), src, dir)
	require.ErrorIs(t, err, os.ErrInvalid)
	requireNames(t, dir)
	requireContent(t, src.Join("a.txt"), "a")
	require.True(t, src.Join(dirName).IsDir(), "the source must be kept")
}

// TestCopyRecursive_InvalidLocalNamesWithoutFiles is a regression test: the
// check of the names before a tree copy only listed the files that match the
// patterns, so the name of an empty directory, of a directory without files
// that match and of a file that doesn't match, which the local file system
// doesn't write to an entry of that name, was only rejected by the copy after
// it had created the destination and written the entries before it.
// CopyRecursive and Move must fail with an error wrapping os.ErrInvalid before
// they write anything, and the move must keep its source.
// copy_windows_test.go has the Windows names.
func TestCopyRecursive_InvalidLocalNamesWithoutFiles(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		requireInvalidLocalDirName(t, "z\x00dir")
	})
	for _, badPath := range []string{"z\x00dir/f.bin", "z\x00.bin"} {
		t.Run(fmt.Sprintf("%q not matching the patterns", badPath), func(t *testing.T) {
			mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/"+badPath, []byte("bad")))
			dir := fs.File(t.TempDir())

			err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dir.Join("copy"), "*.txt")
			require.ErrorIs(t, err, os.ErrInvalid)
			requireNames(t, dir)
		})
	}
}

// TestCopyRecursive_InvalidLocalNameThroughOverlay checks that a tree copy
// into an OverlayFileSystem whose upper layer is a SubFileSystem of the local
// file system, which gets every write, rejects a name that the local file
// system doesn't write to an entry of that name, see
// TestCopyRecursive_InvalidLocalNames: the copy must fail with an error
// wrapping os.ErrInvalid before it writes anything to the upper layer.
func TestCopyRecursive_InvalidLocalNameThroughOverlay(t *testing.T) {
	dir := fs.File(t.TempDir())
	upper, err := fs.NewSubFileSystem(fs.Local, dir.LocalPath(), "")
	require.NoError(t, err)
	overlay, err := fs.NewOverlayFileSystem(newLinkMemFS(t), upper, "")
	require.NoError(t, err)
	register(t, overlay)
	// MemFileSystem lists sorted, so the valid file would be copied first
	mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/src/z\x00.txt", []byte("bad")))

	err = fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), overlay.RootDir().Join("copy"))
	require.ErrorIs(t, err, os.ErrInvalid)
	requireNames(t, dir)
}

// TestCopyRecursive_LocalDirLinkOverFile checks the copy of a symbolic link
// to a directory within the local file system over a regular file of an
// existing destination directory: the file is replaced by a link with the
// same target, like os.Rename would replace it, and no temporary link of
// the replacement is left.
func TestCopyRecursive_LocalDirLinkOverFile(t *testing.T) {
	dir := fs.File(t.TempDir())
	requireLocalLinks(t, dir)
	writeFiles(t, dir, map[string]string{"target/t.txt": "t", "src/a.txt": "a", "dest/link": "file"})
	src, dest, target := dir.Join("src"), dir.Join("dest"), dir.Join("target")
	require.NoError(t, os.Symlink(target.LocalPath(), src.Join("link").LocalPath()))

	require.NoError(t, fs.CopyRecursive(t.Context(), src, dest))
	requireLink(t, dest.Join("link"), target.LocalPath())
	requireNames(t, dest, "a.txt", "link")
	requireContent(t, dest.Join("a.txt"), "a")
	requireFiles(t, target, "t.txt")
}

// makeNamedPipe creates a named pipe at a local file, see mkfifo.
// It is nil on platforms that have no named pipes in the file system.
var makeNamedPipe func(t *testing.T, file fs.File)

// requireNamedPipe requires the local file to be a named pipe.
func requireNamedPipe(t *testing.T, file fs.File) {
	t.Helper()
	info, err := os.Lstat(file.LocalPath())
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe, "%s must still be a named pipe", file)
}

// TestCopyRecursiveAndMove_Cells checks the tree copies and the moves between
// file systems for every kind of source entry over every state of the
// destination. The entry of the source is a file, a directory, a symbolic
// link to a file or one to a directory in the directory /src of
// a MemFileSystem. CopyRecursive copies /src into the destination directory,
// which writes the entry to the destination entry of the same name, and Move
// moves the entry into the destination directory, to the same path. Only for
// a name collision Move moves /src, which has a second name for the entry
// that the destination doesn't tell apart and that the copy writes first.
//
// A successful copy or move writes the data of the source to the destination
// entry, the move removes its source. A failed one leaves the destination
// entry as it was, or as the copy wrote it for the other name of a name
// collision, and keeps the source. The targets of the links of the source
// are never changed.
func TestCopyRecursiveAndMove_Cells(t *testing.T) {
	type kind struct {
		name string
		// create creates the entry name of the kind in /src of mem,
		// the data is the content of its file
		create func(t *testing.T, mem *fs.MemFileSystem, name, data string)
		// requireEntry requires file to be an entry that create created
		// with data, or a copy of it, which is never a symbolic link
		requireEntry func(t *testing.T, file fs.File, data string, isCopy bool)
		// target is the format of the path of the file of a link below
		// /targets, which gets the name of the link
		target string
	}
	kinds := []kind{
		{
			name: "file",
			create: func(t *testing.T, mem *fs.MemFileSystem, name, data string) {
				writeFiles(t, mem.RootDir(), map[string]string{"src/" + name: data})
			},
			requireEntry: func(t *testing.T, file fs.File, data string, _ bool) {
				requireContent(t, file, data)
			},
		},
		{
			name: "directory",
			create: func(t *testing.T, mem *fs.MemFileSystem, name, data string) {
				writeFiles(t, mem.RootDir(), map[string]string{"src/" + name + "/f.txt": data})
			},
			requireEntry: func(t *testing.T, file fs.File, data string, _ bool) {
				require.False(t, file.IsSymbolicLink(), "%s is a symbolic link", file)
				requireContent(t, file.Join("f.txt"), data)
			},
		},
		{
			name: "link to a file",
			create: func(t *testing.T, mem *fs.MemFileSystem, name, data string) {
				writeFiles(t, mem.RootDir(), map[string]string{"targets/" + name + ".txt": data})
				require.NoError(t, mem.MakeAllDirs("/src", 0))
				require.NoError(t, mem.CreateSymbolicLink("/targets/"+name+".txt", "/src/"+name))
			},
			requireEntry: func(t *testing.T, file fs.File, data string, isCopy bool) {
				require.Equal(t, !isCopy, file.IsSymbolicLink(), "%s is a symbolic link", file)
				requireContent(t, file, data)
			},
			target: "%s.txt",
		},
		{
			name: "link to a directory",
			create: func(t *testing.T, mem *fs.MemFileSystem, name, data string) {
				writeFiles(t, mem.RootDir(), map[string]string{"targets/" + name + "/f.txt": data})
				require.NoError(t, mem.MakeAllDirs("/src", 0))
				require.NoError(t, mem.CreateSymbolicLink("/targets/"+name, "/src/"+name))
			},
			requireEntry: func(t *testing.T, file fs.File, data string, isCopy bool) {
				require.Equal(t, !isCopy, file.IsSymbolicLink(), "%s is a symbolic link", file)
				requireContent(t, file.Join("f.txt"), data)
			},
			target: "%s/f.txt",
		},
	}
	errIsDir, errIsNotDir, errDirLink := fs.ErrIsDirectory{}, fs.ErrIsNotDirectory{}, errors.ErrUnsupported
	states := []struct {
		name string
		// entry is the name of the entry of the source, partner the name
		// of the entry that the copy of a name collision writes first,
		// with the data "first", the entry gets the data "src"
		entry, partner string
		// destDir returns the destination directory with the state
		// at the destination entry, it can skip the test
		destDir func(t *testing.T) fs.File
		// unchanged requires the destination entry
		// to be as destDir prepared it
		unchanged func(t *testing.T, entryDest fs.File)
		// copyErrs and moveErrs are the errors of CopyRecursive
		// and Move by kind, nil for a success
		copyErrs, moveErrs [4]error
	}{
		{
			name:      "missing",
			entry:     "e",
			destDir:   func(t *testing.T) fs.File { return fs.File(t.TempDir()) },
			unchanged: requireMissing,
			copyErrs:  [4]error{nil, nil, nil, errDirLink},
			moveErrs:  [4]error{nil, nil, nil, errDirLink},
		},
		{
			name:  "existing file",
			entry: "e",
			destDir: func(t *testing.T) fs.File {
				dir := fs.File(t.TempDir())
				writeFiles(t, dir, map[string]string{"e": "old"})
				return dir
			},
			unchanged: func(t *testing.T, entryDest fs.File) { requireContent(t, entryDest, "old") },
			copyErrs:  [4]error{nil, errIsNotDir, nil, errDirLink},
			moveErrs:  [4]error{nil, errIsNotDir, nil, errDirLink},
		},
		{
			name:  "existing directory",
			entry: "e",
			destDir: func(t *testing.T) fs.File {
				dir := fs.File(t.TempDir())
				writeFiles(t, dir, map[string]string{"e/keep.txt": "keep"})
				return dir
			},
			unchanged: func(t *testing.T, entryDest fs.File) { requireNames(t, entryDest, "keep.txt") },
			// CopyRecursive merges directories, Move never does
			copyErrs: [4]error{errIsDir, nil, errIsDir, errDirLink},
			moveErrs: [4]error{os.ErrExist, os.ErrExist, os.ErrExist, errDirLink},
		},
		{
			name:    "collision in a new directory of a file system that is not local",
			entry:   "e",
			partner: "E",
			destDir: func(t *testing.T) fs.File {
				return newCaseFoldingFS(t).RootDir().Join("dest")
			},
			unchanged: requireMissing,
			copyErrs:  [4]error{os.ErrExist, os.ErrExist, os.ErrExist, errDirLink},
			moveErrs:  [4]error{os.ErrExist, os.ErrExist, os.ErrExist, errDirLink},
		},
		{
			name:    "collision in an existing local directory",
			entry:   "e",
			partner: "E",
			// Skips on a case-sensitive file system
			destDir:   caseInsensitiveTempDir,
			unchanged: requireMissing,
			copyErrs:  [4]error{os.ErrExist, os.ErrExist, os.ErrExist, errDirLink},
			moveErrs:  [4]error{os.ErrExist, os.ErrExist, os.ErrExist, errDirLink},
		},
		{
			name:  "named pipe",
			entry: "e",
			destDir: func(t *testing.T) fs.File {
				if makeNamedPipe == nil {
					t.Skipf("no named pipes in the file system on %s", runtime.GOOS)
				}
				dir := fs.File(t.TempDir())
				makeNamedPipe(t, dir.Join("e"))
				return dir
			},
			unchanged: requireNamedPipe,
			copyErrs:  [4]error{os.ErrExist, errIsNotDir, os.ErrExist, errDirLink},
			moveErrs:  [4]error{os.ErrExist, errIsNotDir, os.ErrExist, errDirLink},
		},
		{
			name:      "invalid local name",
			entry:     "z\x00e",
			destDir:   func(t *testing.T) fs.File { return fs.File(t.TempDir()) },
			unchanged: func(t *testing.T, entryDest fs.File) { requireNames(t, entryDest.Dir()) },
			copyErrs:  [4]error{os.ErrInvalid, os.ErrInvalid, os.ErrInvalid, os.ErrInvalid},
			moveErrs:  [4]error{os.ErrInvalid, os.ErrInvalid, os.ErrInvalid, os.ErrInvalid},
		},
		{
			name:  "reserved device name",
			entry: "NUL",
			destDir: func(t *testing.T) fs.File {
				if runtime.GOOS != "windows" {
					t.Skip("only Windows writes the name NUL to a device")
				}
				return fs.File(t.TempDir())
			},
			unchanged: func(t *testing.T, entryDest fs.File) { requireNames(t, entryDest.Dir()) },
			copyErrs:  [4]error{os.ErrInvalid, os.ErrInvalid, os.ErrInvalid, os.ErrInvalid},
			moveErrs:  [4]error{os.ErrInvalid, os.ErrInvalid, os.ErrInvalid, os.ErrInvalid},
		},
	}
	for _, state := range states {
		for i, kind := range kinds {
			for _, move := range []bool{false, true} {
				op, wantErr := "CopyRecursive", state.copyErrs[i]
				if move {
					op, wantErr = "Move", state.moveErrs[i]
				}
				t.Run(state.name+"/"+kind.name+"/"+op, func(t *testing.T) {
					destDir := state.destDir(t)
					mem := newLinkMemFS(t)
					if state.partner != "" {
						kind.create(t, mem, state.partner, "first")
					}
					kind.create(t, mem, state.entry, "src")
					srcDir := mem.RootDir().Join("src")
					src, entryDest := srcDir.Join(state.entry), destDir.Join(state.entry)

					var err error
					switch {
					case !move:
						err = fs.CopyRecursive(t.Context(), srcDir, destDir)
					case state.partner == "":
						err = fs.Move(t.Context(), src, destDir)
					default:
						if destDir.IsDir() {
							// Moved into the directory
							entryDest = destDir.Join("src", state.entry)
						}
						err = fs.Move(t.Context(), srcDir, destDir)
					}

					switch {
					case wantErr == nil:
						require.NoError(t, err)
						kind.requireEntry(t, entryDest, "src", true)
					case state.partner != "" && errors.Is(wantErr, os.ErrExist):
						require.ErrorIs(t, err, wantErr)
						// The entry that the copy wrote first is not replaced
						kind.requireEntry(t, entryDest, "first", true)
					default:
						require.ErrorIs(t, err, wantErr)
						state.unchanged(t, entryDest)
					}
					if move && err == nil {
						requireMissing(t, src)
					} else {
						kind.requireEntry(t, src, "src", false)
						if state.partner != "" {
							kind.requireEntry(t, srcDir.Join(state.partner), "first", false)
						}
					}
					if kind.target != "" {
						requireContent(t, mem.RootDir().Join("targets", fmt.Sprintf(kind.target, state.entry)), "src")
					}
				})
			}
		}
	}
}

// listHookFS is a minimalFileSystem whose ListDir calls onListDir
// with the path of the directory before it lists it.
type listHookFS struct {
	*minimalFileSystem

	onListDir func(dirPath string)
}

// newListHookFS returns a registered listHookFS
// that keeps its files with the initialFiles in a MemFileSystem.
func newListHookFS(t *testing.T, onListDir func(dirPath string), initialFiles ...fs.MemFile) *listHookFS {
	t.Helper()
	l := &listHookFS{newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t, initialFiles...)), onListDir}
	register(t, l)
	return l
}

func (l *listHookFS) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	l.onListDir(dirPath)
	return l.minimalFileSystem.ListDir(ctx, dirPath, patterns, callback)
}

// TestMove_DestinationCreatedDuringCheck is a regression test: a move between
// file systems checks that its destination is missing, then lists the whole
// source, which takes long for a remote source, see treeCopier.checkSource,
// and only then copies into the destination. A destination directory that
// another process created in between was taken for an existing directory:
// the copy replaced the entries of the other process and the move then removed
// the source. Every directory of a move must be new, so the move must fail
// with an error wrapping os.ErrExist, keep the entries of the other process
// and keep its source.
func TestMove_DestinationCreatedDuringCheck(t *testing.T) {
	dest := fs.File(t.TempDir()).Join("dest")
	src := newListHookFS(t, func(string) {
		if !dest.Exists() {
			// Another process creates the destination while the source is listed
			writeFiles(t, dest, map[string]string{"a.txt": "other", "keep.txt": "keep"})
		}
	}, fs.NewMemFile("/src/a.txt", []byte("moved")))

	err := fs.Move(t.Context(), src.RootDir().Join("src"), dest)
	require.ErrorIs(t, err, os.ErrExist)
	requireNames(t, dest, "a.txt", "keep.txt")
	requireContent(t, dest.Join("a.txt"), "other")
	requireContent(t, src.RootDir().Join("src", "a.txt"), "moved")
}

// TestCopyRecursive_SubDirectoryListingError is a regression test: the check
// of the source before a copy to another file system skipped a sub-directory
// whose listing failed with an error wrapping os.ErrNotExist, which ftpfs
// returns for every reply 550, also for a directory that can't be read.
// The copy then failed at that directory after it had written the entries
// before it. CopyRecursive and Move must fail before they write anything,
// and the move must keep its source.
func TestCopyRecursive_SubDirectoryListingError(t *testing.T) {
	m := newMinimalMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")))
	// Listed as directory after a.txt, but listing it fails
	m.ghostDir = "/src/unreadable"
	src := m.RootDir().Join("src")
	dir := fs.File(t.TempDir())

	err := fs.CopyRecursive(t.Context(), src, dir.Join("copy"))
	require.ErrorIs(t, err, os.ErrNotExist)
	err = fs.Move(t.Context(), src, dir.Join("moved"))
	require.ErrorIs(t, err, os.ErrNotExist)
	requireNames(t, dir)
	requireContent(t, src.Join("a.txt"), "a")
}

// TestCopyRecursive_ListedDirLinkLoop is a regression test: an FTP server
// whose listing reports a symbolic link to a directory as directory lets
// ftpfs list the same directory through every path of such links, until the
// server refuses a path with too many links, like with ELOOP, with a reply
// 550, which ftpfs reports as error wrapping os.ErrNotExist. The check of
// the source before a copy to another file system skipped a directory whose
// listing failed so, so with two links to "." it listed every path of up to
// that many links, exponentially many, and the copy then failed after it had
// written a part. The copy must fail at the first such error with a number
// of listings that grows linearly with the number of links that the server
// allows, before it writes anything.
func TestCopyRecursive_ListedDirLinkLoop(t *testing.T) {
	const maxLinks = 8 // the server refuses a path with more links
	listings := 0
	prefix := "link-loop-" + fsimpl.RandomString() + "://"
	src := &fstest.MockFileSystem{
		MockPrefix: prefix,
		MockStat: func(filePath string) (*fs.FileInfo, error) {
			if path.Base(filePath) == "a.txt" {
				return &fs.FileInfo{Name: "a.txt", Exists: true, IsRegular: true, Size: 1}, nil
			}
			return &fs.FileInfo{Name: path.Base(filePath), Exists: true, IsDir: true}, nil
		},
		MockListDir: func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
			listings++
			if strings.Count(dirPath, "/loop") > maxLinks {
				return fs.NewErrDoesNotExist(fs.File(prefix + dirPath))
			}
			// The links loop1 and loop2 to "." are listed as directories
			for _, info := range []*fs.FileInfo{
				{Name: "a.txt", Exists: true, IsRegular: true, Size: 1},
				{Name: "loop1", Exists: true, IsDir: true},
				{Name: "loop2", Exists: true, IsDir: true},
			} {
				if err := callback(info); err != nil {
					return err
				}
			}
			return nil
		},
		MockOpenReader: func(string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("a")), nil
		},
	}
	register(t, src)
	dir := fs.File(t.TempDir())

	err := fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dir.Join("copy"))
	require.ErrorIs(t, err, os.ErrNotExist)
	// The source and every path through loop1 up to the refused one
	require.LessOrEqual(t, listings, maxLinks+2, "the number of listings")
	requireNames(t, dir)
}

// irregularFS is a minimalFileSystem whose Stat describes the files at the
// irregular paths as no regular files, like the special files of a remote
// file system.
type irregularFS struct {
	*minimalFileSystem

	irregular []string
}

// newIrregularFS returns a registered irregularFS with the irregular paths
// that keeps its files in a MemFileSystem.
func newIrregularFS(t *testing.T, irregular ...string) *irregularFS {
	t.Helper()
	i := &irregularFS{newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t)), irregular}
	register(t, i)
	return i
}

func (i *irregularFS) Stat(filePath string) (*fs.FileInfo, error) {
	info, err := i.minimalFileSystem.Stat(filePath)
	if err == nil && slices.Contains(i.irregular, filePath) {
		info.IsRegular = false
	}
	return info, err
}

// TestCopyRecursiveAndMove_OverlaySpecialFiles checks the special files of an
// OverlayFileSystem with layers that are not local, see isSpecialFile: a copy
// and a move write a new file of the upper layer over a special file of the
// base layer, which the new file hides, and refuse a special file of the
// upper layer, which they would write into, with an error wrapping
// os.ErrExist, the move keeping its source. A copy and a move over a special
// file of the base layer were refused like over one of the upper layer.
func TestCopyRecursiveAndMove_OverlaySpecialFiles(t *testing.T) {
	newSource := func(t *testing.T) *fs.MemFileSystem {
		return newLinkMemFS(t, fs.NewMemFile("/src/f.txt", []byte("copied")), fs.NewMemFile("/g.txt", []byte("moved")))
	}
	newOverlay := func(t *testing.T, base fs.FileSystem, upper fs.WriteFileSystem) *fs.OverlayFileSystem {
		overlay, err := fs.NewOverlayFileSystem(base, upper, "")
		require.NoError(t, err)
		register(t, overlay)
		return overlay
	}
	special := map[string]string{"f.txt": "special", "g.txt": "special"}

	t.Run("of the base layer", func(t *testing.T) {
		base := newIrregularFS(t, "/f.txt", "/g.txt")
		writeFiles(t, base.RootDir(), special)
		upper := newMinimalMemFS(t)
		overlay := newOverlay(t, base, upper)
		mem := newSource(t)

		require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), overlay.RootDir()))
		require.NoError(t, fs.Move(t.Context(), mem.RootDir().Join("g.txt"), overlay.RootDir()))
		requireContent(t, upper.RootDir().Join("f.txt"), "copied")
		requireContent(t, upper.RootDir().Join("g.txt"), "moved")
		requireContent(t, base.RootDir().Join("f.txt"), "special")
		requireContent(t, base.RootDir().Join("g.txt"), "special")
		requireMissing(t, mem.RootDir().Join("g.txt"))
	})
	t.Run("of the upper layer", func(t *testing.T) {
		upper := newIrregularFS(t, "/f.txt", "/g.txt")
		writeFiles(t, upper.RootDir(), special)
		overlay := newOverlay(t, newMinimalMemFS(t), upper)
		mem := newSource(t)

		err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), overlay.RootDir())
		require.ErrorIs(t, err, os.ErrExist)
		require.ErrorContains(t, err, "special file")
		err = fs.Move(t.Context(), mem.RootDir().Join("g.txt"), overlay.RootDir())
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, upper.RootDir().Join("f.txt"), "special")
		requireContent(t, upper.RootDir().Join("g.txt"), "special")
		requireContent(t, mem.RootDir().Join("g.txt"), "moved")
	})
}

// TestCopyRecursive_MalformedPatternWritesNothing is a regression test:
// a malformed pattern was only an error when the copy matched it with
// a name. Within one file system that copies links to directories, which
// doesn't list the source before the copy, the copy had created the
// destination directory then, and a source without files was copied without
// an error. CopyRecursive must fail with an error wrapping path.ErrBadPattern
// before it writes anything.
func TestCopyRecursive_MalformedPatternWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		src  func(t *testing.T) fs.File
	}{
		{
			name: "from another file system",
			src: func(t *testing.T) fs.File {
				return newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a"))).RootDir().Join("src")
			},
		},
		{
			name: "from another file system without files",
			src: func(t *testing.T) fs.File {
				mem := newLinkMemFS(t)
				require.NoError(t, mem.MakeAllDirs("/src/sub", 0))
				return mem.RootDir().Join("src")
			},
		},
		{
			name: "within the local file system",
			src: func(t *testing.T) fs.File {
				src := fs.File(t.TempDir())
				writeFiles(t, src, map[string]string{"a.txt": "a"})
				return src
			},
		},
		{
			name: "a file",
			src: func(t *testing.T) fs.File {
				return localFile(t, "a.txt", "a")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fs.File(t.TempDir())

			err := fs.CopyRecursive(t.Context(), tt.src(t), dir.Join("copy"), "*.txt", "[")
			require.ErrorIs(t, err, path.ErrBadPattern)
			require.ErrorContains(t, err, `"["`, "the error must name the pattern")
			requireNames(t, dir)
		})
	}
}

// newRegisteredSubFS returns a SubFileSystem of the directory dirPath
// of parent that is registered until the end of the test.
func newRegisteredSubFS(t *testing.T, parent fs.FileSystem, dirPath string) *fs.SubFileSystem {
	t.Helper()
	sub, err := fs.NewSubFileSystem(parent, dirPath, "")
	require.NoError(t, err)
	register(t, sub)
	return sub
}

// requireSameFileNoOps requires CopyFile and Move of a onto b and of b onto
// a, two paths of the file x.txt in dir with the content "precious", to be
// no-ops: the file must keep its content and be the only entry of dir.
func requireSameFileNoOps(t *testing.T, dir, a, b fs.File) {
	t.Helper()
	for _, files := range [][2]fs.File{{a, b}, {b, a}} {
		src, dest := files[0], files[1]
		require.NoError(t, fs.CopyFile(t.Context(), src, dest), "copying %s onto itself at %s", src, dest)
		requireContent(t, dest, "precious")
		require.NoError(t, fs.Move(t.Context(), src, dest), "moving %s onto itself at %s", src, dest)
		requireContent(t, dest, "precious")
		requireNames(t, dir, "x.txt")
	}
}

// TestCopyAndMove_SameFileThroughSubFileSystem is a regression test: the
// same-file checks of CopyFile and of the copy then delete fallback of Move
// only compared local files, so a move through a SubFileSystem of another
// file system onto the same file of its parent copied the file onto itself
// and then removed it, returning nil, and a writer that truncates the file,
// like the one of sftpfs, emptied it before the copy read it. Through
// a SubFileSystem and its parent, two SubFileSystems of one parent and
// a SubFileSystem of a SubFileSystem, a copy and a move onto the same file
// must be no-ops that keep the file and write nothing.
// TestCopyFile_SameFileThroughSubFileSystem checks the local file system.
func TestCopyAndMove_SameFileThroughSubFileSystem(t *testing.T) {
	// requireNoOps checks the paths of the file data/d/x.txt in dir of
	// parent with the content "precious" through SubFileSystems of parent
	requireNoOps := func(t *testing.T, parent fs.FileSystem, dir fs.File) {
		t.Helper()
		file := dir.Join("data", "d", "x.txt")
		data := newRegisteredSubFS(t, parent, dir.Join("data").Path())
		root := newRegisteredSubFS(t, parent, dir.Path())
		nested := newRegisteredSubFS(t, data, "/d")

		requireSameFileNoOps(t, file.Dir(), data.RootDir().Join("d", "x.txt"), file)
		requireSameFileNoOps(t, file.Dir(), data.RootDir().Join("d", "x.txt"), root.RootDir().Join("data", "d", "x.txt"))
		requireSameFileNoOps(t, file.Dir(), nested.RootDir().Join("x.txt"), file)
		requireSameFileNoOps(t, file.Dir(), nested.RootDir().Join("x.txt"), data.RootDir().Join("d", "x.txt"))
	}

	t.Run("MemFileSystem", func(t *testing.T) {
		mem := newLinkMemFS(t, fs.NewMemFile("/data/d/x.txt", []byte("precious")))
		requireNoOps(t, mem, mem.RootDir())
	})
	t.Run("upload writer", func(t *testing.T) {
		m := newMinimalMemFS(t, fs.NewMemFile("/data/d/x.txt", []byte("precious")))
		m.uploads = true
		requireNoOps(t, m, m.RootDir())
		require.Zero(t, m.commits.Load(), "nothing must be written")
	})
	t.Run("truncating writer", func(t *testing.T) {
		// A file system that is not local whose writer truncates the file
		m := newMinimalLocalFS(t)
		dir := fs.File(m.URI(t.TempDir()))
		writeFiles(t, dir, map[string]string{"data/d/x.txt": "precious"})
		requireNoOps(t, m, dir)
	})
}

// requireTree requires the directory dir of a tree copy test to contain
// exactly the directory d with the file f.txt with the content "f"
// and the other names, so no copy was written.
func requireTree(t *testing.T, dir fs.File, names ...string) {
	t.Helper()
	requireNames(t, dir, append(names, "d")...)
	requireNames(t, dir.Join("d"), "f.txt")
	requireContent(t, dir.Join("d", "f.txt"), "f")
}

// TestCopyAndMove_IntoItselfThroughSubFileSystem is a regression test: the
// checks of CopyRecursive and of the copy then delete fallback of Move for
// a directory copied or moved into itself only compared local paths, so
// through a SubFileSystem of another file system the copy copied the new
// copy directory into itself until the context was done. Copying and moving
// a directory into one of its sub-directories through a SubFileSystem and
// its parent or through two SubFileSystems of one parent must fail with an
// error wrapping os.ErrInvalid before anything is written. Copying the
// directory onto itself is a no-op, moving it onto itself moves it into the
// existing directory, which is moving it into itself like for local
// directories. A copy and a move to a sibling whose name begins with the name
// of the directory are no copy into itself.
func TestCopyAndMove_IntoItselfThroughSubFileSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	mem := newLinkMemFS(t, fs.NewMemFile("/data/d/f.txt", []byte("f")))
	data := mem.RootDir().Join("data")
	sub := newRegisteredSubFS(t, mem, "/data")
	root := newRegisteredSubFS(t, mem, "/")

	tests := []struct {
		name      string
		src, dest fs.File
		onto      bool // dest is the directory src, which only a move moves into
	}{
		{"SubFileSystem root into the parent", sub.RootDir(), data.Join("copy"), false},
		{"SubFileSystem directory into the parent", sub.RootDir().Join("d"), data.Join("d", "copy"), false},
		{"parent directory into the SubFileSystem", data, sub.RootDir().Join("copy"), false},
		{"two SubFileSystems of one parent", sub.RootDir().Join("d"), root.RootDir().Join("data", "d", "copy"), false},
		{"moved onto itself", sub.RootDir().Join("d"), data.Join("d"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.onto {
				err := fs.CopyRecursive(ctx, tt.src, tt.dest)
				require.ErrorIs(t, err, os.ErrInvalid)
				requireTree(t, data)
			}
			err := fs.Move(ctx, tt.src, tt.dest)
			require.ErrorIs(t, err, os.ErrInvalid)
			requireTree(t, data)
		})
	}

	require.NoError(t, fs.CopyRecursive(ctx, sub.RootDir().Join("d"), data.Join("d")), "copying onto itself is a no-op")
	requireTree(t, data)
	require.NoError(t, fs.CopyRecursive(ctx, sub.RootDir().Join("d"), data.Join("d2")), "a sibling with the name as prefix")
	requireContent(t, data.Join("d2", "f.txt"), "f")
	require.NoError(t, fs.CopyRecursive(ctx, sub.RootDir(), mem.RootDir().Join("data2")), "a sibling with the name as prefix")
	requireContent(t, mem.RootDir().Join("data2", "d", "f.txt"), "f")
	require.NoError(t, fs.Move(ctx, sub.RootDir().Join("d2"), data.Join("d23")), "a sibling with the name as prefix")
	requireTree(t, data, "d23")
	requireContent(t, data.Join("d23", "f.txt"), "f")

	// A link to a directory, which a copy to another file system refuses
	links := newLinkMemFS(t, fs.NewMemFile("/data/d/f.txt", []byte("f")))
	require.NoError(t, links.CreateSymbolicLink("/data/d", "/data/link"))
	linksSub := newRegisteredSubFS(t, links, "/data")
	err := fs.CopyRecursive(ctx, linksSub.RootDir(), links.RootDir().Join("data"))
	require.NoError(t, err, "copying onto itself is a no-op, also with a link to a directory")
}

// TestCopyAndMove_ThroughOverlayLayers is a regression test for the checks
// of CopyFile, CopyRecursive and the copy then delete fallback of Move
// between an OverlayFileSystem and its layers, which only compared local
// paths. The layers keep their files in memory and count the files written
// with upload writers. A file of the overlay is the file of the layer that
// holds it, so a copy and a move onto that file are no-ops that write
// nothing, before the move copied the file onto itself and removed it,
// and a writer that truncates would have emptied it. Moving a file or directory
// of the base layer to its path in the upper layer is an error wrapping
// os.ErrInvalid that changes nothing, before removing the source removed the
// moved copy from the upper layer, while copying it there copies it up.
// A directory of the overlay merges the directories of both layers, so
// copying or moving it into a sub-directory of either layer is an error
// wrapping os.ErrInvalid before anything is written, before the copy copied
// itself until the context was done.
func TestCopyAndMove_ThroughOverlayLayers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	base := newMinimalMemFS(t, fs.NewMemFile("/b.txt", []byte("base")), fs.NewMemFile("/d/f.txt", []byte("f")))
	upper := newMinimalMemFS(t, fs.NewMemFile("/u.txt", []byte("upper")))
	base.uploads, upper.uploads = true, true
	overlay, err := fs.NewOverlayFileSystem(base, upper, "")
	require.NoError(t, err)
	register(t, overlay)
	o, b, u := overlay.RootDir(), base.RootDir(), upper.RootDir()

	sameFiles := [][2]fs.File{
		{o.Join("u.txt"), u.Join("u.txt")},
		{u.Join("u.txt"), o.Join("u.txt")},
		{o.Join("b.txt"), b.Join("b.txt")},
	}
	for _, files := range sameFiles {
		src, dest := files[0], files[1]
		require.NoError(t, fs.CopyFile(ctx, src, dest), "copying %s onto itself at %s", src, dest)
		require.NoError(t, fs.Move(ctx, src, dest), "moving %s onto itself at %s", src, dest)
	}
	requireNames(t, u, "u.txt")
	requireContent(t, o.Join("u.txt"), "upper")
	requireContent(t, o.Join("b.txt"), "base")

	err = fs.Move(ctx, o.Join("b.txt"), u.Join("b.txt"))
	require.ErrorIs(t, err, os.ErrInvalid)
	err = fs.Move(ctx, o.Join("d"), u.Join("d"))
	require.ErrorIs(t, err, os.ErrInvalid)
	for _, dest := range []fs.File{u.Join("d", "copy"), b.Join("d", "copy")} {
		err = fs.CopyRecursive(ctx, o.Join("d"), dest)
		require.ErrorIs(t, err, os.ErrInvalid, "copying into %s", dest)
		err = fs.Move(ctx, o.Join("d"), dest)
		require.ErrorIs(t, err, os.ErrInvalid, "moving into %s", dest)
	}
	requireNames(t, u, "u.txt")
	requireTree(t, b, "b.txt")
	requireTree(t, o, "b.txt", "u.txt")
	require.Zero(t, base.commits.Load()+upper.commits.Load(), "nothing must be written")

	require.NoError(t, fs.CopyFile(ctx, o.Join("b.txt"), u.Join("b.txt")), "copying up")
	require.NoError(t, fs.CopyRecursive(ctx, o.Join("d"), u.Join("d")), "copying up")
	requireTree(t, u, "b.txt", "u.txt")
	requireContent(t, u.Join("b.txt"), "base")
}

// TestCopyAndMove_EmptyLocalPath is a regression test: a file URI without
// a path, "file://" or "file://localhost", has the empty path of the local
// file system, which names no file, but the checks of CopyRecursive and of
// the copy then delete fallback of Move for a copy onto itself cleaned the
// empty path to ".", the working directory. So copying the working directory
// to it, also from the root of a SubFileSystem rooted at ".", and moving that
// root to it returned nil without doing anything, and Move took the empty
// paths of "file://" and "file://localhost" for the same file. Every copy
// and move to or from the empty path must fail with an error wrapping
// ErrEmptyPath and keep the working directory as it was, the copy of
// a directory of another file system before it lists the source.
func TestCopyAndMove_EmptyLocalPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	work := fs.File(t.TempDir())
	writeFiles(t, work, map[string]string{"d/f.txt": "f"})
	t.Chdir(work.LocalPath())
	sub := newRegisteredSubFS(t, fs.Local, ".")

	empty := []fs.File{"file://", "file://localhost"}
	dirs := []fs.File{".", sub.RootDir(), "d", sub.RootDir().Join("d")}
	files := []fs.File{fs.File("d").Join("f.txt"), sub.RootDir().Join("d", "f.txt")}
	for _, e := range empty {
		for _, other := range append(dirs, files...) {
			require.ErrorIs(t, fs.CopyRecursive(ctx, other, e), fs.ErrEmptyPath, "copying %s to %s", other, e)
			require.ErrorIs(t, fs.Move(ctx, other, e), fs.ErrEmptyPath, "moving %s to %s", other, e)
			require.ErrorIs(t, fs.CopyRecursive(ctx, e, other), fs.ErrEmptyPath, "copying %s to %s", e, other)
			require.ErrorIs(t, fs.Move(ctx, e, other), fs.ErrEmptyPath, "moving %s to %s", e, other)
			requireTree(t, work)
		}
		for _, file := range files {
			require.ErrorIs(t, fs.CopyFile(ctx, file, e), fs.ErrEmptyPath, "copying %s to %s", file, e)
			require.ErrorIs(t, fs.CopyFile(ctx, e, file), fs.ErrEmptyPath, "copying %s to %s", e, file)
		}
		for _, e2 := range empty {
			require.ErrorIs(t, fs.CopyRecursive(ctx, e, e2), fs.ErrEmptyPath, "copying %s to %s", e, e2)
			require.ErrorIs(t, fs.Move(ctx, e, e2), fs.ErrEmptyPath, "moving %s to %s", e, e2)
		}
	}
	requireTree(t, work)

	other := newListHookFS(t, func(dirPath string) {
		t.Errorf("the source %s must not be listed", dirPath)
	}, fs.NewMemFile("/d/f.txt", []byte("f")))
	for _, e := range empty {
		require.ErrorIs(t, fs.CopyRecursive(ctx, other.RootDir().Join("d"), e), fs.ErrEmptyPath, "copying to %s", e)
		require.ErrorIs(t, fs.Move(ctx, other.RootDir().Join("d"), e), fs.ErrEmptyPath, "moving to %s", e)
	}
	requireContent(t, other.RootDir().Join("d", "f.txt"), "f")
}
