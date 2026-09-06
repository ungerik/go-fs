package fstest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
)

// Seed describes the directory tree the conformance suite expects
// below Config.TestDir.
//
// Keys are slash separated paths relative to TestDir, values are the
// file contents. Directories are implied by the paths of the files.
type Seed map[string][]byte

// DefaultSeed returns the tree used by RunConformance when
// Config.Seed is nil. It contains an empty file, a hidden file
// and two levels of sub directories.
func DefaultSeed() Seed {
	return Seed{
		"hello.txt":          []byte("Hello, World!"),
		"empty.txt":          nil,
		".hidden.txt":        []byte("hidden"),
		"sub/nested.txt":     []byte("nested content"),
		"sub/deeper/leaf.md": []byte("# leaf"),
	}
}

// FlatSeed returns a Seed without sub directories
// for file systems that only support a single directory level.
func FlatSeed() Seed {
	return Seed{
		"hello.txt":   []byte("Hello, World!"),
		"empty.txt":   nil,
		".hidden.txt": []byte("hidden"),
	}
}

// dirs returns the sorted set of directories implied by the seed,
// relative to TestDir and without the root itself.
func (s Seed) dirs() []string {
	set := map[string]bool{}
	for name := range s {
		for dir := parentDir(name); dir != ""; dir = parentDir(dir) {
			set[dir] = true
		}
	}
	dirs := make([]string, 0, len(set))
	for dir := range set {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// entriesOf returns the sorted names of the direct children of dir
// (relative to TestDir, "" for TestDir itself) with their directory flag.
func (s Seed) entriesOf(dir string) (files, subDirs []string) {
	seen := map[string]bool{}
	for name := range s {
		if parentDir(name) == dir {
			files = append(files, baseName(name))
		}
	}
	for _, d := range s.dirs() {
		if parentDir(d) == dir && !seen[baseName(d)] {
			seen[baseName(d)] = true
			subDirs = append(subDirs, baseName(d))
		}
	}
	sort.Strings(files)
	sort.Strings(subDirs)
	return files, subDirs
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Config configures RunConformance.
type Config struct {
	// Name is the expected result of FileSystem.Name.
	// Not checked if empty.
	Name string

	// Prefix is the expected result of FileSystem.Prefix.
	// Not checked if empty.
	Prefix string

	// TestDir is the file system path (without prefix) of the
	// directory the suite works in.
	//
	// For writable file systems it must exist and be empty;
	// the suite creates the Seed and its own files below it
	// and removes them again at the end.
	//
	// For read-only file systems TestDir must already contain
	// exactly the Seed tree.
	TestDir string

	// Seed is the directory tree the suite expects (read-only file
	// systems) or creates (writable file systems) below TestDir.
	// DefaultSeed() is used if nil.
	Seed Seed

	// NoDirectories marks file systems without a directory concept
	// (like httpfs): Stat of TestDir and directory listings are
	// not checked.
	NoDirectories bool

	// SkipClose keeps the suite from calling FileSystem.Close at the end.
	SkipClose bool
}

// RunConformance runs the go-fs conformance suite against a FileSystem.
//
// The suite verifies the metadata and path methods, then reads the Seed
// tree through the FileSystem methods and through the high level File
// API (the file system is registered for the duration of the test if it
// is not already), and for writable file systems additionally exercises
// every write related method including the optional interfaces, checking
// content, not just existence. It also checks the error contract:
// errors.Is(err, os.ErrNotExist) for missing files, os.ErrExist for
// MakeDir on an existing path, fs.ErrReadOnlyFileSystem for write
// operations on read-only file systems, fs.ErrWriteOnlyFileSystem for
// read operations on write-only file systems and fs.ErrFileSystemClosed
// after Close.
func RunConformance(t *testing.T, fileSystem fs.FileSystem, cfg Config) {
	t.Helper()

	require.NotEmpty(t, cfg.TestDir, "Config.TestDir must not be empty")
	if cfg.Seed == nil {
		cfg.Seed = DefaultSeed()
	}
	c := &conformance{t: t, fs: fileSystem, cfg: cfg, ctx: t.Context()}
	c.readable, c.writable = fileSystem.ReadableWritable()

	// The high level File API resolves a FileSystem via the global
	// registry from a File's URI prefix. Make sure the file system under
	// test is registered for the duration of the tests.
	if !fs.IsRegistered(fileSystem) {
		fs.Register(fileSystem)
		t.Cleanup(func() { fs.Unregister(fileSystem) })
	}

	t.Run("Metadata", c.testMetadata)
	t.Run("Paths", c.testPaths)
	t.Run("PatternMatching", c.testPatternMatching)

	if c.writable {
		t.Run("WriteSeed", c.writeSeed)
	}
	if c.readable {
		t.Run("ReadSeed", c.testReadSeed)
		t.Run("ReadErrors", c.testReadErrors)
		if !cfg.NoDirectories {
			t.Run("ListDir", c.testListDir)
		}
		t.Run("FileAPI", c.testFileAPI)
	} else {
		t.Run("WriteOnly", c.testWriteOnly)
	}
	if c.writable {
		if c.readable {
			t.Run("Write", c.testWrite)
			t.Run("WriteErrors", c.testWriteErrors)
			t.Run("OptionalWrite", c.testOptionalWrite)
			t.Run("HighLevelWrite", c.testHighLevelWrite)
		}
		if c.readable {
			t.Run("Cleanup", c.cleanup)
		}
	} else {
		t.Run("ReadOnly", c.testReadOnly)
	}
	if !cfg.SkipClose {
		t.Run("Close", c.testClose)
	}
}

type conformance struct {
	t        *testing.T
	fs       fs.FileSystem
	cfg      Config
	ctx      context.Context
	readable bool
	writable bool
}

// path returns the file system path of a seed relative path.
func (c *conformance) path(rel string) string {
	if rel == "" {
		return c.fs.JoinCleanPath(c.cfg.TestDir)
	}
	return c.fs.JoinCleanPath(append([]string{c.cfg.TestDir}, strings.Split(rel, "/")...)...)
}

// file returns the File of a seed relative path.
func (c *conformance) file(rel string) fs.File {
	if rel == "" {
		return c.fs.JoinCleanFile(c.cfg.TestDir)
	}
	return c.fs.JoinCleanFile(append([]string{c.cfg.TestDir}, strings.Split(rel, "/")...)...)
}

func (c *conformance) readAll(t *testing.T, path string) []byte {
	t.Helper()
	r, err := c.fs.OpenReader(path)
	require.NoError(t, err, "OpenReader(%q)", path)
	data, err := io.ReadAll(r)
	require.NoError(t, err, "io.ReadAll(%q)", path)
	require.NoError(t, r.Close(), "Close reader of %q", path)
	return data
}

func (c *conformance) writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	w, err := c.fs.OpenWriter(path, nil)
	require.NoError(t, err, "OpenWriter(%q)", path)
	n, err := w.Write(data)
	require.NoError(t, err, "Write(%q)", path)
	require.Equal(t, len(data), n, "Write(%q) must write all bytes", path)
	require.NoError(t, w.Close(), "Close writer of %q", path)
}

func (c *conformance) makeDir(t *testing.T, path string) {
	t.Helper()
	if _, err := c.fs.Stat(path); err == nil {
		return
	}
	require.NoError(t, c.fs.MakeDir(path, nil), "MakeDir(%q)", path)
}

func (c *conformance) testMetadata(t *testing.T) {
	if c.cfg.Name != "" {
		assert.Equal(t, c.cfg.Name, c.fs.Name(), "Name()")
	}
	if c.cfg.Prefix != "" {
		assert.Equal(t, c.cfg.Prefix, c.fs.Prefix(), "Prefix()")
	}
	assert.NotEmpty(t, c.fs.Prefix(), "Prefix() must not be empty")
	assert.True(t, strings.HasSuffix(c.fs.Prefix(), fs.PrefixSeparator) || strings.Contains(c.fs.Prefix(), fs.PrefixSeparator),
		"Prefix() %q must contain %q", c.fs.Prefix(), fs.PrefixSeparator)
	assert.NotEmpty(t, c.fs.String(), "String() must not be empty")
	assert.NotEmpty(t, c.fs.Separator(), "Separator() must not be empty")
	assert.True(t, c.readable || c.writable, "file system must be readable or writable")

	id, err := c.fs.ID()
	require.NoError(t, err, "ID()")
	assert.NotEmpty(t, id, "ID() must not be empty")

	if root := c.fs.RootDir(); root != fs.InvalidFile {
		assert.True(t, strings.HasPrefix(root.URL(), c.fs.Prefix()),
			"RootDir() %q must be below Prefix() %q", root, c.fs.Prefix())
		assert.Same(t, c.fs, root.FileSystem(), "RootDir() must resolve to the file system")
	} else {
		t.Log("RootDir() is InvalidFile")
	}
}

func (c *conformance) testPaths(t *testing.T) {
	sep := c.fs.Separator()

	// JoinCleanPath must be pure: it must not modify the passed slice.
	parts := []string{c.fs.Prefix() + "a", "b", "..", "c", "file.txt"}
	partsCopy := slices.Clone(parts)
	joined := c.fs.JoinCleanPath(parts...)
	assert.Equal(t, partsCopy, parts, "JoinCleanPath must not modify the passed uriParts")
	assert.False(t, strings.HasPrefix(joined, c.fs.Prefix()), "JoinCleanPath must strip the prefix: %q", joined)
	assert.NotContains(t, joined, "..", "JoinCleanPath must clean the path: %q", joined)
	assert.True(t, strings.HasSuffix(joined, "a"+sep+"c"+sep+"file.txt"), "JoinCleanPath must join with the separator: %q", joined)

	// JoinCleanFile must agree with JoinCleanPath and carry the prefix.
	file := c.fs.JoinCleanFile("a", "b", "..", "c", "file.txt")
	assert.Equal(t, c.fs.JoinCleanPath("a", "c", "file.txt"), file.Path(), "JoinCleanFile().Path() must equal JoinCleanPath()")
	assert.True(t, strings.HasPrefix(file.URL(), c.fs.Prefix()), "JoinCleanFile().URL() %q must have prefix %q", file.URL(), c.fs.Prefix())
	assert.Same(t, c.fs, file.FileSystem(), "JoinCleanFile() must resolve to the file system")

	// URL and CleanPathFromURI must be inverse operations.
	testPath := c.fs.JoinCleanPath(c.cfg.TestDir, "x", "y.txt")
	url := c.fs.URL(testPath)
	assert.True(t, strings.HasPrefix(url, c.fs.Prefix()), "URL() %q must have prefix %q", url, c.fs.Prefix())
	assert.Equal(t, testPath, c.fs.CleanPathFromURI(url), "CleanPathFromURI(URL(p)) must return p")
	assert.Equal(t, testPath, c.fs.CleanPathFromURI(testPath), "CleanPathFromURI of a path without prefix must return the path")

	// SplitPath and SplitDirAndName
	parts = c.fs.SplitPath(testPath)
	require.NotEmpty(t, parts, "SplitPath")
	assert.Equal(t, "y.txt", parts[len(parts)-1], "SplitPath last element")
	assert.Equal(t, "x", parts[len(parts)-2], "SplitPath second to last element")
	dir, name := c.fs.SplitDirAndName(testPath)
	assert.Equal(t, "y.txt", name, "SplitDirAndName name")
	assert.Equal(t, c.fs.JoinCleanPath(c.cfg.TestDir, "x"), dir, "SplitDirAndName dir")
	assert.Equal(t, testPath, c.fs.JoinCleanPath(dir, name), "JoinCleanPath(SplitDirAndName(p)) must return p")

	// AbsPath must be idempotent
	abs := c.fs.AbsPath(testPath)
	assert.NotEmpty(t, abs, "AbsPath")
	assert.Equal(t, abs, c.fs.AbsPath(abs), "AbsPath must be idempotent")
	assert.True(t, c.fs.IsAbsPath(abs), "IsAbsPath(AbsPath(p)) must be true")
}

func (c *conformance) testPatternMatching(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{"file.txt", []string{"*.txt"}, true},
		{"file.txt", []string{"*.go"}, false},
		{"file.txt", []string{"*.go", "*.txt"}, true},
		{"file.txt", []string{"file.*"}, true},
		{"file.txt", []string{"?ile.txt"}, true},
		{"file.txt", nil, true},
		{"file.txt", []string{}, true},
		{".hidden", []string{"*.txt"}, false},
	}
	for _, tt := range tests {
		got, err := c.fs.MatchAnyPattern(tt.name, tt.patterns)
		require.NoError(t, err, "MatchAnyPattern(%q, %q)", tt.name, tt.patterns)
		assert.Equal(t, tt.want, got, "MatchAnyPattern(%q, %q)", tt.name, tt.patterns)
	}
	_, err := c.fs.MatchAnyPattern("file.txt", []string{"[invalid"})
	assert.Error(t, err, "MatchAnyPattern with a malformed pattern must return an error")
}

// writeSeed creates the seed tree through the FileSystem write methods.
func (c *conformance) writeSeed(t *testing.T) {
	root := c.path("")
	info, err := c.fs.Stat(root)
	switch {
	case err == nil && !c.cfg.NoDirectories:
		require.True(t, info.IsDir(), "Config.TestDir %q must be a directory", root)
	case err != nil && !c.readable:
		// Write-only file systems can't stat, just create the directory
		_ = c.fs.MakeDir(root, nil)
	case err != nil:
		require.ErrorIs(t, err, os.ErrNotExist, "Stat(TestDir)")
		require.NoError(t, c.fs.MakeDir(root, nil), "creating Config.TestDir %q", root)
	}
	for _, dir := range c.cfg.Seed.dirs() {
		c.makeDir(t, c.path(dir))
	}
	for name, content := range c.cfg.Seed {
		c.writeFile(t, c.path(name), content)
	}
}

func (c *conformance) testReadSeed(t *testing.T) {
	if !c.cfg.NoDirectories {
		info, err := c.fs.Stat(c.path(""))
		require.NoError(t, err, "Stat(TestDir)")
		assert.True(t, info.IsDir(), "TestDir must be a directory")
		for _, dir := range c.cfg.Seed.dirs() {
			info, err := c.fs.Stat(c.path(dir))
			require.NoError(t, err, "Stat(%q)", dir)
			assert.True(t, info.IsDir(), "%q must be a directory", dir)
			assert.Equal(t, baseName(dir), info.Name(), "Stat(%q).Name()", dir)
		}
	}
	for name, content := range c.cfg.Seed {
		path := c.path(name)
		info, err := c.fs.Stat(path)
		require.NoError(t, err, "Stat(%q)", name)
		assert.False(t, info.IsDir(), "%q must not be a directory", name)
		assert.Equal(t, int64(len(content)), info.Size(), "Stat(%q).Size()", name)
		assert.Equal(t, baseName(name), info.Name(), "Stat(%q).Name()", name)
		assert.True(t, info.Mode().IsRegular(), "Stat(%q).Mode().IsRegular()", name)

		got := c.readAll(t, path)
		assert.True(t, bytes.Equal(content, got), "OpenReader(%q) content: want %q, got %q", name, content, got)

		if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
			got, err := rafs.ReadAll(c.ctx, path)
			require.NoError(t, err, "ReadAll(%q)", name)
			assert.True(t, bytes.Equal(content, got), "ReadAll(%q) content: want %q, got %q", name, content, got)
		}
		if efs, ok := c.fs.(fs.ExistsFileSystem); ok {
			assert.True(t, efs.Exists(path), "Exists(%q)", name)
		}
		assert.Equal(t, c.fs.IsHidden(path), c.file(name).IsHidden(), "IsHidden(%q) must agree between FileSystem and File", name)
	}
}

