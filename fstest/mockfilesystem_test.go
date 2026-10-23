package fstest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// memMock is the state of the in-memory file system of newMemMock.
type memMock struct {
	mock   *MockFullyFeaturedFileSystem
	sep    string
	mtx    sync.Mutex
	nodes  map[string]*memNode // key is the clean path
	closed bool
}

// memNode is a file or a directory of a memMock.
type memNode struct {
	dir      bool
	data     []byte
	modified time.Time
}

// newMemMock returns a MockFullyFeaturedFileSystem with an in-memory
// implementation of the primitives that follows the contract of
// fs.FileSystem, and the emulations of the mock for everything else,
// with the directory testDir below the root.
//
// It is the reference the conformance suite is tested with:
// it must pass every check of the suite.
func newMemMock(sep string) (mock *MockFullyFeaturedFileSystem, testDir string) {
	m := &memMock{
		sep:   sep,
		nodes: map[string]*memNode{sep: {dir: true}},
	}
	mock = &MockFullyFeaturedFileSystem{
		MockFileSystem: MockFileSystem{
			MockPrefix:     "memref://" + fsimpl.RandomString(),
			MockSeparator:  func() string { return sep },
			MockStat:       m.stat,
			MockListDir:    m.listDir,
			MockOpenReader: m.openReader,
			MockOpenWriter: m.openWriter,
			MockMakeDir:    m.makeDir,
			MockRemove:     m.remove,
			MockClose:      m.close,
		},
	}
	m.mock = mock
	// The methods without emulation report the closed file system
	unsupported := func(op string) error {
		m.mtx.Lock()
		defer m.mtx.Unlock()
		if err := m.closedErr(); err != nil {
			return err
		}
		return fs.NewErrUnsupported(mock, op)
	}
	mock.MockWatch = func(string, func(fs.File, fs.Event)) (func() error, error) { return nil, unsupported("Watch") }
	mock.MockSetPermissions = func(string, fs.Permissions) error { return unsupported("SetPermissions") }
	mock.MockUser = func(string) (string, error) { return "", unsupported("User") }
	mock.MockSetUser = func(string, string) error { return unsupported("SetUser") }
	mock.MockGroup = func(string) (string, error) { return "", unsupported("Group") }
	mock.MockSetGroup = func(string, string) error { return unsupported("SetGroup") }
	mock.MockCreateSymbolicLink = func(string, string) error { return unsupported("CreateSymbolicLink") }
	mock.MockReadSymbolicLink = func(string) (string, error) { return "", unsupported("ReadSymbolicLink") }
	mock.MockListXAttr = func(string, bool) ([]string, error) { return nil, unsupported("ListXAttr") }
	mock.MockGetXAttr = func(string, string, bool) ([]byte, error) { return nil, unsupported("GetXAttr") }
	mock.MockSetXAttr = func(string, string, []byte, fs.XAttrFlags, bool) error { return unsupported("SetXAttr") }
	mock.MockRemoveXAttr = func(string, string, bool) error { return unsupported("RemoveXAttr") }

	testDir = sep + "test"
	m.nodes[testDir] = &memNode{dir: true}
	return mock, testDir
}

func (m *memMock) file(filePath string) fs.File {
	return m.mock.file(filePath)
}

// closedErr must be called with m.mtx locked.
func (m *memMock) closedErr() error {
	if m.closed {
		return fmt.Errorf("%s: %w", m.mock.Prefix(), fs.ErrFileSystemClosed)
	}
	return nil
}

func (m *memMock) parent(filePath string) string {
	dir, _ := fsimpl.SplitDirAndName(filePath, 0, m.sep)
	return dir
}

// info must be called with m.mtx locked.
func (m *memMock) info(filePath string, node *memNode) *fs.FileInfo {
	_, name := fsimpl.SplitDirAndName(filePath, 0, m.sep)
	perm := fs.Permissions(0o644)
	if node.dir {
		perm = 0o755
	}
	return &fs.FileInfo{
		File:        m.file(filePath),
		Name:        name,
		Exists:      true,
		IsDir:       node.dir,
		IsRegular:   !node.dir,
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        int64(len(node.data)),
		Modified:    node.modified,
		Permissions: perm,
	}
}

func (m *memMock) stat(filePath string) (*fs.FileInfo, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if err := m.closedErr(); err != nil {
		return nil, err
	}
	node, ok := m.nodes[filePath]
	if !ok {
		return nil, fs.NewErrDoesNotExist(m.file(filePath))
	}
	return m.info(filePath, node), nil
}

