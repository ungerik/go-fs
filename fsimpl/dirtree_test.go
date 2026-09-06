package fsimpl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDirTree_ImplicitDirectories verifies that the directories along an
// entry path are created implicitly, because archives usually only
// contain the file entries.
func TestDirTree_ImplicitDirectories(t *testing.T) {
	mod := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	tree := NewDirTree()
	require.NoError(t, tree.Add("docs/sub/a.txt", mod, 3))
	// Leading slashes and "." elements must not create extra nodes
	require.NoError(t, tree.Add("/./docs/b.txt", mod, 5))
	// A trailing slash marks an explicit directory entry
	require.NoError(t, tree.Add("empty/", mod, 0))

	docs := tree.Lookup("docs")
	require.NotNil(t, docs)
	assert.True(t, docs.IsDir, "implicitly created directory")
	assert.Equal(t, "docs", docs.Path)

	a := tree.Lookup("/docs/sub/a.txt")
	require.NotNil(t, a, "leading and trailing slashes must be ignored")
	assert.False(t, a.IsDir)
	assert.Equal(t, int64(3), a.Size)
	assert.Equal(t, "docs/sub/a.txt", a.Path)
	assert.Equal(t, mod, a.Modified)

	empty := tree.Lookup("empty")
	require.NotNil(t, empty)
	assert.True(t, empty.IsDir, "an entry name ending with a slash is a directory")

	assert.Nil(t, tree.Lookup("docs/missing.txt"), "unknown path")
	assert.Nil(t, tree.Lookup("docs/a.txt/deeper"), "path below a file")
	assert.Same(t, tree, tree.Lookup("/"), "the root path is the tree itself")
	assert.Same(t, tree, tree.Lookup("."), "a dot path is the tree itself")
}

// TestDirTree_KindConflict verifies that a malformed archive using the
// same name for a file and a directory is reported instead of silently
// producing a tree where one of the two entries is unreachable.
func TestDirTree_KindConflict(t *testing.T) {
	mod := time.Time{}

	fileFirst := NewDirTree()
	require.NoError(t, fileFirst.Add("a", mod, 1))
	err := fileFirst.Add("a/b.txt", mod, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file", "the existing node is a file")

	dirFirst := NewDirTree()
	require.NoError(t, dirFirst.Add("a/b.txt", mod, 1))
	err = dirFirst.Add("a", mod, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directory", "the existing node is a directory")

	// Adding the same entry twice keeps the first node
	again := NewDirTree()
	require.NoError(t, again.Add("a.txt", mod, 1))
	require.NoError(t, again.Add("a.txt", mod, 99))
	assert.Equal(t, int64(1), again.Lookup("a.txt").Size, "the first entry wins")
}

// TestDirTree_SortedChildren verifies the listing order: directories
// first, then files, both by name.
func TestDirTree_SortedChildren(t *testing.T) {
	mod := time.Time{}
	tree := NewDirTree()
	require.NoError(t, tree.Add("z.txt", mod, 1))
	require.NoError(t, tree.Add("a.txt", mod, 1))
	require.NoError(t, tree.Add("zdir/x", mod, 1))
	require.NoError(t, tree.Add("adir/x", mod, 1))

	var names []string
	for _, child := range tree.SortedChildren() {
		names = append(names, child.Name)
	}
	assert.Equal(t, []string{"adir", "zdir", "a.txt", "z.txt"}, names)

	assert.Empty(t, tree.Lookup("a.txt").SortedChildren(), "a file has no children")
}