func (c *conformance) testReadErrors(t *testing.T) {
	missing := c.path("does-not-exist.txt")

	_, err := c.fs.Stat(missing)
	require.Error(t, err, "Stat of a missing file must fail")
	assert.ErrorIs(t, err, os.ErrNotExist, "Stat of a missing file must wrap os.ErrNotExist")

	_, err = c.fs.OpenReader(missing)
	require.Error(t, err, "OpenReader of a missing file must fail")
	assert.ErrorIs(t, err, os.ErrNotExist, "OpenReader of a missing file must wrap os.ErrNotExist")

	if efs, ok := c.fs.(fs.ExistsFileSystem); ok {
		assert.False(t, efs.Exists(missing), "Exists of a missing file must be false")
	}
	if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
		_, err = rafs.ReadAll(c.ctx, missing)
		require.Error(t, err, "ReadAll of a missing file must fail")
		assert.ErrorIs(t, err, os.ErrNotExist, "ReadAll of a missing file must wrap os.ErrNotExist")

		canceled, cancel := context.WithCancel(c.ctx)
		cancel()
		_, err = rafs.ReadAll(canceled, c.path("hello.txt"))
		assert.Error(t, err, "ReadAll with a canceled context must fail")
	}

	if !c.cfg.NoDirectories {
		// Listing a missing directory must either fail with os.ErrNotExist
		// or, for file systems without real directories, list nothing.
		n := 0
		err = c.fs.ListDirInfo(c.ctx, c.path("does-not-exist-dir"), func(*fs.FileInfo) error { n++; return nil }, nil)
		if err != nil {
			assert.ErrorIs(t, err, os.ErrNotExist, "ListDirInfo of a missing directory must wrap os.ErrNotExist")
		}
		assert.Zero(t, n, "ListDirInfo of a missing directory must not list anything")

		// Listing a file must either fail or list nothing.
		n = 0
		err = c.fs.ListDirInfo(c.ctx, c.path("hello.txt"), func(*fs.FileInfo) error { n++; return nil }, nil)
		if err != nil {
			assert.NotErrorIs(t, err, os.ErrNotExist, "ListDirInfo of a file must not report os.ErrNotExist")
		}
		assert.Zero(t, n, "ListDirInfo of a file must not list anything")
	}
}