func (m *memMock) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mtx.Lock()
	if err := m.closedErr(); err != nil {
		m.mtx.Unlock()
		return err
	}
	dir, ok := m.nodes[dirPath]
	switch {
	case !ok:
		m.mtx.Unlock()
		return fs.NewErrDoesNotExist(m.file(dirPath))
	case !dir.dir:
		m.mtx.Unlock()
		return fs.NewErrIsNotDirectory(m.file(dirPath))
	}
	var infos []*fs.FileInfo
	for p, node := range m.nodes {
		if p != dirPath && m.parent(p) == dirPath {
			infos = append(infos, m.info(p, node))
		}
	}
	m.mtx.Unlock()

	slices.SortFunc(infos, func(a, b *fs.FileInfo) int { return strings.Compare(a.Name, b.Name) })
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		if err = callback(info); err != nil {
			return err
		}
	}
	return nil
}

// memReader reads the content of a memNode.
type memReader struct {
	*bytes.Reader
	info *fs.FileInfo
}

func (r *memReader) Close() error                 { return nil }
func (r *memReader) Stat() (iofs.FileInfo, error) { return r.info.StdFileInfo(), nil }

func (m *memMock) openReader(filePath string) (io.ReadCloser, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if err := m.closedErr(); err != nil {
		return nil, err
	}
	node, ok := m.nodes[filePath]
	switch {
	case !ok:
		return nil, fs.NewErrDoesNotExist(m.file(filePath))
	case node.dir:
		return nil, fs.NewErrIsDirectory(m.file(filePath))
	}
	return &memReader{Reader: bytes.NewReader(node.data), info: m.info(filePath, node)}, nil
}

// checkWritable checks that filePath is not a directory
// and its parent is an existing directory.
// Must be called with m.mtx locked.
func (m *memMock) checkWritable(filePath string) error {
	if err := m.closedErr(); err != nil {
		return err
	}
	if node, ok := m.nodes[filePath]; ok && node.dir {
		return fs.NewErrIsDirectory(m.file(filePath))
	}
	parent := m.parent(filePath)
	if dir, ok := m.nodes[parent]; !ok || !dir.dir {
		return fs.NewErrDoesNotExist(m.file(parent))
	}
	return nil
}

// memWriter buffers the written data and commits it in Close.
type memWriter struct {
	m        *memMock
	path     string
	buf      bytes.Buffer
	finished bool
}

func (w *memWriter) Write(p []byte) (int, error) {
	if w.finished {
		return 0, os.ErrClosed
	}
	return w.buf.Write(p)
}

func (w *memWriter) Close() error {
	if w.finished {
		return nil
	}
	w.finished = true
	w.m.mtx.Lock()
	defer w.m.mtx.Unlock()
	if err := w.m.checkWritable(w.path); err != nil {
		return err
	}
	w.m.nodes[w.path] = &memNode{data: bytes.Clone(w.buf.Bytes()), modified: time.Now()}
	return nil
}

func (w *memWriter) Abort() error {
	w.finished = true
	return nil
}

func (m *memMock) openWriter(filePath string, _ fs.Permissions) (io.WriteCloser, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if err := m.checkWritable(filePath); err != nil {
		return nil, err
	}
	return &memWriter{m: m, path: filePath}, nil
}

func (m *memMock) makeDir(dirPath string, _ fs.Permissions) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if err := m.closedErr(); err != nil {
		return err
	}
	if _, ok := m.nodes[dirPath]; ok {
		return fs.NewErrAlreadyExists(m.file(dirPath))
	}
	parent := m.parent(dirPath)
	dir, ok := m.nodes[parent]
	switch {
	case !ok:
		return fs.NewErrDoesNotExist(m.file(parent))
	case !dir.dir:
		return fs.NewErrIsNotDirectory(m.file(parent))
	}
	m.nodes[dirPath] = &memNode{dir: true, modified: time.Now()}
	return nil
}

func (m *memMock) remove(filePath string) error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if err := m.closedErr(); err != nil {
		return err
	}
	node, ok := m.nodes[filePath]
	if !ok {
		return fs.NewErrDoesNotExist(m.file(filePath))
	}
	if filePath == m.sep {
		return fmt.Errorf("%w: can't remove the root directory", os.ErrPermission)
	}
	if node.dir {
		for p := range m.nodes {
			if p != filePath && m.parent(p) == filePath {
				return fs.NewErrDirectoryNotEmpty(m.file(filePath))
			}
		}
	}
	delete(m.nodes, filePath)
	return nil
}

func (m *memMock) close() error {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.closed = true
	return nil
}

