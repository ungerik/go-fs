package dirtree

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// testPaths are the paths of the file system in the tests.
var testPaths = fsimpl.PathHelper{URIPrefix: "test://id", Rooted: true}

// TestTree_ImpliedDirectories verifies that the directories along an
// entry path are created implicitly, because archives usually only
// contain the file entries.
func TestTree_ImpliedDirectories(t *testing.T) {
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tree := New()
	_, err := tree.Add("docs/sub/a.txt", false, 3, mod, "a")
	require.NoError(t, err)
	// Leading slashes and "." elements must not create extra nodes
	_, err = tree.Add("/./docs/b.txt", false, 5, mod, nil)
	require.NoError(t, err)
	_, err = tree.Add("empty/", true, 0, mod, nil)
	require.NoError(t, err)

	docs := tree.Lookup("docs")
	require.NotNil(t, docs)
	assert.True(t, docs.IsDir, "implied directory")
	assert.Equal(t, "docs", docs.Path)
	assert.Nil(t, docs.Sys, "an implied directory has no Sys")

	a := tree.Lookup("/docs/sub/a.txt")
	require.NotNil(t, a, "leading and trailing slashes must be ignored")
	assert.False(t, a.IsDir)
	assert.Equal(t, int64(3), a.Size)
	assert.Equal(t, "docs/sub/a.txt", a.Path)
	assert.Equal(t, "a.txt", a.Name)
	assert.Equal(t, mod, a.Modified)
	assert.Equal(t, "a", a.Sys)

	empty := tree.Lookup("empty")
	require.NotNil(t, empty)
	assert.True(t, empty.IsDir)

	assert.Nil(t, tree.Lookup("docs/missing.txt"), "unknown path")
	assert.Nil(t, tree.Lookup("docs/sub/a.txt/deeper"), "path below a file")
	assert.Same(t, tree.Root(), tree.Lookup("/"), "the root path")
	assert.Same(t, tree.Root(), tree.Lookup("."), "a dot path is the root")
	assert.True(t, tree.Root().IsDir)
	assert.Equal(t, "", tree.Root().Path)
}

// TestTree_LaterEntryReplaces verifies that a later entry with the same
// path replaces an earlier one. tarfs used to report the size of the
// first entry and read the data at the offset of the last one, which
// returned a corrupt mix of both.
func TestTree_LaterEntryReplaces(t *testing.T) {
	first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	tree := New()
	_, err := tree.Add("a.txt", false, 5, first, int64(100))
	require.NoError(t, err)
	_, err = tree.Add("a.txt", false, 25, second, int64(200))
	require.NoError(t, err)
	a := tree.Lookup("a.txt")
	assert.Equal(t, int64(25), a.Size, "size of the later entry")
	assert.Equal(t, int64(200), a.Sys, "Sys of the later entry")
	assert.Equal(t, second, a.Modified)
	assert.Len(t, tree.Root().Children(), 1, "no duplicate node")

	// A directory entry for an existing directory keeps its children
	_, err = tree.Add("dir/x.txt", false, 1, first, nil)
	require.NoError(t, err)
	_, err = tree.Add("dir/", true, 0, second, "dir")
	require.NoError(t, err)
	dir := tree.Lookup("dir")
	assert.Equal(t, second, dir.Modified)
	assert.Equal(t, "dir", dir.Sys)
	assert.NotNil(t, tree.Lookup("dir/x.txt"), "the children stay")

	// A file replaces a directory together with its children
	_, err = tree.Add("dir", false, 3, second, nil)
	require.NoError(t, err)
	assert.False(t, tree.Lookup("dir").IsDir)
	assert.Nil(t, tree.Lookup("dir/x.txt"))

	// A path through a file replaces the file with an implied directory
	_, err = tree.Add("dir/y.txt", false, 1, second, nil)
	require.NoError(t, err)
	assert.True(t, tree.Lookup("dir").IsDir)
	assert.NotNil(t, tree.Lookup("dir/y.txt"))

	// A directory entry replaces a file
	_, err = tree.Add("a.txt", true, 0, second, nil)
	require.NoError(t, err)
	assert.True(t, tree.Lookup("a.txt").IsDir)
	assert.Zero(t, tree.Lookup("a.txt").Size)
}