func (c *conformance) checkFileInfo(t *testing.T, info *fs.FileInfo, dir string) {
	t.Helper()
	require.NotNil(t, info, "ListDirInfo must not pass nil")
	require.NoError(t, info.Validate(), "FileInfo.Validate")
	assert.True(t, info.Exists, "listed %q must exist", info.Name)
	assert.NotContains(t, info.Name, "/", "FileInfo.Name %q must be a name, not a path", info.Name)
	assert.Equal(t, info.Name, info.File.Name(), "FileInfo.Name must equal FileInfo.File.Name()")
	assert.True(t, strings.HasPrefix(info.File.URL(), c.fs.Prefix()),
		"FileInfo.File %q must have the prefix %q", info.File.URL(), c.fs.Prefix())
	assert.Same(t, c.fs, info.File.FileSystem(), "FileInfo.File must resolve to the file system under test")
	assert.Equal(t, c.fs.JoinCleanPath(c.path(dir), info.Name), info.File.Path(), "FileInfo.File.Path()")
	assert.Equal(t, c.fs.IsHidden(info.File.Path()), info.IsHidden, "FileInfo.IsHidden must agree with IsHidden()")

	rel := info.Name
	if dir != "" {
		rel = dir + "/" + info.Name
	}
	if content, ok := c.cfg.Seed[rel]; ok {
		assert.False(t, info.IsDir, "listed file %q must not be a directory", rel)
		assert.True(t, info.IsRegular, "listed file %q must be regular", rel)
		assert.Equal(t, int64(len(content)), info.Size, "FileInfo.Size of %q", rel)
	} else if slices.Contains(c.cfg.Seed.dirs(), rel) {
		assert.True(t, info.IsDir, "listed directory %q must be a directory", rel)
		assert.False(t, info.IsRegular, "listed directory %q must not be regular", rel)
	} else {
		t.Errorf("ListDirInfo(%q) listed unexpected entry %q", dir, info.Name)
	}
	assert.True(t, info.File.Exists(), "listed %q must exist via the File API", info.File)
}

func (c *conformance) listNames(t *testing.T, dir string, patterns []string) (names []string) {
	t.Helper()
	err := c.fs.ListDirInfo(c.ctx, c.path(dir), func(info *fs.FileInfo) error {
		c.checkFileInfo(t, info, dir)
		names = append(names, info.Name)
		return nil
	}, patterns)
	require.NoError(t, err, "ListDirInfo(%q, %q)", dir, patterns)
	sort.Strings(names)
	return names
}