// TestMockFileSystem_NilHooks verifies that a MockFileSystem without
// hooks has working defaults and reports the primitives as unsupported
// instead of panicking.
func TestMockFileSystem_NilHooks(t *testing.T) {
	ctx := t.Context()
	m := &MockFileSystem{}
	checkEqual(t, "mock://", m.Prefix(), "default Prefix")
	checkEqual(t, "mock://", m.ID(), "default ID")
	checkEqual(t, "MockFileSystem", m.Name(), "default Name")
	checkEqual(t, "MockFileSystem", m.String(), "default String")
	checkEqual(t, "/", m.Separator(), "default Separator")
	checkEqual(t, fs.File("mock:///"), m.RootDir(), "default RootDir: the scheme-only prefix is followed by the rooted path")
	checkEqual(t, "/a/c", m.CleanPath("mock://a", "b", "..", "c"), "default CleanPath")
	readable, writable := m.ReadableWritable()
	checkTrue(t, readable && writable, "default ReadableWritable")

	_, err := m.Stat("/a")
	checkErrorIs(t, err, errors.ErrUnsupported, "Stat")
	checkErrorIs(t, m.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil }), errors.ErrUnsupported, "ListDir")
	_, err = m.OpenReader("/a")
	checkErrorIs(t, err, errors.ErrUnsupported, "OpenReader")
	_, err = m.OpenWriter("/a", 0)
	checkErrorIs(t, err, errors.ErrUnsupported, "OpenWriter")
	checkErrorIs(t, m.MakeDir("/a", 0), errors.ErrUnsupported, "MakeDir")
	checkErrorIs(t, m.Remove("/a"), errors.ErrUnsupported, "Remove")

	mustNoError(t, fs.Register(m), "Register")
	checkNoError(t, m.Close(), "Close")
	checkFalse(t, fs.IsRegistered(m), "Close must unregister the mock")
}

// TestMockFullyFeaturedFileSystem_NilHooks verifies that every method of
// a MockFullyFeaturedFileSystem without hooks returns instead of panicking:
// the emulations report the unsupported primitives and the methods without
// emulation report themselves as unsupported.
func TestMockFullyFeaturedFileSystem_NilHooks(t *testing.T) {
	ctx := t.Context()
	m := &MockFullyFeaturedFileSystem{}
	nop := func(*fs.FileInfo) error { return nil }

	checkEqual(t, 0, len(m.PrefixAliases()), "default PrefixAliases")
	checkTrue(t, m.IsHidden("/dir/.hidden"), "default IsHidden of a dot file")
	checkFalse(t, m.IsHidden("/.dir/visible"), "default IsHidden of a file in a dot directory")
	checkEqual(t, "", m.VolumeName("/a"), "default VolumeName")
	checkTrue(t, m.IsAbsPath("/a"), "default IsAbsPath")
	checkEqual(t, "/a", m.AbsPath("/a"), "default AbsPath")
	rel, err := m.RelPath("/a/b", "/a/c/d")
	checkNoError(t, err, "default RelPath")
	checkEqual(t, "../c/d", rel, "default RelPath")
	rel, err = m.RelPath("/a", "/a")
	checkNoError(t, err, "default RelPath of the same path")
	checkEqual(t, ".", rel, "default RelPath of the same path")

	errs := map[string]error{}
	_, errs["Exists"] = m.Exists("/a")
	_, errs["ReadAll"] = m.ReadAll(ctx, "/a")
	errs["WriteAll"] = m.WriteAll(ctx, "/a", []byte("x"), 0)
	errs["Append"] = m.Append(ctx, "/a", []byte("x"), 0)
	_, errs["OpenAppendWriter"] = m.OpenAppendWriter("/a", 0)
	_, errs["OpenReadWriter"] = m.OpenReadWriter("/a", 0)
	errs["Truncate"] = m.Truncate(ctx, "/a", 1)
	errs["Touch"] = m.Touch("/a", 0)
	errs["MakeAllDirs"] = m.MakeAllDirs("/a/b", 0)
	errs["RemoveAll"] = m.RemoveAll(ctx, "/a")
	errs["CopyFile"] = m.CopyFile(ctx, "/a", "/b")
	errs["Move"] = m.Move(ctx, "/a", "/b")
	errs["ListDirRecursive"] = m.ListDirRecursive(ctx, "/", nil, nop)
	_, errs["Watch"] = m.Watch("/a", func(fs.File, fs.Event) {})
	errs["SetPermissions"] = m.SetPermissions("/a", fs.UserReadWrite)
	_, errs["User"] = m.User("/a")
	errs["SetUser"] = m.SetUser("/a", "nobody")
	_, errs["Group"] = m.Group("/a")
	errs["SetGroup"] = m.SetGroup("/a", "nobody")
	_, errs["Lstat"] = m.Lstat("/a")
	errs["CreateSymbolicLink"] = m.CreateSymbolicLink("/a", "/b")
	_, errs["ReadSymbolicLink"] = m.ReadSymbolicLink("/b")
	_, errs["ListXAttr"] = m.ListXAttr("/a", true)
	_, errs["GetXAttr"] = m.GetXAttr("/a", "user.x", true)
	errs["SetXAttr"] = m.SetXAttr("/a", "user.x", []byte("x"), 0, true)
	errs["RemoveXAttr"] = m.RemoveXAttr("/a", "user.x", true)
	for name, err := range errs {
		checkErrorIs(t, err, errors.ErrUnsupported, "%s without hooks", name)
	}

	mustNoError(t, fs.Register(m), "Register")
	checkNoError(t, m.Close(), "Close")
	checkFalse(t, fs.IsRegistered(m), "Close must unregister the mock, not only its embedded MockFileSystem")
}