// TestTree_Children verifies the listing order: directories
// first, then files, both by name.
func TestTree_Children(t *testing.T) {
	tree := New()
	for _, name := range []string{"z.txt", "a.txt", "zdir/x", "adir/x"} {
		_, err := tree.Add(name, false, 1, time.Time{}, nil)
		require.NoError(t, err)
	}
	var names []string
	for _, child := range tree.Root().Children() {
		names = append(names, child.Name)
	}
	assert.Equal(t, []string{"adir", "zdir", "a.txt", "z.txt"}, names)
	assert.Empty(t, tree.Lookup("a.txt").Children(), "a file has no children")
}

// TestTree_RejectsUnsafeNames verifies that entry names which could
// address a path outside of an extracted archive are refused. A tar
// entry `..\..\evil.txt` passed the old check for "/" separated ".."
// elements and CopyRecursive wrote it outside of the destination
// on file systems with a backslash separator.
func TestTree_RejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"../evil", "a/../../evil", "..", "a/../b", `..\..\evil.txt`, `a\b.txt`} {
		tree := New()
		_, err := tree.Add(name, false, 1, time.Time{}, nil)
		assert.Error(t, err, "entry %q must be rejected", name)
		assert.Empty(t, tree.Root().Children(), "entry %q must not be added", name)

		_, _, err = tree.Conflict(name)
		assert.Error(t, err, "Conflict(%q)", name)
	}

	tree := New()
	_, err := tree.Add("", false, 1, time.Time{}, nil)
	assert.Error(t, err, "a file can't be the root")
	_, err = tree.Add("/", true, 0, time.Time{}, "root")
	require.NoError(t, err, "a directory entry for the root")
	assert.Equal(t, "root", tree.Root().Sys)
}

// TestCleanName verifies that unsafe archive entry names are turned into
// names inside the archive, like archive/zip does for its io/fs view.
func TestCleanName(t *testing.T) {
	tests := map[string]string{
		"a.txt":           "a.txt",
		"/a/b.txt":        "a/b.txt",
		"a/":              "a",
		"./a//b/./c":      "a/b/c",
		"../evil.txt":     "evil.txt",
		"a/../../evil":    "evil",
		`..\..\evil.txt`:  "evil.txt",
		`dir\file.txt`:    "dir/file.txt",
		"..":              "",
		".":               "",
		"":                "",
		"/":               "",
		"a%20b/100%25.md": "a%20b/100%25.md",
	}
	for name, want := range tests {
		got := CleanName(name)
		assert.Equal(t, want, got, "CleanName(%q)", name)
		_, err := Elements(got)
		assert.NoError(t, err, "a cleaned name %q must be valid", got)
	}
}

// TestCleanName_ValidNames verifies that CleanName of a name that
// Elements accepts is its elements joined with slashes. The writers
// of tarfs and zipfs rely on that to name the entries of paths
// that were validated with Elements.
func TestCleanName_ValidNames(t *testing.T) {
	for _, name := range []string{
		"", ".", "/", "//", "a", "/a", "a/", "/a/b/", "a//b", "./a/./b/.", "a/.b/..c/c..",
		"a%20b/100%25.md", "a:b/c d", "dir/sub/file.txt",
	} {
		elements, err := Elements(name)
		require.NoError(t, err, "Elements(%q)", name)
		assert.Equal(t, strings.Join(elements, "/"), CleanName(name), "CleanName(%q)", name)
	}
}

// TestTree_CleanNodePaths verifies that empty and "." elements do not
// end up in a node's Path: a lookup by the cleaned path of a listed File
// must find the node the File was listed from.
func TestTree_CleanNodePaths(t *testing.T) {
	tree := New()
	_, err := tree.Add("a//b/./c.txt", false, 3, time.Time{}, nil)
	require.NoError(t, err)

	node := tree.Lookup("a/b/c.txt")
	require.NotNil(t, node, "the entry must be reachable by its cleaned path")
	assert.Equal(t, "a/b/c.txt", node.Path)
	assert.Equal(t, "a/b", tree.Lookup("a/b").Path)
}

