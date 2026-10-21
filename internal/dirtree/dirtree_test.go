package dirtree

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