func (c *conformance) testListDir(t *testing.T) {
	seed := c.cfg.Seed
	dirs := append([]string{""}, seed.dirs()...)

	// Every directory of the seed lists exactly its files and sub directories
	for _, dir := range dirs {
		files, subDirs := seed.entriesOf(dir)
		want := append(append([]string{}, files...), subDirs...)
		sort.Strings(want)
		assert.Equal(t, want, c.listNames(t, dir, nil), "ListDirInfo(%q) entries", dir)
	}

	// Patterns filter by name
	files, _ := seed.entriesOf("")
	var wantTxt []string
	for _, f := range files {
		if strings.HasSuffix(f, ".txt") {
			wantTxt = append(wantTxt, f)
		}
	}
	assert.Equal(t, wantTxt, c.listNames(t, "", []string{"*.txt"}), "ListDirInfo with pattern *.txt")
	assert.Empty(t, c.listNames(t, "", []string{"*.nomatch"}), "ListDirInfo with non matching pattern")

	// Returning an error from the callback stops the listing and returns the error
	errStop := errors.New("stop listing")
	n := 0
	err := c.fs.ListDirInfo(c.ctx, c.path(""), func(*fs.FileInfo) error { n++; return errStop }, nil)
	assert.ErrorIs(t, err, errStop, "ListDirInfo must return the callback error")
	assert.Equal(t, 1, n, "ListDirInfo must stop after the callback returned an error")

	// A canceled context stops the listing
	canceled, cancel := context.WithCancel(c.ctx)
	cancel()
	err = c.fs.ListDirInfo(canceled, c.path(""), func(*fs.FileInfo) error { return nil }, nil)
	assert.Error(t, err, "ListDirInfo with a canceled context must fail")

	// ListDirMax
	if ldmfs, ok := c.fs.(fs.ListDirMaxFileSystem); ok {
		all, err := ldmfs.ListDirMax(c.ctx, c.path(""), -1, nil)
		require.NoError(t, err, "ListDirMax(-1)")
		files, subDirs := seed.entriesOf("")
		assert.Len(t, all, len(files)+len(subDirs), "ListDirMax(-1) must list all entries")
		for _, f := range all {
			assert.True(t, strings.HasPrefix(f.URL(), c.fs.Prefix()), "ListDirMax File %q must have the prefix", f)
			assert.True(t, f.Exists(), "ListDirMax File %q must exist", f)
		}
		limited, err := ldmfs.ListDirMax(c.ctx, c.path(""), 2, nil)
		require.NoError(t, err, "ListDirMax(2)")
		assert.Len(t, limited, 2, "ListDirMax(2) must list 2 entries")
		none, err := ldmfs.ListDirMax(c.ctx, c.path(""), 0, nil)
		require.NoError(t, err, "ListDirMax(0)")
		assert.Empty(t, none, "ListDirMax(0) must list nothing")
	}

	// ListDirInfoRecursive lists all files (not directories) of the tree
	if ldrfs, ok := c.fs.(fs.ListDirRecursiveFileSystem); ok {
		var got []string
		err := ldrfs.ListDirInfoRecursive(c.ctx, c.path(""), func(info *fs.FileInfo) error {
			require.NoError(t, info.Validate(), "FileInfo.Validate")
			assert.False(t, info.IsDir, "ListDirInfoRecursive must only list files, got directory %q", info.File)
			assert.True(t, strings.HasPrefix(info.File.URL(), c.fs.Prefix()), "FileInfo.File %q must have the prefix", info.File)
			assert.True(t, info.File.Exists(), "listed %q must exist via the File API", info.File)
			rel, err := relPath(c.path(""), info.File.Path(), c.fs.Separator())
			require.NoError(t, err, "listed %q must be below TestDir", info.File)
			assert.Equal(t, int64(len(seed[rel])), info.Size, "FileInfo.Size of %q", rel)
			got = append(got, rel)
			return nil
		}, nil)
		require.NoError(t, err, "ListDirInfoRecursive")
		sort.Strings(got)
		want := make([]string, 0, len(seed))
		for name := range seed {
			want = append(want, name)
		}
		sort.Strings(want)
		assert.Equal(t, want, got, "ListDirInfoRecursive must list every seed file")

		var mdFiles []string
		err = ldrfs.ListDirInfoRecursive(c.ctx, c.path(""), func(info *fs.FileInfo) error {
			mdFiles = append(mdFiles, info.Name)
			return nil
		}, []string{"*.md"})
		require.NoError(t, err, "ListDirInfoRecursive with pattern")
		var wantMd []string
		for name := range seed {
			if strings.HasSuffix(name, ".md") {
				wantMd = append(wantMd, baseName(name))
			}
		}
		sort.Strings(wantMd)
		sort.Strings(mdFiles)
		assert.Equal(t, wantMd, mdFiles, "ListDirInfoRecursive with pattern *.md")
	}
}

// relPath returns the slash separated path of target relative to base.
func relPath(base, target, sep string) (string, error) {
	if !strings.HasPrefix(target, base) {
		return "", errors.New("not below base")
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(target, base), sep)
	if sep != "/" {
		rel = strings.ReplaceAll(rel, sep, "/")
	}
	return rel, nil
}

