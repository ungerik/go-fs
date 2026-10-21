package fs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
)

// minimalFileSystem implements nothing but fs.FileSystem and
// fs.WriteFileSystem by forwarding to a backing file system,
// a MemFileSystem or the local file system. Every other
// operation therefore has to be served by the generic emulations in
// dispatch.go instead of a native implementation.
//
// The methods are written out instead of embedding the backing file system,
// because embedding would promote all of its optional interfaces and
// bypass exactly the emulations this file system exists to exercise.
// The wrapper types below add single optional interfaces.
//
// The fields after backing simulate the behaviour of remote file systems
// and are set by the tests before using the file system.
type minimalFileSystem struct {
	fsimpl.PathHelper

	id      string
	backing fs.WriteFileSystem

	// readOnly makes the file system report that it is not writable.
	readOnly bool

	// uploads makes OpenWriter return an uploadWriter that commits
	// the written data only in Close, like the writers of object stores.
	uploads bool
	// closeErr is returned by Close of an uploadWriter
	// without committing the data, like a failed upload.
	closeErr error
	// writeErr is returned by Write of an uploadWriter.
	writeErr error
	// onWrite is called by Write of an uploadWriter.
	onWrite func()
	// commits and aborts count the committed and aborted uploadWriters.
	commits, aborts atomic.Int32

	// readErr is returned by readers from OpenReader
	// after the first Read, which still returns data.
	readErr error
	// onRead is called by Read of readers from OpenReader.
	onRead func()

	// ghostDir is reported as directory by ListDir of its parent
	// directory, but does not exist, like a directory that is removed
	// while its parent is listed.
	ghostDir string
}

var (
	_ fs.FileSystem      = new(minimalFileSystem)
	_ fs.WriteFileSystem = new(minimalFileSystem)
)

// newMinimalFileSystem returns an unregistered minimalFileSystem
// with the prefix "minimal://"+id that forwards to backing.
func newMinimalFileSystem(id string, backing fs.WriteFileSystem) *minimalFileSystem {
	return &minimalFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: "minimal://" + id, PathSep: backing.Separator(), Rooted: true},
		id:         id,
		backing:    backing,
	}
}

// newMemBacking returns a MemFileSystem with the initialFiles
// as backing of a minimalFileSystem.
func newMemBacking(t *testing.T, initialFiles ...fs.MemFile) *fs.MemFileSystem {
	t.Helper()
	mem, err := fs.NewMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// newMinimalMemFS returns a registered minimalFileSystem with a unique prefix
// that keeps its files with the initialFiles in a MemFileSystem.
func newMinimalMemFS(t *testing.T, initialFiles ...fs.MemFile) *minimalFileSystem {
	t.Helper()
	m := newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t, initialFiles...))
	register(t, m)
	return m
}

// newMinimalLocalFS returns a registered minimalFileSystem with a unique prefix
// whose paths are the paths of the local file system.
// It skips the test on Windows, where local paths have volume names.
func newMinimalLocalFS(t *testing.T) *minimalFileSystem {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("minimalFileSystem doesn't support local Windows paths")
	}
	m := newMinimalFileSystem(fsimpl.RandomString(), fs.Local)
	register(t, m)
	return m
}

// register registers fileSystem until the end of the test.
func register(t *testing.T, fileSystem fs.FileSystem) {
	t.Helper()
	require.NoError(t, fs.Register(fileSystem))
	t.Cleanup(func() { fs.Unregister(fileSystem) })
}

func (m *minimalFileSystem) ID() string     { return m.id }
func (m *minimalFileSystem) Name() string   { return "minimal file system" }
func (m *minimalFileSystem) String() string { return m.Name() + " with prefix " + m.Prefix() }
func (m *minimalFileSystem) RootDir() fs.File {
	return fs.File(m.URL(m.Separator()))
}

func (m *minimalFileSystem) ReadableWritable() (readable, writable bool) {
	readable, writable = m.backing.ReadableWritable()
	return readable, writable && !m.readOnly
}

