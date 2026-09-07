package fs_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// sliceTestFiles creates a temp directory with the sub directory "sub"
// and three files whose name, size and modification time orders all
// differ, so a sort test can only pass with the right sort key:
//
//	name   size  modified
//	a.txt  3     +2s
//	b.txt  1     +0s
//	c.txt  2     +1s
//
// It returns the directory and the files in the order [a, b, c, sub].
func sliceTestFiles(t *testing.T) (dir fs.File, files []fs.File) {
	t.Helper()
	ctx := t.Context()
	dir = fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, dir.RemoveRecursive(context.Background()))
	})
	subDir := dir.Join("sub")
	require.NoError(t, subDir.MakeDir())

	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range []struct {
		name    string
		content string
		offset  time.Duration
	}{
		{"a.txt", "xxx", 2 * time.Second},
		{"b.txt", "x", 0},
		{"c.txt", "xx", 1 * time.Second},
	} {
		file := dir.Join(f.name)
		require.NoError(t, file.WriteAllString(ctx, f.content))
		mod := base.Add(f.offset)
		require.NoError(t, os.Chtimes(file.LocalPath(), mod, mod))
		files = append(files, file)
	}
	// The directory is modified before all files so that the
	// dirs-first sorts can't accidentally pass by time order.
	require.NoError(t, os.Chtimes(subDir.LocalPath(), base.Add(-time.Second), base.Add(-time.Second)))
	return dir, append(files, subDir)
}

func TestNameIndexAndContainsName(t *testing.T) {
	files := []fs.MemFile{
		fs.NewMemFile("a.txt", []byte("a")),
		fs.NewMemFile("b.txt", []byte("bb")),
	}
	assert.Equal(t, 1, fs.NameIndex(files, "b.txt"))
	assert.Equal(t, -1, fs.NameIndex(files, "missing.txt"), "no match must return -1")
	assert.Equal(t, -1, fs.NameIndex([]fs.MemFile(nil), "a.txt"), "empty slice must return -1")

	assert.True(t, fs.ContainsName(files, "a.txt"))
	assert.False(t, fs.ContainsName(files, "missing.txt"))
	assert.False(t, fs.ContainsName([]fs.MemFile(nil), "a.txt"))
}

func TestLocalPathIndexAndContainsLocalPath(t *testing.T) {
	dir, files := sliceTestFiles(t)
	files = files[:3]
	bPath := dir.Join("b.txt").LocalPath()

	assert.Equal(t, 1, fs.LocalPathIndex(files, bPath))
	assert.Equal(t, -1, fs.LocalPathIndex(files, dir.Join("missing.txt").LocalPath()))
	assert.True(t, fs.ContainsLocalPath(files, bPath))
	assert.False(t, fs.ContainsLocalPath(files, dir.Join("missing.txt").LocalPath()))
}

// TestContentHashIndex documents that the index is found by comparing
// content hashes, not names, and that a hashing error is reported
// instead of being swallowed as "not found".
func TestContentHashIndex(t *testing.T) {
	ctx := t.Context()
	files := []fs.MemFile{
		fs.NewMemFile("a.txt", []byte("a")),
		fs.NewMemFile("b.txt", []byte("bb")),
	}
	hashB, err := files[1].ContentHash(ctx)
	require.NoError(t, err)

	i, err := fs.ContentHashIndex(ctx, files, hashB)
	require.NoError(t, err)
	assert.Equal(t, 1, i)

	i, err = fs.ContentHashIndex(ctx, files, "0000000000000000000000000000000000000000")
	require.NoError(t, err)
	assert.Equal(t, -1, i, "unknown hash must return -1 without an error")

	dir, _ := sliceTestFiles(t)
	i, err = fs.ContentHashIndex(ctx, []fs.File{dir.Join("missing.txt")}, hashB)
	require.Error(t, err, "hashing a missing file must report the error")
	assert.Equal(t, -1, i)
}

func TestNotExistsIndexAndAllExist(t *testing.T) {
	dir, files := sliceTestFiles(t)
	files = files[:3]

	assert.Equal(t, -1, fs.NotExistsIndex(files))
	assert.True(t, fs.AllExist(files))

	withMissing := append([]fs.File{dir.Join("missing.txt")}, files...)
	assert.Equal(t, 0, fs.NotExistsIndex(withMissing))
	assert.False(t, fs.AllExist(withMissing))
}

func TestFileURLsPathsNames(t *testing.T) {
	dir, files := sliceTestFiles(t)
	files = files[:3]

	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, fs.FileNames(files))
	assert.Equal(t, []string{
		dir.Join("a.txt").Path(),
		dir.Join("b.txt").Path(),
		dir.Join("c.txt").Path(),
	}, fs.FilePaths(files))
	assert.Equal(t, []string{
		dir.Join("a.txt").URL(),
		dir.Join("b.txt").URL(),
		dir.Join("c.txt").URL(),
	}, fs.FileURLs(files))

	assert.Empty(t, fs.FileNames([]fs.File(nil)))
	assert.Empty(t, fs.FilePaths([]fs.File(nil)))
	assert.Empty(t, fs.FileURLs([]fs.File(nil)))
}

// TestSortBy verifies that every sort function orders by its own key.
// The fixture is built so that the name, size and time orders differ,
// which is what makes a wrong sort key detectable at all.
func TestSortBy(t *testing.T) {
	_, files := sliceTestFiles(t)
	a, b, c, subDir := files[0], files[1], files[2], files[3]

	t.Run("SortByName", func(t *testing.T) {
		s := []fs.File{c, a, b}
		fs.SortByName(s)
		assert.Equal(t, []fs.File{a, b, c}, s)
	})

	t.Run("SortByPath", func(t *testing.T) {
		s := []fs.File{c, a, b}
		fs.SortByPath(s)
		assert.Equal(t, []fs.File{a, b, c}, s)
	})

	t.Run("SortByLocalPath", func(t *testing.T) {
		s := []fs.File{c, a, b}
		fs.SortByLocalPath(s)
		assert.Equal(t, []fs.File{a, b, c}, s)
	})

	t.Run("SortBySize", func(t *testing.T) {
		s := []fs.File{a, c, b}
		fs.SortBySize(s)
		assert.Equal(t, []fs.File{b, c, a}, s, "1, 2, 3 bytes")
	})

	t.Run("SortByModified", func(t *testing.T) {
		s := []fs.File{a, c, b}
		fs.SortByModified(s)
		assert.Equal(t, []fs.File{b, c, a}, s, "oldest first")
	})

	t.Run("SortByNameDirsFirst", func(t *testing.T) {
		s := []fs.File{c, a, subDir, b}
		fs.SortByNameDirsFirst(s)
		assert.Equal(t, []fs.File{subDir, a, b, c}, s, "the directory must sort before all files")
	})

	t.Run("SortByModifiedDirsFirst", func(t *testing.T) {
		s := []fs.File{a, subDir, c, b}
		fs.SortByModifiedDirsFirst(s)
		assert.Equal(t, []fs.File{subDir, b, c, a}, s, "the directory must sort before all files")
	})
}

// TestSortByModifiedGeneric compiles SortByModified with a type
// other than fs.File. Before v1 the function was declared generic
// but sorted a []File parameter, so it could only be used with
// fs.File slices.
func TestSortByModifiedGeneric(t *testing.T) {
	older := modified(time.Unix(1, 0))
	newer := modified(time.Unix(2, 0))
	s := []modified{newer, older}
	fs.SortByModified(s)
	assert.Equal(t, []modified{older, newer}, s)
}

type modified time.Time

func (m modified) Modified() time.Time { return time.Time(m) }