// testFileAPI reads the seed through the high level File API.
func (c *conformance) testFileAPI(t *testing.T) {
	canceled, cancel := context.WithCancel(c.ctx)
	cancel()

	if !c.cfg.NoDirectories {
		dir := c.file("")
		assert.True(t, dir.Exists(), "TestDir must exist")
		assert.True(t, dir.IsDir(), "TestDir must be a directory")
		assert.NoError(t, dir.CheckIsDir(), "CheckIsDir(TestDir)")
		assert.False(t, dir.IsEmptyDir(), "TestDir must not be empty")
		for _, sub := range c.cfg.Seed.dirs() {
			assert.True(t, c.file(sub).IsDir(), "%q must be a directory", sub)
			assert.Equal(t, c.file(parentDir(sub)), c.file(sub).Dir(), "Dir() of %q", sub)
		}

		// Iterator and callback listing through the File API
		var names []string
		for f, err := range dir.ListDirIterContext(c.ctx) {
			require.NoError(t, err, "ListDirIter")
			names = append(names, f.Name())
		}
		sort.Strings(names)
		files, subDirs := c.cfg.Seed.entriesOf("")
		want := append(append([]string{}, files...), subDirs...)
		sort.Strings(want)
		assert.Equal(t, want, names, "File.ListDirIter entries")

		var recursive []string
		err := dir.ListDirRecursiveContext(c.ctx, func(f fs.File) error {
			rel, err := relPath(c.path(""), f.Path(), c.fs.Separator())
			require.NoError(t, err)
			recursive = append(recursive, rel)
			return nil
		})
		require.NoError(t, err, "File.ListDirRecursive")
		sort.Strings(recursive)
		wantAll := make([]string, 0, len(c.cfg.Seed))
		for name := range c.cfg.Seed {
			wantAll = append(wantAll, name)
		}
		sort.Strings(wantAll)
		assert.Equal(t, wantAll, recursive, "File.ListDirRecursive must list every seed file")

		max, err := dir.ListDirMaxContext(c.ctx, 1)
		require.NoError(t, err, "File.ListDirMax(1)")
		assert.Len(t, max, 1, "File.ListDirMax(1)")

		_, err = dir.ListDirMaxContext(canceled, -1)
		assert.Error(t, err, "File.ListDirMax with a canceled context must fail")
	}

	for name, content := range c.cfg.Seed {
		f := c.file(name)
		assert.Equal(t, baseName(name), f.Name(), "Name() of %q", name)
		assert.True(t, f.Exists(), "%q must exist", name)
		assert.NoError(t, f.CheckExists(), "CheckExists(%q)", name)
		assert.False(t, f.IsDir(), "%q must not be a directory", name)
		assert.ErrorAs(t, f.CheckIsDir(), new(fs.ErrIsNotDirectory), "CheckIsDir(%q) must return ErrIsNotDirectory", name)
		assert.True(t, f.IsRegular(), "%q must be regular", name)
		assert.True(t, f.IsReadable(), "%q must be readable", name)
		assert.Equal(t, int64(len(content)), f.Size(), "Size() of %q", name)
		assert.Equal(t, c.file(parentDir(name)), f.Dir(), "Dir() of %q", name)

		info := f.Info()
		require.NotNil(t, info)
		assert.True(t, info.Exists, "Info().Exists of %q", name)
		assert.Equal(t, f, info.File, "Info().File of %q", name)
		assert.Equal(t, baseName(name), info.Name, "Info().Name of %q", name)
		assert.Equal(t, int64(len(content)), info.Size, "Info().Size of %q", name)

		stat, err := f.Stat()
		require.NoError(t, err, "Stat() of %q", name)
		assert.Equal(t, info.Size, stat.Size(), "Stat().Size() must equal Info().Size")
		assert.Equal(t, info.Modified, stat.ModTime(), "Stat().ModTime() must equal Info().Modified")
		assert.Equal(t, info.Modified, f.Modified(), "Modified() must equal Info().Modified")
		assert.Equal(t, info.Permissions, f.Permissions(), "Permissions() must equal Info().Permissions")

		data, err := f.ReadAllContext(c.ctx)
		require.NoError(t, err, "ReadAll(%q)", name)
		assert.True(t, bytes.Equal(content, data), "ReadAll(%q) content", name)
		_, err = f.ReadAllContext(canceled)
		assert.Error(t, err, "ReadAll(%q) with a canceled context must fail", name)

		str, err := f.ReadAllStringContext(c.ctx)
		require.NoError(t, err, "ReadAllString(%q)", name)
		assert.Equal(t, string(content), str, "ReadAllString(%q)", name)

		hash, err := f.ContentHashContext(c.ctx)
		require.NoError(t, err, "ContentHash(%q)", name)
		assert.NotEmpty(t, hash, "ContentHash(%q)", name)
		expectedHash, err := fs.DefaultContentHash(c.ctx, bytes.NewReader(content))
		require.NoError(t, err)
		assert.Equal(t, expectedHash, hash, "ContentHash(%q) must equal the hash of the content", name)
		_, err = f.ContentHashContext(canceled)
		assert.Error(t, err, "ContentHash(%q) with a canceled context must fail", name)

		data, hash, err = f.ReadAllContentHash(c.ctx)
		require.NoError(t, err, "ReadAllContentHash(%q)", name)
		assert.True(t, bytes.Equal(content, data), "ReadAllContentHash(%q) content", name)
		assert.Equal(t, expectedHash, hash, "ReadAllContentHash(%q) hash", name)

		var buf bytes.Buffer
		n, err := f.WriteTo(&buf)
		require.NoError(t, err, "WriteTo(%q)", name)
		assert.Equal(t, int64(len(content)), n, "WriteTo(%q) bytes", name)
		assert.True(t, bytes.Equal(content, buf.Bytes()), "WriteTo(%q) content", name)

		c.testReadSeeker(t, f, content)
	}

	missing := c.file("does-not-exist.txt")
	assert.False(t, missing.Exists(), "missing file must not exist")
	assert.ErrorIs(t, missing.CheckExists(), os.ErrNotExist, "CheckExists of a missing file")
	assert.False(t, missing.IsDir(), "missing file must not be a directory")
	assert.False(t, missing.IsReadable(), "missing file must not be readable")
	assert.Zero(t, missing.Size(), "missing file must have size 0")
	assert.False(t, missing.Info().Exists, "Info().Exists of a missing file")
	_, err := missing.Stat()
	assert.ErrorIs(t, err, os.ErrNotExist, "Stat of a missing file")
	_, err = missing.ReadAllContext(c.ctx)
	assert.ErrorIs(t, err, os.ErrNotExist, "ReadAll of a missing file")
	_, err = missing.OpenReader()
	assert.ErrorIs(t, err, os.ErrNotExist, "OpenReader of a missing file")
}

func (c *conformance) testReadSeeker(t *testing.T, f fs.File, content []byte) {
	t.Helper()
	rs, err := f.OpenReadSeeker()
	require.NoError(t, err, "OpenReadSeeker(%s)", f)
	defer rs.Close()

	data, err := io.ReadAll(rs)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, data), "OpenReadSeeker content")

	pos, err := rs.Seek(0, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, int64(0), pos, "Seek(0, SeekStart)")
	data, err = io.ReadAll(rs)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, data), "content after Seek(0, SeekStart)")

	if len(content) < 2 {
		return
	}
	pos, err = rs.Seek(-2, io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)-2), pos, "Seek(-2, SeekCurrent)")
	buf := make([]byte, 1)
	n, err := rs.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, content[len(content)-2:len(content)-1], buf, "byte after Seek(-2, SeekCurrent)")

	pos, err = rs.Seek(-1, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)-1), pos, "Seek(-1, SeekEnd)")
	n, err = rs.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, content[len(content)-1:], buf, "byte after Seek(-1, SeekEnd)")

	n, err = rs.ReadAt(buf, 0)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, content[:1], buf, "ReadAt(0)")
}

// testWriteOnly checks that a write-only file system rejects reads
// with fs.ErrWriteOnlyFileSystem.
func (c *conformance) testWriteOnly(t *testing.T) {
	path := c.path("hello.txt")
	_, err := c.fs.OpenReader(path)
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "OpenReader on a write-only file system")
	err = c.fs.ListDirInfo(c.ctx, c.path(""), func(*fs.FileInfo) error { return nil }, nil)
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "ListDirInfo on a write-only file system")
	if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
		_, err = rafs.ReadAll(c.ctx, path)
		assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "ReadAll on a write-only file system")
	}
	_, err = c.file("hello.txt").ReadAllContext(c.ctx)
	assert.ErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "File.ReadAll on a write-only file system")
}

// testReadOnly checks that a read-only file system rejects writes
// with fs.ErrReadOnlyFileSystem.
func (c *conformance) testReadOnly(t *testing.T) {
	path := c.path("read-only-test.txt")
	_, err := c.fs.OpenWriter(path, nil)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "OpenWriter on a read-only file system")
	_, err = c.fs.OpenReadWriter(path, nil)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "OpenReadWriter on a read-only file system")
	err = c.fs.MakeDir(c.path("read-only-dir"), nil)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "MakeDir on a read-only file system")
	err = c.fs.Remove(c.path("hello.txt"))
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "Remove on a read-only file system")
	if wafs, ok := c.fs.(fs.WriteAllFileSystem); ok {
		err = wafs.WriteAll(c.ctx, path, []byte("x"), nil)
		assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "WriteAll on a read-only file system")
	}
	if tfs, ok := c.fs.(fs.TouchFileSystem); ok {
		err = tfs.Touch(path, nil)
		assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem, "Touch on a read-only file system")
	}
	assert.False(t, c.file("hello.txt").IsWritable(), "File.IsWritable on a read-only file system")
	assert.True(t, c.file("hello.txt").Exists(), "the seed must still exist after rejected writes")
}