// info returns a copy of the backing FileInfo with the File
// translated to a URI of this file system.
func (m *minimalFileSystem) info(filePath string, backingInfo *fs.FileInfo) *fs.FileInfo {
	info := *backingInfo
	info.File = fs.File(m.JoinCleanURI(filePath))
	return &info
}

func (m *minimalFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	backingInfo, err := m.backing.Stat(filePath)
	if err != nil {
		return nil, err
	}
	return m.info(filePath, backingInfo), nil
}

func (m *minimalFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	err := m.backing.ListDir(ctx, dirPath, patterns, func(backingInfo *fs.FileInfo) error {
		return callback(m.info(m.CleanPath(dirPath, backingInfo.Name), backingInfo))
	})
	if err != nil || m.ghostDir == "" {
		return err
	}
	if parent, name := m.SplitDirAndName(m.ghostDir); parent == m.CleanPath(dirPath) {
		return callback(&fs.FileInfo{File: fs.File(m.URL(m.ghostDir)), Name: name, Exists: true, IsDir: true})
	}
	return nil
}

func (m *minimalFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	reader, err := m.backing.OpenReader(filePath)
	if err != nil || (m.readErr == nil && m.onRead == nil) {
		return reader, err
	}
	return &hookReader{ReadCloser: reader, fs: m}, nil
}

func (m *minimalFileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if !m.uploads && m.closeErr == nil {
		return m.backing.OpenWriter(filePath, perm)
	}
	if info, err := m.backing.Stat(filePath); err == nil && info.IsDir {
		return nil, fs.NewErrIsDirectory(fs.File(m.URL(filePath)))
	}
	return &uploadWriter{fs: m, filePath: filePath, perm: perm}, nil
}

func (m *minimalFileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return m.backing.MakeDir(dirPath, perm)
}

func (m *minimalFileSystem) Remove(filePath string) error {
	return m.backing.Remove(filePath)
}

// Close unregisters the file system and closes the backing file system,
// so that every operation reports fs.ErrFileSystemClosed afterwards
// like the backing does. The local file system is shared by all tests
// and never closed.
func (m *minimalFileSystem) Close() error {
	fs.Unregister(m)
	if m.backing == fs.WriteFileSystem(fs.Local) {
		return nil
	}
	return m.backing.Close()
}

// hookReader is a reader from minimalFileSystem.OpenReader
// that implements its readErr and onRead.
type hookReader struct {
	io.ReadCloser
	fs    *minimalFileSystem
	reads int
}

func (r *hookReader) Read(p []byte) (int, error) {
	r.reads++
	if r.fs.onRead != nil {
		r.fs.onRead()
	}
	if r.fs.readErr != nil && r.reads > 1 {
		return 0, r.fs.readErr
	}
	return r.ReadCloser.Read(p)
}

// uploadWriter buffers the written data and commits it to the
// backing file system only in Close, like an upload to an object store.
// It implements fs.WriteAborter.
type uploadWriter struct {
	fs       *minimalFileSystem
	filePath string
	perm     fs.Permissions
	append   bool
	data     []byte
	done     bool
}

var _ fs.WriteAborter = new(uploadWriter)

func (w *uploadWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, os.ErrClosed
	}
	if w.fs.onWrite != nil {
		w.fs.onWrite()
	}
	if w.fs.writeErr != nil {
		return 0, w.fs.writeErr
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *uploadWriter) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	if w.fs.closeErr != nil {
		return w.fs.closeErr
	}
	w.fs.commits.Add(1)
	data := w.data
	if w.append {
		current, err := readBacking(w.fs.backing, w.filePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data = append(current, data...)
	}
	writer, err := w.fs.backing.OpenWriter(w.filePath, w.perm)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return errors.Join(err, writer.Close())
}

func (w *uploadWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	w.fs.aborts.Add(1)
	return nil
}