// TestMockFullyFeaturedFileSystem_PrefixAliases verifies that the
// aliases of the hook are used by the registry.
func TestMockFullyFeaturedFileSystem_PrefixAliases(t *testing.T) {
	m, _ := newMemMock("/")
	m.MockPrefixAliases = func() []string { return []string{m.Prefix() + "-alias"} }
	mustNoError(t, fs.Register(m), "Register")
	t.Cleanup(func() { _ = m.Close() })

	f := fs.File(m.Prefix() + "-alias/test")
	checkSameFS(t, m, f.FileSystem(), "a File with the prefix alias must resolve to the mock")
	checkEqual(t, "/test", f.Path(), "Path of a File with the prefix alias")
	checkTrue(t, f.IsDir(), "the test directory must be reachable with the prefix alias")
}

// TestMockFullyFeaturedFileSystem_Move verifies the overwrite semantics
// of the emulated Move directly, without the conformance suite.
func TestMockFullyFeaturedFileSystem_Move(t *testing.T) {
	ctx := t.Context()
	m, dir := newMemMock("/")
	put := func(path, content string) {
		t.Helper()
		mustNoError(t, m.WriteAll(ctx, dir+path, []byte(content), 0), "WriteAll(%q)", path)
	}
	mustNoError(t, m.MakeAllDirs(dir+"/destdir", 0), "MakeAllDirs")
	mustNoError(t, m.MakeAllDirs(dir+"/srcdir/sub", 0), "MakeAllDirs")
	put("/srcdir/child.txt", "child")
	put("/src.txt", "source")
	put("/dest.txt", "destination")

	checkErrorIs(t, m.Move(ctx, dir+"/src.txt", dir+"/destdir"), os.ErrExist, "Move of a file onto a directory")
	checkErrorIs(t, m.Move(ctx, dir+"/srcdir", dir+"/dest.txt"), fs.ErrIsNotDirectory{}, "Move of a directory onto a file")
	checkErrorIs(t, m.Move(ctx, dir+"/srcdir", dir+"/srcdir/sub/new"), os.ErrInvalid, "Move of a directory into itself")
	checkNoError(t, m.Move(ctx, dir+"/src.txt", dir+"/dest.txt"), "Move of a file onto a file")
	data, err := m.ReadAll(ctx, dir+"/dest.txt")
	checkNoError(t, err, "ReadAll")
	checkEqual(t, "source", string(data), "Move must replace the destination file")
	checkNoError(t, m.Move(ctx, dir+"/srcdir", dir+"/moved"), "Move of a directory")
	data, err = m.ReadAll(ctx, dir+"/moved/child.txt")
	checkNoError(t, err, "ReadAll")
	checkEqual(t, "child", string(data), "Move of a directory must move its content")
	_, err = m.Stat(dir + "/srcdir")
	checkErrorIs(t, err, os.ErrNotExist, "Move of a directory must remove the source")
}

// TestMockFullyFeaturedFileSystem_ListDirRecursiveLinks checks that the
// emulated ListDirRecursive lists a symbolic link to a directory as an entry
// with IsDir false and doesn't list the directory it points to.
func TestMockFullyFeaturedFileSystem_ListDirRecursiveLinks(t *testing.T) {
	entries := map[string][]*fs.FileInfo{
		"/":    {{Name: "dir", IsDir: true}, {Name: "link", IsDir: true, IsSymlink: true}},
		"/dir": {{Name: "file.txt", IsRegular: true}},
	}
	m := &MockFullyFeaturedFileSystem{MockFileSystem: MockFileSystem{
		MockListDir: func(_ context.Context, dirPath string, _ []string, callback func(*fs.FileInfo) error) error {
			for _, entry := range entries[dirPath] {
				info := *entry
				if err := callback(&info); err != nil {
					return err
				}
			}
			return nil
		},
	}}

	var listed []string
	err := m.ListDirRecursive(t.Context(), "/", nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.Name)
		checkFalse(t, info.IsDir, "ListDirRecursive must not list a directory, got %q", info.Name)
		return nil
	})
	mustNoError(t, err, "ListDirRecursive")
	checkStrings(t, []string{"file.txt", "link"}, listed, "ListDirRecursive")
}