func (c *conformance) testWrite(t *testing.T) {
	// OpenWriter must truncate an existing larger file
	long := []byte("0123456789ABCDEFGHIJ")
	short := []byte("xyz")
	shrink := c.path("overwrite-shrink.txt")
	c.writeFile(t, shrink, long)
	c.writeFile(t, shrink, short)
	assert.Equal(t, short, c.readAll(t, shrink), "OpenWriter must truncate previous larger content")

	// Writing in several chunks must append within one writer
	chunked := c.path("chunked.txt")
	w, err := c.fs.OpenWriter(chunked, nil)
	require.NoError(t, err, "OpenWriter")
	for _, chunk := range []string{"first ", "second ", "third"} {
		_, err = w.Write([]byte(chunk))
		require.NoError(t, err, "Write chunk")
	}
	require.NoError(t, w.Close(), "Close writer")
	assert.Equal(t, []byte("first second third"), c.readAll(t, chunked), "chunked writes must be concatenated")

	// Writing into a sub directory
	c.makeDir(t, c.path("write-dir"))
	info, err := c.fs.Stat(c.path("write-dir"))
	require.NoError(t, err, "Stat of created directory")
	assert.True(t, info.IsDir(), "created path must be a directory")
	c.writeFile(t, c.path("write-dir/inner.txt"), []byte("inner"))
	assert.Equal(t, []byte("inner"), c.readAll(t, c.path("write-dir/inner.txt")))

	// Remove a file and a directory
	require.NoError(t, c.fs.Remove(c.path("write-dir/inner.txt")), "Remove file")
	_, err = c.fs.Stat(c.path("write-dir/inner.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist, "removed file must not exist")
	require.NoError(t, c.fs.Remove(c.path("write-dir")), "Remove empty directory")
	_, err = c.fs.Stat(c.path("write-dir"))
	assert.ErrorIs(t, err, os.ErrNotExist, "removed directory must not exist")

	// OpenReadWriter: read, seek, write, and the result must be visible after Close
	rwPath := c.path("readwriter.txt")
	c.writeFile(t, rwPath, []byte("Initial content"))
	rw, err := c.fs.OpenReadWriter(rwPath, nil)
	require.NoError(t, err, "OpenReadWriter")
	buf := make([]byte, 7)
	n, err := rw.Read(buf)
	require.NoError(t, err, "Read")
	assert.Equal(t, 7, n)
	assert.Equal(t, []byte("Initial"), buf, "Read must return the existing content")
	pos, err := rw.Seek(0, io.SeekStart)
	require.NoError(t, err, "Seek")
	assert.Equal(t, int64(0), pos)
	n, err = rw.Write([]byte("Updated"))
	require.NoError(t, err, "Write")
	assert.Equal(t, 7, n)
	require.NoError(t, rw.Close(), "Close read writer")
	assert.Equal(t, []byte("Updated content"), c.readAll(t, rwPath), "OpenReadWriter must not truncate and must persist writes on Close")
}

func (c *conformance) testWriteErrors(t *testing.T) {
	// MakeDir on an existing path must wrap os.ErrExist
	existing := c.path("sub")
	err := c.fs.MakeDir(existing, nil)
	require.Error(t, err, "MakeDir on an existing directory must fail")
	assert.ErrorIs(t, err, os.ErrExist, "MakeDir on an existing directory must wrap os.ErrExist")
	err = c.fs.MakeDir(c.path("hello.txt"), nil)
	require.Error(t, err, "MakeDir on an existing file must fail")
	assert.ErrorIs(t, err, os.ErrExist, "MakeDir on an existing file must wrap os.ErrExist")

	// Remove of a missing path must wrap os.ErrNotExist
	err = c.fs.Remove(c.path("does-not-exist.txt"))
	require.Error(t, err, "Remove of a missing file must fail")
	assert.ErrorIs(t, err, os.ErrNotExist, "Remove of a missing file must wrap os.ErrNotExist")

	// Remove of a non-empty directory must fail and keep the content
	err = c.fs.Remove(existing)
	require.Error(t, err, "Remove of a non-empty directory must fail")
	_, err = c.fs.Stat(c.path("sub/nested.txt"))
	assert.NoError(t, err, "content of a non-empty directory must survive a failed Remove")

	// Writing into a missing directory must fail with os.ErrNotExist
	_, err = c.fs.OpenWriter(c.path("no-such-dir/file.txt"), nil)
	if err == nil {
		// Object stores have no directories and may allow this
		require.NoError(t, c.fs.Remove(c.path("no-such-dir/file.txt")))
	} else {
		assert.ErrorIs(t, err, os.ErrNotExist, "OpenWriter into a missing directory must wrap os.ErrNotExist")
	}
}

func (c *conformance) testOptionalWrite(t *testing.T) {
	if wafs, ok := c.fs.(fs.WriteAllFileSystem); ok {
		path := c.path("writeall.txt")
		require.NoError(t, wafs.WriteAll(c.ctx, path, []byte("0123456789ABCDEFGHIJ"), nil), "WriteAll long")
		require.NoError(t, wafs.WriteAll(c.ctx, path, []byte("xyz"), nil), "WriteAll short")
		assert.Equal(t, []byte("xyz"), c.readAll(t, path), "WriteAll must truncate previous larger content")
		canceled, cancel := context.WithCancel(c.ctx)
		cancel()
		assert.Error(t, wafs.WriteAll(canceled, path, []byte("canceled"), nil), "WriteAll with a canceled context must fail")
		assert.Equal(t, []byte("xyz"), c.readAll(t, path), "WriteAll with a canceled context must not modify the file")
	}

	if afs, ok := c.fs.(fs.AppendFileSystem); ok {
		path := c.path("append.txt")
		require.NoError(t, afs.Append(c.ctx, path, []byte("first\n"), nil), "Append to a new file")
		require.NoError(t, afs.Append(c.ctx, path, []byte("second\n"), nil), "Append to an existing file")
		assert.Equal(t, []byte("first\nsecond\n"), c.readAll(t, path), "Append content")
	}

	if awfs, ok := c.fs.(fs.AppendWriterFileSystem); ok {
		path := c.path("append-writer.txt")
		c.writeFile(t, path, []byte("existing "))
		w, err := awfs.OpenAppendWriter(path, nil)
		require.NoError(t, err, "OpenAppendWriter")
		_, err = w.Write([]byte("appended"))
		require.NoError(t, err, "Write to append writer")
		require.NoError(t, w.Close(), "Close append writer")
		assert.Equal(t, []byte("existing appended"), c.readAll(t, path), "OpenAppendWriter content")
	}

	if tfs, ok := c.fs.(fs.TruncateFileSystem); ok {
		path := c.path("truncate.txt")
		c.writeFile(t, path, []byte("Hello, World!"))
		require.NoError(t, tfs.Truncate(path, 5), "Truncate to smaller size")
		assert.Equal(t, []byte("Hello"), c.readAll(t, path), "Truncate to smaller size content")
		require.NoError(t, tfs.Truncate(path, 8), "Truncate to larger size")
		assert.Equal(t, []byte("Hello\x00\x00\x00"), c.readAll(t, path), "Truncate to larger size must pad with zeros")
	}

	if tfs, ok := c.fs.(fs.TouchFileSystem); ok {
		path := c.path("touch.txt")
		require.NoError(t, tfs.Touch(path, nil), "Touch a new file")
		info, err := c.fs.Stat(path)
		require.NoError(t, err, "Stat touched file")
		assert.False(t, info.IsDir())
		assert.Zero(t, info.Size(), "touched new file must be empty")

		existing := c.path("touch-existing.txt")
		c.writeFile(t, existing, []byte("keep me"))
		err = tfs.Touch(existing, nil)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Logf("Touch of an existing file is unsupported: %v", err)
		} else {
			require.NoError(t, err, "Touch an existing file")
		}
		assert.Equal(t, []byte("keep me"), c.readAll(t, existing), "Touch must not modify the content of an existing file")
	}

	if efs, ok := c.fs.(fs.ExistsFileSystem); ok {
		path := c.path("exists.txt")
		assert.False(t, efs.Exists(path), "Exists before creation")
		c.writeFile(t, path, []byte("x"))
		assert.True(t, efs.Exists(path), "Exists after creation")
		require.NoError(t, c.fs.Remove(path))
		assert.False(t, efs.Exists(path), "Exists after removal")
	}

	if mafs, ok := c.fs.(fs.MakeAllDirsFileSystem); ok {
		nested := c.path("all/dirs/nested")
		require.NoError(t, mafs.MakeAllDirs(nested, nil), "MakeAllDirs")
		info, err := c.fs.Stat(nested)
		require.NoError(t, err, "Stat nested directory")
		assert.True(t, info.IsDir(), "nested path must be a directory")
		assert.NoError(t, mafs.MakeAllDirs(nested, nil), "MakeAllDirs on an existing directory must not fail")
	}

	if cfs, ok := c.fs.(fs.CopyFileSystem); ok {
		src := c.path("copy-src.txt")
		dst := c.path("copy-dst.txt")
		c.writeFile(t, src, []byte("copy me"))
		var buf []byte
		require.NoError(t, cfs.CopyFile(c.ctx, src, dst, &buf), "CopyFile")
		assert.Equal(t, []byte("copy me"), c.readAll(t, dst), "CopyFile destination content")
		assert.Equal(t, []byte("copy me"), c.readAll(t, src), "CopyFile must keep the source")
		require.NoError(t, cfs.CopyFile(c.ctx, src, src, &buf), "CopyFile(src, src) must be a no-op")
		assert.Equal(t, []byte("copy me"), c.readAll(t, src), "CopyFile(src, src) must keep the content")
	}

	if mfs, ok := c.fs.(fs.MoveFileSystem); ok {
		src := c.path("move-src.txt")
		dst := c.path("move-dst.txt")
		c.writeFile(t, src, []byte("move me"))
		require.NoError(t, mfs.Move(src, src), "Move(src, src) must be a no-op")
		assert.Equal(t, []byte("move me"), c.readAll(t, src), "Move(src, src) must keep the content")
		require.NoError(t, mfs.Move(src, dst), "Move")
		assert.Equal(t, []byte("move me"), c.readAll(t, dst), "Move destination content")
		_, err := c.fs.Stat(src)
		assert.ErrorIs(t, err, os.ErrNotExist, "Move source must be gone")
	}

	if rfs, ok := c.fs.(fs.RenameFileSystem); ok {
		src := c.path("rename-src.txt")
		c.writeFile(t, src, []byte("rename me"))
		newPath, err := rfs.Rename(src, "rename-dst.txt")
		require.NoError(t, err, "Rename")
		assert.Equal(t, c.path("rename-dst.txt"), newPath, "Rename must return the new path")
		assert.Equal(t, []byte("rename me"), c.readAll(t, newPath), "Rename destination content")
		_, err = c.fs.Stat(src)
		assert.ErrorIs(t, err, os.ErrNotExist, "Rename source must be gone")
	}

	if slfs, ok := c.fs.(fs.SymbolicLinkFileSystem); ok {
		target := c.path("hello.txt")
		link := c.path("hello-link.txt")
		err := slfs.CreateSymbolicLink(target, link)
		if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, os.ErrPermission) {
			t.Logf("symbolic links not available: %v", err)
		} else {
			require.NoError(t, err, "CreateSymbolicLink")
			assert.True(t, c.fs.IsSymbolicLink(link), "IsSymbolicLink(link)")
			assert.False(t, c.fs.IsSymbolicLink(target), "IsSymbolicLink(target)")
			got, err := slfs.ReadSymbolicLink(link)
			require.NoError(t, err, "ReadSymbolicLink")
			assert.Equal(t, target, got, "ReadSymbolicLink must return the target as stored")
			assert.Equal(t, c.cfg.Seed["hello.txt"], c.readAll(t, link), "reading through the link")
		}
	}

	if xfs, ok := c.fs.(fs.XAttrFileSystem); ok {
		path := c.path("hello.txt")
		err := xfs.SetXAttr(path, "user.gofs-test", []byte("value"), 0, true)
		if err != nil {
			t.Logf("extended attributes not available: %v", err)
		} else {
			names, err := xfs.ListXAttr(path, true)
			require.NoError(t, err, "ListXAttr")
			assert.Contains(t, names, "user.gofs-test", "ListXAttr must contain the set attribute")
			value, err := xfs.GetXAttr(path, "user.gofs-test", true)
			require.NoError(t, err, "GetXAttr")
			assert.Equal(t, []byte("value"), value, "GetXAttr")
			require.NoError(t, xfs.RemoveXAttr(path, "user.gofs-test", true), "RemoveXAttr")
			names, err = xfs.ListXAttr(path, true)
			require.NoError(t, err, "ListXAttr after RemoveXAttr")
			assert.NotContains(t, names, "user.gofs-test", "ListXAttr must not contain the removed attribute")
		}
	}

	if rpfs, ok := c.fs.(fs.RelPathFileSystem); ok {
		rel, err := rpfs.RelPath(c.path(""), c.path("sub/deeper/leaf.md"))
		require.NoError(t, err, "RelPath")
		assert.Equal(t, c.fs.JoinCleanPath("sub", "deeper", "leaf.md"), rel, "RelPath")
		rel, err = rpfs.RelPath(c.path("sub/deeper"), c.path("hello.txt"))
		require.NoError(t, err, "RelPath with parent segments")
		assert.Equal(t, c.fs.JoinCleanPath("..", "..", "hello.txt"), rel, "RelPath with parent segments")
	}

	if pfs, ok := c.fs.(fs.PermissionsFileSystem); ok {
		path := c.path("perm.txt")
		c.writeFile(t, path, []byte("perm"))
		err := pfs.SetPermissions(path, fs.UserRead|fs.UserWrite|fs.GroupRead)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Logf("SetPermissions unsupported: %v", err)
		} else {
			require.NoError(t, err, "SetPermissions")
			info, err := c.fs.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, fs.UserRead|fs.UserWrite|fs.GroupRead, fs.PermissionsFromStdFileInfo(info), "permissions after SetPermissions")
		}
	}
}