func readBacking(backing fs.FileSystem, filePath string) ([]byte, error) {
	reader, err := backing.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

// symlinkFileSystem is a minimalFileSystem that also implements
// fs.SymbolicLinkFileSystem by forwarding to its backing file system.
type symlinkFileSystem struct{ *minimalFileSystem }

var _ fs.SymbolicLinkFileSystem = symlinkFileSystem{}

func newSymlinkMemFS(t *testing.T, initialFiles ...fs.MemFile) symlinkFileSystem {
	t.Helper()
	s := symlinkFileSystem{newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t, initialFiles...))}
	register(t, s)
	return s
}

func (s symlinkFileSystem) links() fs.SymbolicLinkFileSystem {
	return s.backing.(fs.SymbolicLinkFileSystem)
}

func (s symlinkFileSystem) Lstat(filePath string) (*fs.FileInfo, error) {
	backingInfo, err := s.links().Lstat(filePath)
	if err != nil {
		return nil, err
	}
	return s.info(filePath, backingInfo), nil
}

func (s symlinkFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	return s.links().CreateSymbolicLink(targetPath, linkPath)
}

func (s symlinkFileSystem) ReadSymbolicLink(linkPath string) (string, error) {
	return s.links().ReadSymbolicLink(linkPath)
}

// appendWriterFileSystem is a minimalFileSystem that also implements
// fs.AppendWriterFileSystem with uploadWriters.
type appendWriterFileSystem struct{ *minimalFileSystem }

var _ fs.AppendWriterFileSystem = appendWriterFileSystem{}

func (a appendWriterFileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return &uploadWriter{fs: a.minimalFileSystem, filePath: filePath, perm: perm, append: true}, nil
}

// nativeFileSystem is a minimalFileSystem that also implements
// fs.CopyFileSystem and fs.MoveFileSystem by forwarding to its
// MemFileSystem backing and records the calls.
type nativeFileSystem struct {
	*minimalFileSystem

	calls []string
}

var (
	_ fs.CopyFileSystem = new(nativeFileSystem)
	_ fs.MoveFileSystem = new(nativeFileSystem)
)

func newNativeMemFS(t *testing.T, initialFiles ...fs.MemFile) *nativeFileSystem {
	t.Helper()
	n := &nativeFileSystem{minimalFileSystem: newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t, initialFiles...))}
	register(t, n)
	return n
}

func (n *nativeFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	n.calls = append(n.calls, fmt.Sprintf("CopyFile(%s, %s)", srcFile, destFile))
	return n.backing.(fs.CopyFileSystem).CopyFile(ctx, srcFile, destFile)
}

func (n *nativeFileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	n.calls = append(n.calls, fmt.Sprintf("Move(%s, %s)", filePath, destPath))
	return n.backing.(fs.MoveFileSystem).Move(ctx, filePath, destPath)
}

// TestGenericEmulations_Conformance runs the conformance suite against a
// file system that implements only the required primitives, so that the
// suite exercises the generic emulations of dispatch.go: Exists, ReadAll,
// WriteAll, Append, OpenAppendWriter, OpenReadWriter, Truncate, Touch,
// MakeAllDirs, RemoveAll, Move, Rename, ListDirMax and ListDirRecursive.
//
// Every other backend of this module implements those optional
// interfaces natively, so without this test the emulations that make the
// interfaces optional in the first place would go unverified.
func TestGenericEmulations_Conformance(t *testing.T) {
	mem, err := fs.RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	minimal := newMinimalFileSystem("emulated", mem)
	require.NoError(t, minimal.MakeDir("/conformance", 0), "creating the test directory")

	fstest.RunConformance(t, minimal, fstest.Config{
		Name:    "minimal file system",
		Prefix:  "minimal://emulated",
		TestDir: "/conformance",
	})
}

// requireContent requires file to exist with content.
func requireContent(t *testing.T, file fs.File, content string) {
	t.Helper()
	data, err := file.ReadAllString(t.Context())
	require.NoError(t, err, "reading %s", file)
	require.Equal(t, content, data, "content of %s", file)
}

