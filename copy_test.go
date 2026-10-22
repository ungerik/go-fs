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
// TestCopyRecursive_LinkTargetsBetweenFileSystems checks the targets
// of links copied to another file system.
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
		// The link "A" and the directory "a" of a case-sensitive source are
		// the same name in a case-insensitive destination, the files of "a"
		// were written through the copy of the link into the directory "B"
		dest := caseInsensitiveTempDir(t)
		requireLocalLinks(t, dest)
		mem := newLinkMemFS(t, fs.NewMemFile("/src/B/b.txt", []byte("b")), fs.NewMemFile("/src/a/f.txt", []byte("f")))
		require.NoError(t, mem.CreateSymbolicLink("B", "/src/A"))

		err := fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), dest)
		require.ErrorIs(t, err, os.ErrExist)
		requireFiles(t, dest.Join("B"), "b.txt")
		requireLink(t, dest.Join("A"), "B")
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

// TestCopyRecursive_LinkTargetsBetweenFileSystems is a regression test:
// the target of a symbolic link to a directory was copied verbatim to another
// file system, where it means another path, like an absolute path of
// a SubFileSystem that became a path of the host. Between file systems only
// a relative target that stays inside the copied directory keeps its meaning,
// other links fail the copy before anything is written.
func TestCopyRecursive_LinkTargetsBetweenFileSystems(t *testing.T) {
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
	t.Run("relative target inside the copy", func(t *testing.T) {
		src := newLinkMemFS(t, fs.NewMemFile("/src/sub/b.txt", []byte("b")), fs.NewMemFile("/src/a.txt", []byte("a")))
		require.NoError(t, src.CreateSymbolicLink("sub", "/src/link"))
		require.NoError(t, src.CreateSymbolicLink("..", "/src/sub/up"))
		require.NoError(t, src.CreateSymbolicLink(".", "/src/self"))
		dest := newLinkMemFS(t).RootDir().Join("dest")

		require.NoError(t, fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest))
		requireFiles(t, dest, "a.txt", "link", "self", "sub/b.txt", "sub/up")
		requireLink(t, dest.Join("link"), "sub")
		requireLink(t, dest.Join("sub", "up"), "..")
		requireLink(t, dest.Join("self"), ".")
	})
	t.Run("relative target leaving the copy", func(t *testing.T) {
		// "../src/sub" leaves the copy and comes back by the name of the
		// source directory, which the destination doesn't have
		for _, target := range []string{"../outside", "../src/sub", "sub/../../outside"} {
			src := newLinkMemFS(t, fs.NewMemFile("/src/sub/b.txt", []byte("b")), fs.NewMemFile("/outside/c.txt", []byte("c")))
			require.NoError(t, src.CreateSymbolicLink(target, "/src/link"))
			dest := newLinkMemFS(t).RootDir().Join("dest")

			err := fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest)
			require.ErrorIs(t, err, errors.ErrUnsupported, "target %q", target)
			require.ErrorContains(t, err, src.RootDir().Join("src", "link").String(), "the error must name the link")
			requireMissing(t, dest)
		}
	})
	t.Run("absolute target within one file system", func(t *testing.T) {
		mem := newLinkMemFS(t, fs.NewMemFile("/src/a.txt", []byte("a")), fs.NewMemFile("/outside/c.txt", []byte("c")))
		require.NoError(t, mem.CreateSymbolicLink("/outside", "/src/ext"))
		require.NoError(t, mem.CreateSymbolicLink("../outside", "/src/rel"))

		require.NoError(t, fs.CopyRecursive(t.Context(), mem.RootDir().Join("src"), mem.RootDir().Join("dest")))
		requireLink(t, mem.RootDir().Join("dest", "ext"), "/outside")
		requireLink(t, mem.RootDir().Join("dest", "rel"), "../outside")
	})
	t.Run("separator of the destination", func(t *testing.T) {
		src := newLinkMemFS(t, fs.NewMemFile("/src/sub/deeper/d.txt", []byte("d")))
		require.NoError(t, src.CreateSymbolicLink("sub/deeper", "/src/link"))
		require.NoError(t, src.CreateSymbolicLink("../..", "/src/sub/deeper/up"))
		dest, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`})
		require.NoError(t, err)
		t.Cleanup(func() { _ = dest.Close() })

		require.NoError(t, fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest.RootDir().Join("dest")))
		requireLink(t, dest.RootDir().Join("dest", "link"), `sub\deeper`)
		requireLink(t, dest.RootDir().Join("dest", "sub", "deeper", "up"), `..\..`)
	})
	t.Run("target that the separator of the destination can't express", func(t *testing.T) {
		// The name `we\ird` of the source is two names with the separator \
		src := newLinkMemFS(t, fs.NewMemFile(`/src/we\ird/d.txt`, []byte("d")))
		require.NoError(t, src.CreateSymbolicLink(`we\ird`, "/src/link"))
		dest, err := fs.RegisteredTestMemFileSystem(&fs.MemFileSystemOptions{Separator: `\`})
		require.NoError(t, err)
		t.Cleanup(func() { _ = dest.Close() })

		err = fs.CopyRecursive(t.Context(), src.RootDir().Join("src"), dest.RootDir().Join("dest"))
		require.ErrorIs(t, err, errors.ErrUnsupported)
		require.ErrorContains(t, err, src.RootDir().Join("src", "link").String(), "the error must name the link")
		requireMissing(t, dest.RootDir().Join("dest"))
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