// testHighLevelWrite exercises the write paths of the File API,
// which use the generic emulations for file systems without the
// corresponding optional interfaces.
func (c *conformance) testHighLevelWrite(t *testing.T) {
	dir := c.file("")

	// WriteAll must truncate
	f := dir.Join("file-writeall.txt")
	require.NoError(t, f.WriteAllContext(c.ctx, []byte("0123456789ABCDEFGHIJ")), "File.WriteAll long")
	require.NoError(t, f.WriteAllContext(c.ctx, []byte("xyz")), "File.WriteAll short")
	got, err := f.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("xyz"), got, "File.WriteAll must truncate previous larger content")
	assert.True(t, f.IsWritable(), "File.IsWritable of an existing file")
	assert.True(t, dir.Join("new-file.txt").IsWritable(), "File.IsWritable of a new file in an existing directory")

	// Append and AppendString
	require.NoError(t, f.Append(c.ctx, []byte("-appended")), "File.Append")
	require.NoError(t, f.AppendString(c.ctx, "-string"), "File.AppendString")
	got, err = f.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("xyz-appended-string"), got, "File.Append content")

	// OpenAppendWriter
	w, err := f.OpenAppendWriter()
	require.NoError(t, err, "File.OpenAppendWriter")
	_, err = w.Write([]byte("-writer"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	got, err = f.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("xyz-appended-string-writer"), got, "File.OpenAppendWriter content")

	// Truncate
	require.NoError(t, f.Truncate(3), "File.Truncate shrink")
	got, err = f.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("xyz"), got, "File.Truncate shrink content")
	require.NoError(t, f.Truncate(5), "File.Truncate grow")
	got, err = f.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("xyz\x00\x00"), got, "File.Truncate grow content")

	// Touch
	touched := dir.Join("file-touch.txt")
	require.NoError(t, touched.Touch(), "File.Touch new file")
	assert.True(t, touched.Exists(), "touched file must exist")
	assert.Zero(t, touched.Size(), "touched file must be empty")

	// MakeDir and MakeAllDirs
	sub := dir.Join("file-dir")
	require.NoError(t, sub.MakeDir(), "File.MakeDir")
	assert.True(t, sub.IsDir())
	assert.NoError(t, sub.MakeDir(), "File.MakeDir on an existing directory must not fail")
	assert.ErrorAs(t, dir.Join("hello.txt").MakeDir(), new(fs.ErrIsNotDirectory), "File.MakeDir on an existing file")
	nested := dir.Join("file-all", "dirs", "nested")
	require.NoError(t, nested.MakeAllDirs(), "File.MakeAllDirs")
	assert.True(t, nested.IsDir())
	assert.True(t, nested.IsEmptyDir(), "new directory must be empty")

	// ReadFrom
	target := dir.Join("file-readfrom.txt")
	n, err := target.ReadFrom(strings.NewReader("from reader"))
	require.NoError(t, err, "File.ReadFrom")
	assert.Equal(t, int64(len("from reader")), n)
	got, err = target.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("from reader"), got, "File.ReadFrom content")

	// JSON round trip
	type doc struct {
		Name  string `json:"name" xml:"name"`
		Count int    `json:"count" xml:"count"`
	}
	jsonFile := dir.Join("file.json")
	require.NoError(t, jsonFile.WriteJSON(c.ctx, doc{Name: "go-fs", Count: 3}, "  "), "File.WriteJSON")
	var decoded doc
	require.NoError(t, jsonFile.ReadJSON(c.ctx, &decoded), "File.ReadJSON")
	assert.Equal(t, doc{Name: "go-fs", Count: 3}, decoded, "JSON round trip")
	xmlFile := dir.Join("file.xml")
	require.NoError(t, xmlFile.WriteXML(c.ctx, doc{Name: "go-fs", Count: 3}), "File.WriteXML")
	decoded = doc{}
	require.NoError(t, xmlFile.ReadXML(c.ctx, &decoded), "File.ReadXML")
	assert.Equal(t, doc{Name: "go-fs", Count: 3}, decoded, "XML round trip")

	// Rename of a file and of a non-empty directory
	renamed, err := target.Rename("file-renamed.txt")
	require.NoError(t, err, "File.Rename")
	assert.Equal(t, dir.Join("file-renamed.txt"), renamed, "File.Rename result")
	assert.False(t, target.Exists(), "File.Rename source must be gone")
	got, err = renamed.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("from reader"), got, "File.Rename content")

	srcDir := dir.Join("rename-dir-src")
	require.NoError(t, srcDir.MakeDir())
	require.NoError(t, srcDir.Join("child.txt").WriteAllContext(c.ctx, []byte("dir-content")))
	renamedDir, err := srcDir.Rename("rename-dir-dst")
	require.NoError(t, err, "File.Rename of a non-empty directory")
	got, err = renamedDir.Join("child.txt").ReadAllContext(c.ctx)
	require.NoError(t, err, "child must exist at the new location")
	assert.Equal(t, []byte("dir-content"), got, "child content preserved across directory rename")
	assert.False(t, srcDir.Exists(), "source directory must be gone after rename")

	// MoveTo
	moved := dir.Join("file-moved.txt")
	require.NoError(t, renamed.MoveTo(moved), "File.MoveTo")
	assert.False(t, renamed.Exists(), "File.MoveTo source must be gone")
	got, err = moved.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("from reader"), got, "File.MoveTo content")
	require.NoError(t, moved.MoveTo(moved), "File.MoveTo(self) must be a no-op")
	assert.True(t, moved.Exists(), "File.MoveTo(self) must keep the file")

	// CopyFile and CopyRecursive
	copied := dir.Join("file-copied.txt")
	require.NoError(t, fs.CopyFile(c.ctx, moved, copied), "fs.CopyFile")
	got, err = copied.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("from reader"), got, "fs.CopyFile content")
	copiedDir := dir.Join("copied-dir")
	require.NoError(t, fs.CopyRecursive(c.ctx, renamedDir, copiedDir), "fs.CopyRecursive")
	got, err = copiedDir.Join("child.txt").ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("dir-content"), got, "fs.CopyRecursive content")

	// Remove variants
	require.NoError(t, copied.Remove(), "File.Remove")
	assert.False(t, copied.Exists())
	assert.ErrorIs(t, copied.Remove(), os.ErrNotExist, "File.Remove of a missing file")
	require.NoError(t, copiedDir.RemoveRecursive(), "File.RemoveRecursive")
	assert.False(t, copiedDir.Exists(), "removed directory must be gone")
	require.NoError(t, renamedDir.RemoveDirContentsRecursive(), "File.RemoveDirContentsRecursive")
	assert.True(t, renamedDir.IsEmptyDir(), "directory must be empty after RemoveDirContentsRecursive")

	// Cross file system copy into an in-memory file system
	memFS, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	memFile := memFS.RootDir().Join("copied.txt")
	require.NoError(t, fs.CopyFile(c.ctx, moved, memFile), "fs.CopyFile to another file system")
	got, err = memFile.ReadAllContext(c.ctx)
	require.NoError(t, err)
	assert.Equal(t, []byte("from reader"), got, "cross file system copy content")
	identical, err := fs.IdenticalFileContents(c.ctx, moved, memFile)
	require.NoError(t, err)
	assert.True(t, identical, "IdenticalFileContents across file systems")
}

// cleanup removes everything the suite created below TestDir.
func (c *conformance) cleanup(t *testing.T) {
	err := c.file("").RemoveDirContentsRecursive()
	require.NoError(t, err, "removing the suite files below TestDir")
	if !c.cfg.NoDirectories {
		assert.True(t, c.file("").IsEmptyDir(), "TestDir must be empty after cleanup")
	}
}

func (c *conformance) testClose(t *testing.T) {
	registered := fs.IsRegistered(c.fs)
	require.NoError(t, c.fs.Close(), "Close")
	assert.NoError(t, c.fs.Close(), "Close must be idempotent")
	if registered && c.fs != fs.Local {
		assert.False(t, fs.IsRegistered(c.fs), "Close must unregister the file system")
	}
	if _, err := c.fs.Stat(c.path("")); err != nil {
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "errors after Close must wrap fs.ErrFileSystemClosed")
	}
}