// writeFiles writes the files of a map from path to content below dir.
func writeFiles(t *testing.T, dir fs.File, files map[string]string) {
	t.Helper()
	for filePath, content := range files {
		file := dir.Join(filePath)
		require.NoError(t, file.Dir().MakeAllDirs())
		require.NoError(t, file.WriteAllString(t.Context(), content))
	}
}

func TestWriteAllEmulation_AbortsOnCanceledContext(t *testing.T) {
	m := newMinimalMemFS(t, fs.NewMemFile("/file.txt", []byte("old")))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.uploads = true
	m.onWrite = cancel

	err := m.RootDir().Join("file.txt").WriteAllString(ctx, "partial")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), m.aborts.Load(), "the writer must be aborted")
	require.Zero(t, m.commits.Load(), "nothing must be committed")
	m.onWrite = nil
	requireContent(t, m.RootDir().Join("file.txt"), "old")
}

func TestWriteAllEmulation_ReturnsCloseError(t *testing.T) {
	m := newMinimalMemFS(t)
	m.closeErr = errors.New("upload failed")

	err := m.RootDir().Join("file.txt").WriteAllString(t.Context(), "data")
	require.ErrorIs(t, err, m.closeErr)
}

func TestAppendEmulation_AbortsOnWriteError(t *testing.T) {
	a := appendWriterFileSystem{newMinimalFileSystem(fsimpl.RandomString(), newMemBacking(t, fs.NewMemFile("/file.txt", []byte("old"))))}
	register(t, a)
	a.writeErr = errors.New("write failed")

	err := a.RootDir().Join("file.txt").Append(t.Context(), []byte("more"))
	require.ErrorIs(t, err, a.writeErr)
	require.Equal(t, int32(1), a.aborts.Load(), "the append writer must be aborted")
	require.Zero(t, a.commits.Load(), "nothing must be committed")
	requireContent(t, a.RootDir().Join("file.txt"), "old")
}

func TestMoveEmulation_KeepsSourceWhenUploadFails(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/src.txt", []byte("precious")),
		fs.NewMemFile("/dir/file.txt", []byte("precious too")),
	)
	m.closeErr = errors.New("upload failed")
	root := m.RootDir()

	err := root.Join("src.txt").MoveTo(t.Context(), root.Join("dest.txt"))
	require.ErrorIs(t, err, m.closeErr, "MoveTo must report the failed upload")
	requireContent(t, root.Join("src.txt"), "precious")
	require.False(t, root.Join("dest.txt").Exists(), "nothing must be committed")

	_, err = root.Join("src.txt").Rename(t.Context(), "renamed.txt")
	require.ErrorIs(t, err, m.closeErr, "Rename must report the failed upload")
	requireContent(t, root.Join("src.txt"), "precious")

	err = root.Join("dir").MoveTo(t.Context(), root.Join("moved-dir"))
	require.ErrorIs(t, err, m.closeErr, "MoveTo of a directory must report the failed upload")
	requireContent(t, root.Join("dir", "file.txt"), "precious too")
}

func TestMoveEmulation_AbortsOnCanceledContext(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/src.txt", []byte("new")),
		fs.NewMemFile("/dest.txt", []byte("old")),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m.uploads = true
	m.onRead = cancel
	root := m.RootDir()

	_, err := root.Join("src.txt").Rename(ctx, "dest.txt")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), m.aborts.Load(), "the writer must be aborted")
	m.onRead = nil
	requireContent(t, root.Join("src.txt"), "new")
	requireContent(t, root.Join("dest.txt"), "old")
}