// TestTree_Conflict verifies how writers find out whether
// a new entry would replace or contradict a written one.
func TestTree_Conflict(t *testing.T) {
	tree := New()
	_, err := tree.Add("dir/file.txt", false, 1, time.Time{}, nil)
	require.NoError(t, err)

	node, isAncestor, err := tree.Conflict("new.txt")
	require.NoError(t, err)
	assert.Nil(t, node, "no conflict for a new name")
	assert.False(t, isAncestor)

	node, isAncestor, err = tree.Conflict("dir/file.txt")
	require.NoError(t, err)
	assert.Equal(t, "dir/file.txt", node.Path, "the existing file")
	assert.False(t, isAncestor)

	node, isAncestor, err = tree.Conflict("/dir")
	require.NoError(t, err)
	assert.Equal(t, "dir", node.Path, "the implied directory")
	assert.True(t, node.IsDir)
	assert.False(t, isAncestor)

	node, isAncestor, err = tree.Conflict("dir/file.txt/below/x")
	require.NoError(t, err)
	assert.Equal(t, "dir/file.txt", node.Path, "the file in the way")
	assert.True(t, isAncestor)

	node, isAncestor, err = tree.Conflict("/")
	require.NoError(t, err)
	assert.Same(t, tree.Root(), node, "the root always exists")
	assert.False(t, isAncestor)
}

// TestList verifies the flat and recursive listing with patterns,
// callback errors and ctx cancellation.
func TestList(t *testing.T) {
	tree := New()
	for _, name := range []string{"b.txt", "a.md", "sub/c.txt", "sub/deeper/d.txt"} {
		_, err := tree.Add(name, false, 1, time.Time{}, nil)
		require.NoError(t, err)
	}
	names := func(recursive bool, patterns ...string) (names []string) {
		t.Helper()
		err := List(t.Context(), tree.Root(), patterns, recursive, func(n *Node) error {
			names = append(names, n.Path)
			return nil
		})
		require.NoError(t, err)
		return names
	}
	assert.Equal(t, []string{"sub", "a.md", "b.txt"}, names(false))
	assert.Equal(t, []string{"b.txt"}, names(false, "*.txt"))
	assert.Equal(t, []string{"sub/deeper/d.txt", "sub/c.txt", "a.md", "b.txt"}, names(true))
	assert.Equal(t, []string{"sub/deeper/d.txt", "sub/c.txt", "b.txt"}, names(true, "*.txt"))

	stop := errors.New("stop")
	err := List(t.Context(), tree.Root(), nil, true, func(*Node) error { return stop })
	assert.ErrorIs(t, err, stop)

	err = List(t.Context(), tree.Root(), []string{"["}, false, func(*Node) error { return nil })
	assert.Error(t, err, "an invalid pattern")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = List(ctx, tree.Root(), nil, false, func(*Node) error { return nil })
	assert.ErrorIs(t, err, context.Canceled)
}

// TestTree_ValidateNew verifies the typed errors of package fs that
// the writers return for an entry that can't be added.
func TestTree_ValidateNew(t *testing.T) {
	tree := New()
	_, err := tree.Add("dir/file.txt", false, 1, time.Time{}, nil)
	require.NoError(t, err)

	assert.NoError(t, tree.ValidateNew("testfs", testPaths, "new.txt", false), "a new file")
	assert.NoError(t, tree.ValidateNew("testfs", testPaths, "dir/new", true), "a new directory in an existing one")

	err = tree.ValidateNew("testfs", testPaths, "dir/file.txt", false)
	assert.ErrorIs(t, err, fs.ErrAlreadyExists{}, "an existing file")
	assert.ErrorContains(t, err, "test://id/dir/file.txt", "the error names the file")

	err = tree.ValidateNew("testfs", testPaths, "dir", false)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "a file can't replace a directory")

	err = tree.ValidateNew("testfs", testPaths, "dir", true)
	assert.ErrorIs(t, err, fs.ErrAlreadyExists{}, "an existing directory")

	err = tree.ValidateNew("testfs", testPaths, "dir/file.txt/sub", true)
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "a file as ancestor")
	assert.ErrorContains(t, err, "test://id/dir/file.txt", "the error names the ancestor")

	err = tree.ValidateNew("testfs", testPaths, "../evil.txt", false)
	assert.ErrorIs(t, err, os.ErrInvalid, "an invalid name")
	assert.ErrorContains(t, err, "testfs: ", "the error names the file system")
}