// TestMoveEmulation_OverwriteSemantics checks the destination table
// of MoveFileSystem.Move for the emulation. Rename passes the final
// path to the emulation, fs.Move first resolves an existing
// directory destination to the path inside the directory.
func TestMoveEmulation_OverwriteSemantics(t *testing.T) {
	setup := func(t *testing.T) fs.File {
		m := newMinimalMemFS(t)
		root := m.RootDir()
		writeFiles(t, root, map[string]string{
			"file.txt":           "file",
			"other.txt":          "other",
			"dir/a.txt":          "a",
			"other-dir/b.txt":    "b",
			"into/dir/inner.txt": "inner",
		})
		return root
	}

	t.Run("file onto missing path", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("file.txt").Rename(t.Context(), "new.txt")
		require.NoError(t, err)
		requireContent(t, root.Join("new.txt"), "file")
		require.False(t, root.Join("file.txt").Exists())
	})
	t.Run("file replaces existing file", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("file.txt").Rename(t.Context(), "other.txt")
		require.NoError(t, err)
		requireContent(t, root.Join("other.txt"), "file")
		require.False(t, root.Join("file.txt").Exists())
	})
	t.Run("file onto existing directory", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("file.txt").Rename(t.Context(), "dir")
		require.ErrorIs(t, err, os.ErrExist)
		require.ErrorIs(t, err, fs.ErrAlreadyExists{})
		requireContent(t, root.Join("file.txt"), "file")
		require.False(t, root.Join("dir", "file.txt").Exists(), "must not move into the directory")
		requireContent(t, root.Join("dir", "a.txt"), "a")
	})
	t.Run("directory onto existing file", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("dir").Rename(t.Context(), "file.txt")
		require.ErrorIs(t, err, fs.ErrIsNotDirectory{})
		requireContent(t, root.Join("file.txt"), "file")
		requireContent(t, root.Join("dir", "a.txt"), "a")
	})
	t.Run("directory onto existing directory", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("dir").Rename(t.Context(), "other-dir")
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, root.Join("dir", "a.txt"), "a")
		requireContent(t, root.Join("other-dir", "b.txt"), "b")
		require.False(t, root.Join("other-dir", "a.txt").Exists(), "directories must not be merged")
	})
	t.Run("directory onto missing path", func(t *testing.T) {
		root := setup(t)
		_, err := root.Join("dir").Rename(t.Context(), "new-dir")
		require.NoError(t, err)
		requireContent(t, root.Join("new-dir", "a.txt"), "a")
		require.False(t, root.Join("dir").Exists())
	})
	t.Run("Move into existing directory", func(t *testing.T) {
		root := setup(t)
		require.NoError(t, fs.Move(t.Context(), root.Join("file.txt"), root.Join("dir")))
		requireContent(t, root.Join("dir", "file.txt"), "file")
		require.False(t, root.Join("file.txt").Exists())
	})
	t.Run("Move into directory with a directory of the same name", func(t *testing.T) {
		root := setup(t)
		err := fs.Move(t.Context(), root.Join("dir"), root.Join("into"))
		require.ErrorIs(t, err, os.ErrExist)
		requireContent(t, root.Join("dir", "a.txt"), "a")
		requireContent(t, root.Join("into", "dir", "inner.txt"), "inner")
		require.False(t, root.Join("into", "dir", "a.txt").Exists(), "directories must not be merged")
	})
	t.Run("Move onto itself", func(t *testing.T) {
		root := setup(t)
		require.NoError(t, fs.Move(t.Context(), root.Join("dir"), root.Join("dir")))
		require.NoError(t, fs.Move(t.Context(), root.Join("file.txt"), root))
		requireContent(t, root.Join("dir", "a.txt"), "a")
		requireContent(t, root.Join("file.txt"), "file")
	})
}

func TestMoveEmulation_DirectoryIntoItself(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	m := newMinimalMemFS(t, fs.NewMemFile("/a/f.txt", []byte("x")))
	root := m.RootDir()

	err := fs.Move(ctx, root.Join("a"), root.Join("a", "b"))
	require.ErrorIs(t, err, os.ErrInvalid)
	require.False(t, root.Join("a", "b").Exists(), "nothing must be copied")
	requireContent(t, root.Join("a", "f.txt"), "x")

	// Moving into an existing sub-directory resolves to /a/b/a
	require.NoError(t, root.Join("a", "b").MakeDir())
	err = fs.Move(ctx, root.Join("a"), root.Join("a", "b"))
	require.ErrorIs(t, err, os.ErrInvalid)
	require.True(t, root.Join("a", "b").IsEmptyDir(), "nothing must be copied")
	requireContent(t, root.Join("a", "f.txt"), "x")
}

func TestMove_DirectoryIntoItselfNotPassedToNativeMove(t *testing.T) {
	n := newNativeMemFS(t, fs.NewMemFile("/a/f.txt", []byte("x")))

	err := fs.Move(t.Context(), n.RootDir().Join("a"), n.RootDir().Join("a", "b"))
	require.ErrorIs(t, err, os.ErrInvalid)
	require.Empty(t, n.calls, "native Move must not be called")
}

func TestRenameEmulation_InvalidNames(t *testing.T) {
	m := newMinimalMemFS(t, fs.NewMemFile("/dir/sub/f.txt", []byte("precious")))
	file := m.RootDir().Join("dir", "sub", "f.txt")

	for _, newName := range []string{"", ".", "..", "x/y", "/"} {
		_, err := file.Rename(t.Context(), newName)
		require.ErrorIs(t, err, os.ErrInvalid, "Rename(%q)", newName)
		requireContent(t, file, "precious")
	}
	_, err := m.RootDir().Join("dir", "sub").Rename(t.Context(), "..")
	require.ErrorIs(t, err, os.ErrInvalid, `Rename("..") of a directory`)
	requireContent(t, file, "precious")
}

// symlinkTree creates the directory "tree" in dir with the file "file.txt"
// and the symbolic link "link" pointing to the directory "outside",
// which contains the file "precious.txt", and returns tree and outside.
func symlinkTree(t *testing.T, dir fs.File, createLink func(target, link string) error) (tree, outside fs.File) {
	t.Helper()
	tree, outside = dir.Join("tree"), dir.Join("outside")
	writeFiles(t, dir, map[string]string{
		"tree/file.txt":        "file",
		"outside/precious.txt": "precious",
	})
	if err := createLink(outside.Path(), tree.Join("link").Path()); err != nil {
		t.Skipf("can't create symbolic link: %v", err)
	}
	return tree, outside
}