// TestTree_AddMissingDirs verifies that only the missing directories
// along a path are added, in order, and that a file stops it.
func TestTree_AddMissingDirs(t *testing.T) {
	tree := New()
	_, err := tree.Add("a", true, 0, time.Time{}, nil)
	require.NoError(t, err)
	_, err = tree.Add("file.txt", false, 1, time.Time{}, nil)
	require.NoError(t, err)

	var added []string
	addDir := func(dirPath string) error {
		added = append(added, dirPath)
		_, err := tree.Add(dirPath, true, 0, time.Time{}, nil)
		return err
	}
	require.NoError(t, tree.AddMissingDirs(testPaths, []string{"a", "b", "c"}, addDir))
	assert.Equal(t, []string{"a/b", "a/b/c"}, added, "the existing directory a is skipped")

	added = nil
	require.NoError(t, tree.AddMissingDirs(testPaths, []string{"a", "b", "c"}, addDir))
	assert.Empty(t, added, "all directories exist")

	err = tree.AddMissingDirs(testPaths, []string{"file.txt", "sub"}, addDir)
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "a file along the path")
	assert.ErrorContains(t, err, "test://id/file.txt")
	assert.Empty(t, added, "nothing is added after the file")

	err = tree.AddMissingDirs(testPaths, []string{"a", "b", "c", "file.txt"}, addDir)
	assert.NoError(t, err, "a file name only as last element of another path")
	assert.Equal(t, []string{"a/b/c/file.txt"}, added)

	errAdd := errors.New("add failed")
	err = tree.AddMissingDirs(testPaths, []string{"x", "y"}, func(string) error { return errAdd })
	assert.ErrorIs(t, err, errAdd, "the error of addDir is returned")

	err = tree.AddMissingDirs(testPaths, []string{"x", "y"}, func(string) error { return nil })
	assert.ErrorContains(t, err, `"x" was not added`, "an addDir that doesn't add the directory")
}

// TestFileInfo verifies the FileInfo of the root and of the entries.
func TestFileInfo(t *testing.T) {
	const (
		defaultPerm    = fs.UserReadWrite
		defaultDirPerm = fs.UserReadWriteExecute
	)
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tree := New()
	_, err := tree.Add("/", true, 0, mod, nil)
	require.NoError(t, err)
	_, err = tree.Add("dir/.hidden.txt", false, 3, mod, "file sys")
	require.NoError(t, err)

	root := FileInfo(testPaths, tree.Root(), fs.AllRead, defaultPerm, defaultDirPerm, "root sys")
	assert.Equal(t, fs.File("test://id/"), root.File)
	assert.Equal(t, "/", root.Name)
	assert.True(t, root.Exists)
	assert.True(t, root.IsDir)
	assert.False(t, root.IsRegular)
	assert.Equal(t, mod, root.Modified)
	assert.Equal(t, fs.AllRead, root.Permissions, "the permissions of an entry")
	assert.Equal(t, "root sys", root.Sys, "the root keeps sys, like the header of an explicit root entry")

	file := FileInfo(testPaths, tree.Lookup("dir/.hidden.txt"), 0, defaultPerm, defaultDirPerm, "file sys")
	assert.Equal(t, fs.File("test://id/dir/.hidden.txt"), file.File)
	assert.Equal(t, ".hidden.txt", file.Name)
	assert.True(t, file.Exists)
	assert.False(t, file.IsDir)
	assert.True(t, file.IsRegular)
	assert.True(t, file.IsHidden, "a name beginning with a dot")
	assert.Equal(t, int64(3), file.Size)
	assert.Equal(t, mod, file.Modified)
	assert.Equal(t, defaultPerm, file.Permissions, "the default permissions of a file")
	assert.Equal(t, "file sys", file.Sys)

	dir := FileInfo(testPaths, tree.Lookup("dir"), 0, defaultPerm, defaultDirPerm, nil)
	assert.True(t, dir.IsDir)
	assert.False(t, dir.IsRegular)
	assert.False(t, dir.IsHidden)
	assert.Equal(t, defaultDirPerm, dir.Permissions, "the default permissions of a directory")
	assert.Nil(t, dir.Sys)
}