func TestRemoveAllEmulation_DoesNotFollowSymlinks(t *testing.T) {
	t.Run("ListDir describes links", func(t *testing.T) {
		// Local without SymbolicLinkFileSystem: the emulation
		// only knows the links from ListDir and Stat.
		m := newMinimalLocalFS(t)
		dir := fs.File(m.URL(t.TempDir()))
		tree, outside := symlinkTree(t, dir, os.Symlink)

		require.NoError(t, tree.RemoveRecursive(t.Context()))
		require.False(t, tree.Exists(), "tree must be removed")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("Lstat describes links", func(t *testing.T) {
		s := newSymlinkMemFS(t)
		tree, outside := symlinkTree(t, s.RootDir(), s.CreateSymbolicLink)

		require.NoError(t, tree.RemoveRecursive(t.Context()))
		require.False(t, tree.Exists(), "tree must be removed")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("link to a directory", func(t *testing.T) {
		m := newMinimalLocalFS(t)
		dir := fs.File(m.URL(t.TempDir()))
		tree, outside := symlinkTree(t, dir, os.Symlink)

		require.NoError(t, tree.Join("link").RemoveRecursive(t.Context()))
		require.False(t, tree.Join("link").Exists(), "the link must be removed")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
}

func TestMoveEmulation_Symlinks(t *testing.T) {
	t.Run("links are copied as links", func(t *testing.T) {
		s := newSymlinkMemFS(t)
		tree, outside := symlinkTree(t, s.RootDir(), s.CreateSymbolicLink)
		moved := s.RootDir().Join("moved")

		require.NoError(t, tree.MoveTo(t.Context(), moved))
		require.False(t, tree.Exists(), "the source must be removed")
		requireContent(t, moved.Join("file.txt"), "file")
		require.True(t, moved.Join("link").IsSymbolicLink(), "the link must be moved as a link")
		target, err := moved.Join("link").ReadSymbolicLink()
		require.NoError(t, err)
		require.Equal(t, outside.Path(), target, "the link target must be kept verbatim")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("without SymbolicLinkFileSystem", func(t *testing.T) {
		m := newMinimalLocalFS(t)
		dir := fs.File(m.URL(t.TempDir()))
		tree, outside := symlinkTree(t, dir, os.Symlink)
		moved := dir.Join("moved")

		require.NoError(t, tree.MoveTo(t.Context(), moved))
		require.False(t, tree.Exists(), "the source must be removed")
		requireContent(t, moved.Join("file.txt"), "file")
		requireContent(t, moved.Join("link", "precious.txt"), "precious")
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("link is moved, not its target", func(t *testing.T) {
		s := newSymlinkMemFS(t)
		tree, outside := symlinkTree(t, s.RootDir(), s.CreateSymbolicLink)

		_, err := tree.Join("link").Rename(t.Context(), "renamed-link")
		require.NoError(t, err)
		require.True(t, tree.Join("renamed-link").IsSymbolicLink())
		require.False(t, tree.Join("link").Exists())
		requireContent(t, outside.Join("precious.txt"), "precious")
	})
	t.Run("existing link is replaced, not its target", func(t *testing.T) {
		s := newSymlinkMemFS(t)
		root := s.RootDir()
		writeFiles(t, root, map[string]string{"src.txt": "new", "target.txt": "precious"})
		require.NoError(t, s.CreateSymbolicLink("/target.txt", "/link"))

		_, err := root.Join("src.txt").Rename(t.Context(), "link")
		require.NoError(t, err)
		require.False(t, root.Join("link").IsSymbolicLink(), "the link must be replaced")
		requireContent(t, root.Join("link"), "new")
		requireContent(t, root.Join("target.txt"), "precious")
	})
}

func TestListDirRecursiveEmulation_ReturnsCallbackError(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/dir/sub/a.txt", []byte("a")),
		fs.NewMemFile("/dir/sub/b.txt", []byte("b")),
	)
	callbackErr := fmt.Errorf("callback failed: %w", os.ErrNotExist)
	calls := 0

	err := m.RootDir().Join("dir").ListDirRecursive(t.Context(), func(fs.File) error {
		calls++
		return callbackErr
	})
	require.ErrorIs(t, err, callbackErr, "the callback error must be returned")
	require.Equal(t, 1, calls, "the listing must stop at the callback error")
}

func TestListDirRecursiveEmulation_IgnoresVanishedDirectory(t *testing.T) {
	m := newMinimalMemFS(t,
		fs.NewMemFile("/dir/a.txt", []byte("a")),
		fs.NewMemFile("/dir/sub/b.txt", []byte("b")),
	)
	m.ghostDir = "/dir/ghost"

	var files []string
	err := m.RootDir().Join("dir").ListDirRecursive(t.Context(), func(file fs.File) error {
		files = append(files, path.Base(file.Path()))
		return nil
	})
	require.NoError(t, err, "a directory that vanished while listing is ignored")
	require.ElementsMatch(t, []string{"a.txt", "b.txt"}, files)
}

func TestListDirRecursiveEmulation_ReturnsListingError(t *testing.T) {
	m := newMinimalMemFS(t)

	err := m.RootDir().Join("missing").ListDirRecursive(t.Context(), func(fs.File) error { return nil })
	require.ErrorIs(t, err, os.ErrNotExist, "a missing directory is an error")
}

// localFile returns the File of a new local file with content in a temp dir.
func localFile(t *testing.T, name, content string) fs.File {
	t.Helper()
	file := fs.File(filepath.Join(t.TempDir(), name))
	require.NoError(t, file.WriteAllString(t.Context(), content))
	return file
}
