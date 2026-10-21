package fstest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// Seed describes the directory tree the conformance suite expects
// below Config.TestDir.
//
// Keys are slash separated paths relative to TestDir, values are the
// file contents. Directories are implied by the paths of the files.
//
// The suite derives the files it reads from the seed, so any tree
// works, including an empty one. The read checks that need an existing
// file use the first file by name with content in the least deep
// directory, the relative path checks use the first file of the deepest
// directory.
type Seed map[string][]byte

// DefaultSeed returns the tree used by RunConformance when
// Config.Seed is nil. It contains an empty file, a hidden file,
// a file with URL escapes in its name that must not be decoded,
// and two levels of sub directories.
func DefaultSeed() Seed {
	return Seed{
		"hello.txt":          []byte("Hello, World!"),
		"empty.txt":          nil,
		".hidden.txt":        []byte("hidden"),
		escapedSeedName:      []byte("escaped name"),
		"sub/nested.txt":     []byte("nested content"),
		"sub/deeper/leaf.md": []byte("# leaf"),
	}
}

// FlatSeed returns DefaultSeed without the sub directories
// for file systems that only support a single directory level.
func FlatSeed() Seed {
	return Seed{
		"hello.txt":     []byte("Hello, World!"),
		"empty.txt":     nil,
		".hidden.txt":   []byte("hidden"),
		escapedSeedName: []byte("escaped name"),
	}
}

// escapedSeedName is a file name that looks URL escaped.
// It must keep its name, a file named "100% a b.txt" is another file.
const escapedSeedName = "100%25 a%20b.txt"

// files returns the sorted names of the seed files.
func (s Seed) files() []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// sampleFile returns the seed file that the checks needing an existing
// file use: the first file by name with content in the least deep
// directory, else the first empty file, else "" for a seed without files.
func (s Seed) sampleFile() string {
	sample := ""
	for _, name := range s.files() {
		switch {
		case sample == "":
			sample = name
		case len(s[sample]) == 0 && len(s[name]) > 0:
			sample = name
		case len(s[name]) > 0 && depth(name) < depth(sample):
			sample = name
		}
	}
	return sample
}

// deepestFile returns the first file by name in the deepest
// directory of the seed, or "" for a seed without files.
func (s Seed) deepestFile() string {
	deepest := ""
	for _, name := range s.files() {
		if deepest == "" || depth(name) > depth(deepest) {
			deepest = name
		}
	}
	return deepest
}

// unusedName returns base, or base with a number appended,
// so that it is not the name of a top level entry of the seed.
func (s Seed) unusedName(base string) string {
	used := map[string]bool{}
	for name := range s {
		top, _, _ := strings.Cut(name, "/")
		used[top] = true
	}
	name := base
	for i := 1; used[name]; i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}

func depth(p string) int {
	return strings.Count(p, "/")
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
	// For writable file systems it must be empty and either exist
	// or have an existing parent directory; the suite creates the
	// Seed and its own files below it and removes them again at the end.
	//
	// For read-only file systems TestDir must already contain
	// exactly the Seed tree.
	TestDir string

	// Seed is the directory tree the suite expects (read-only file
	// systems) or creates (writable file systems) below TestDir.
	// DefaultSeed() is used if nil.
	Seed Seed

	// NoDirectories marks read-only file systems without a directory
	// concept (like httpfs): Stat of TestDir, directory listings and
	// the directory error checks are skipped.
	// The write checks need directories.
	NoDirectories bool

	// SkipClose keeps the suite from calling FileSystem.Close at the end.
	SkipClose bool

	// PermissionMask are the permission bits the file system stores;
	// SetPermissions is only checked for these bits. Zero means all bits.
	// SMB shares for example only keep a read-only flag, so their
	// mask is fs.UserWrite.
	PermissionMask fs.Permissions

	// Skip is called with the name of every sub-test before it runs,
	// relative to the test that called RunConformance, like "Close"
	// or "Rename/Overwrite". The sub-test and everything below it
	// is skipped if Skip returns true. Nil runs every sub-test.
	//
	// Use it to opt out of a check temporarily, for example after
	// an update of go-fs added a check that the file system doesn't
	// pass yet.
	Skip func(testName string) bool
}

// RunConformance runs the go-fs conformance suite against a FileSystem.
//
// The suite verifies the metadata and path methods, then reads the Seed
// tree through the FileSystem methods and through the high level File
// API (the file system is registered for the duration of the test if it
// is not already), and for writable file systems additionally exercises
// every write related method including the optional interfaces, checking
// content, not just existence.
//
// It also checks the error contract of fs.FileSystem: errors.Is(err,
// os.ErrNotExist) for missing files, os.ErrExist for MakeDir on an
// existing path, fs.ErrIsDirectory, fs.ErrIsNotDirectory and
// fs.ErrDirectoryNotEmpty where they apply, context.Canceled for a
// canceled context, fs.ErrReadOnlyFileSystem for write operations on
// read-only file systems, fs.ErrWriteOnlyFileSystem for read operations
// on write-only file systems and fs.ErrFileSystemClosed for every
// operation after Close, as well as the overwrite semantics of Move,
// Rename and CopyFile.
//
// Every check is a named sub-test, see Config.Skip.
func RunConformance(t *testing.T, fileSystem fs.FileSystem, cfg Config) {
	t.Helper()

	mustTrue(t, cfg.TestDir != "", "Config.TestDir must not be empty")
	if cfg.Seed == nil {
		cfg.Seed = DefaultSeed()
	}
	c := &conformance{
		t:     t,
		fs:    fileSystem,
		cfg:   cfg,
		ctx:   t.Context(),
		names: map[*testing.T]string{},
		work:  cfg.Seed.unusedName("fstest-work"),
	}
	c.readable, c.writable = fileSystem.ReadableWritable()
	c.writeFS, _ = fileSystem.(fs.WriteFileSystem)
	if c.writable {
		mustTrue(t, c.writeFS != nil, "a writable file system must implement fs.WriteFileSystem")
	}

	// The high level File API resolves a FileSystem via the global
	// registry from a File's URI prefix. Make sure the file system under
	// test is registered for the duration of the tests.
	if !fs.IsRegistered(fileSystem) {
		mustNoError(t, fs.Register(fileSystem), "Register")
		t.Cleanup(func() { fs.Unregister(fileSystem) })
	}

	c.run(t, "Metadata", c.testMetadata)
	c.run(t, "Paths", c.testPaths)

	if c.writable {
		c.run(t, "WriteSeed", c.writeSeed)
	}
	if c.readable {
		c.run(t, "ReadSeed", c.testReadSeed)
		c.run(t, "ReadErrors", c.testReadErrors)
		if !cfg.NoDirectories {
			c.run(t, "ListDir", c.testListDir)
		}
		c.run(t, "FileAPI", c.testFileAPI)
		c.run(t, "Lstat", c.testLstat)
		c.run(t, "Canceled", c.testCanceled)
	} else {
		c.run(t, "WriteOnly", c.testWriteOnly)
	}
	if c.writable {
		if c.readable {
			c.run(t, "Write", c.testWrite)
			c.run(t, "LargeContent", c.testLargeContent)
			c.run(t, "WriteErrors", c.testWriteErrors)
			c.run(t, "OptionalWrite", c.testOptionalWrite)
			c.run(t, "MoveFileSystem", c.testMoveFileSystem)
			c.run(t, "CopyFileSystem", c.testCopyFileSystem)
			c.run(t, "Rename", c.testRename)
			c.run(t, "MoveTo", c.testMoveTo)
			c.run(t, "CopyFile", c.testCopyFile)
			c.run(t, "Abort", c.testAbort)
			c.run(t, "HighLevelWrite", c.testHighLevelWrite)
			c.run(t, "Cleanup", c.cleanup)
		}
	} else {
		c.run(t, "ReadOnly", c.testReadOnly)
	}
	if !cfg.SkipClose {
		c.run(t, "Close", c.testClose)
	}
}

type conformance struct {
	t        *testing.T
	fs       fs.FileSystem
	writeFS  fs.WriteFileSystem // nil if not implemented
	cfg      Config
	ctx      context.Context
	readable bool
	writable bool

	// work is the directory below TestDir
	// where the write checks create their files
	work        string
	workCreated bool

	namesMtx sync.Mutex
	names    map[*testing.T]string // sub-test names relative to t
}

// run runs test as sub-test name of t unless Config.Skip skips it.
func (c *conformance) run(t *testing.T, name string, test func(*testing.T)) bool {
	t.Helper()
	fullName := name
	c.namesMtx.Lock()
	if parent := c.names[t]; parent != "" {
		fullName = parent + "/" + name
	}
	c.namesMtx.Unlock()
	return t.Run(name, func(t *testing.T) {
		c.namesMtx.Lock()
		c.names[t] = fullName
		c.namesMtx.Unlock()
		if c.cfg.Skip != nil && c.cfg.Skip(fullName) {
			t.Skipf("skipped by Config.Skip(%q)", fullName)
		}
		test(t)
	})
}

// isLocalFS reports if the file system under test is fs.Local,
// whose Files are plain local paths without prefix.
func (c *conformance) isLocalFS() bool {
	return c.fs == fs.FileSystem(fs.Local)
}

// path returns the file system path of a seed relative path.
func (c *conformance) path(rel string) string {
	if rel == "" {
		return c.fs.CleanPath(c.cfg.TestDir)
	}
	return c.fs.CleanPath(append([]string{c.cfg.TestDir}, strings.Split(rel, "/")...)...)
}

// file returns the File of a seed relative path.
func (c *conformance) file(rel string) fs.File {
	return c.fileOf(c.path(rel))
}

// fileOf returns the File of a file system path.
func (c *conformance) fileOf(cleanPath string) fs.File {
	if c.isLocalFS() {
		return fs.File(cleanPath) // Local files have no prefix
	}
	return fs.File(c.url(cleanPath))
}

// url returns the URI of a file system path as documented by
// fs.FileSystem: the prefix followed by the path with "%" escaped
// as "%25", or the result of URIFromPath for a fs.URIFileSystem.
func (c *conformance) url(cleanPath string) string {
	if c.isLocalFS() {
		return fs.File(cleanPath).URL()
	}
	if u, ok := c.fs.(fs.URIFileSystem); ok {
		return u.URIFromPath(cleanPath)
	}
	prefix, sep := c.fs.Prefix(), c.fs.Separator()
	if strings.HasSuffix(prefix, sep) && strings.HasPrefix(cleanPath, sep) {
		cleanPath = cleanPath[len(sep):]
	}
	return prefix + fsimpl.EscapeURIPath(cleanPath)
}

// boundaryPrefix returns the longest prefix or prefix alias of fileSystem
// that uri starts with at a boundary as defined by fs.FileSystem,
// or "" if there is none: the URI ends after the prefix, the prefix
// ends with a slash, or a slash or the separator follows the prefix.
func boundaryPrefix(fileSystem fs.FileSystem, uri string) string {
	prefixes := []string{fileSystem.Prefix()}
	if aliasFS, ok := fileSystem.(fs.PrefixAliasFileSystem); ok {
		prefixes = append(prefixes, aliasFS.PrefixAliases()...)
	}
	sep := fileSystem.Separator()
	best := ""
	for _, prefix := range prefixes {
		if prefix == "" || len(prefix) <= len(best) || !strings.HasPrefix(uri, prefix) {
			continue
		}
		rest := uri[len(prefix):]
		if rest == "" ||
			strings.HasSuffix(prefix, "/") ||
			strings.HasPrefix(rest, "/") ||
			(sep != "" && strings.HasPrefix(rest, sep)) {
			best = prefix
		}
	}
	return best
}

// checkURI checks a File that the file system or the package produced:
// its URI must start with the prefix or a prefix alias at a boundary,
// and fs.ParseRawURI must resolve it to the file system under test
// and to wantPath.
func (c *conformance) checkURI(t *testing.T, file fs.File, wantPath, what string) bool {
	t.Helper()
	uri := string(file)
	if !c.isLocalFS() && !checkTrue(t, boundaryPrefix(c.fs, uri) != "",
		"%s %q must start with Prefix() %q or a prefix alias at a boundary: the end of the URI, a prefix ending with a slash, or a slash or the Separator() %q after the prefix",
		what, uri, c.fs.Prefix(), c.fs.Separator()) {
		return false
	}
	parsedFS, parsedPath := fs.ParseRawURI(uri)
	sameFS := checkSameFS(t, c.fs, parsedFS, "fs.ParseRawURI(%q) of %s must return the file system under test", uri, what)
	samePath := checkEqual(t, wantPath, parsedPath, "fs.ParseRawURI(%q) of %s must return the path", uri, what)
	return sameFS && samePath
}

// tryReadAll reads the file at path with OpenReader.
func (c *conformance) tryReadAll(path string) ([]byte, error) {
	r, err := c.fs.OpenReader(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	return data, errors.Join(err, r.Close())
}

func (c *conformance) readAll(t *testing.T, path string) []byte {
	t.Helper()
	data, err := c.tryReadAll(path)
	mustNoError(t, err, "reading %q with OpenReader", path)
	return data
}

func (c *conformance) writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	w, err := c.writeFS.OpenWriter(path, 0)
	mustNoError(t, err, "OpenWriter(%q)", path)
	n, err := w.Write(data)
	mustNoError(t, err, "Write(%q)", path)
	mustEqual(t, len(data), n, "Write(%q) must write all bytes", path)
	mustNoError(t, w.Close(), "Close writer of %q", path)
}

func (c *conformance) makeDir(t *testing.T, path string) {
	t.Helper()
	if _, err := c.fs.Stat(path); err == nil {
		return
	}
	mustNoError(t, c.writeFS.MakeDir(path, 0), "MakeDir(%q)", path)
}

// wpath returns the file system path of a path relative to the work directory.
func (c *conformance) wpath(rel string) string {
	return c.path(c.work + "/" + rel)
}

// wfile returns the File of a path relative to the work directory.
func (c *conformance) wfile(rel string) fs.File {
	return c.file(c.work + "/" + rel)
}

// ensureWork creates the work directory below TestDir.
func (c *conformance) ensureWork(t *testing.T) {
	t.Helper()
	if !c.workCreated {
		c.makeDir(t, c.path(""))
		c.makeDir(t, c.path(c.work))
		c.workCreated = true
	}
}

// mkdir creates the directory rel below the work directory
// with all its parents.
func (c *conformance) mkdir(t *testing.T, rel string) {
	t.Helper()
	c.ensureWork(t)
	dir := ""
	for elem := range strings.SplitSeq(rel, "/") {
		if dir != "" {
			dir += "/"
		}
		dir += elem
		c.makeDir(t, c.wpath(dir))
	}
}

// put writes a file below the work directory, creating its parent directories.
func (c *conformance) put(t *testing.T, rel, content string) {
	t.Helper()
	if dir := parentDir(rel); dir != "" {
		c.mkdir(t, dir)
	} else {
		c.ensureWork(t)
	}
	c.writeFile(t, c.wpath(rel), []byte(content))
}

// checkContent checks the content of a file below the work directory.
func (c *conformance) checkContent(t *testing.T, rel, want, format string, args ...any) bool {
	t.Helper()
	what := fmt.Sprintf(format, args...)
	data, err := c.tryReadAll(c.wpath(rel))
	if !checkNoError(t, err, "%s: reading %q", what, rel) {
		return false
	}
	return checkBytes(t, []byte(want), data, "%s: content of %q", what, rel)
}

// checkMissing checks that a path below the work directory does not exist.
func (c *conformance) checkMissing(t *testing.T, rel, format string, args ...any) bool {
	t.Helper()
	_, err := c.fs.Stat(c.wpath(rel))
	return checkErrorIs(t, err, os.ErrNotExist, "%s: Stat(%q)", fmt.Sprintf(format, args...), rel)
}

// checkIsDir checks that a path below the work directory is a directory.
func (c *conformance) checkIsDir(t *testing.T, rel, format string, args ...any) bool {
	t.Helper()
	what := fmt.Sprintf(format, args...)
	info, err := c.fs.Stat(c.wpath(rel))
	if !checkNoError(t, err, "%s: Stat(%q)", what, rel) {
		return false
	}
	return checkTrue(t, info.IsDir, "%s: %q must be a directory", what, rel)
}

// canceledContext returns a context that is already canceled.
func (c *conformance) canceledContext() context.Context {
	ctx, cancel := context.WithCancel(c.ctx)
	cancel()
	return ctx
}

func (c *conformance) testMetadata(t *testing.T) {
	if c.cfg.Name != "" {
		checkEqual(t, c.cfg.Name, c.fs.Name(), "Name()")
	}
	if c.cfg.Prefix != "" {
		checkEqual(t, c.cfg.Prefix, c.fs.Prefix(), "Prefix()")
	}
	prefix := c.fs.Prefix()
	checkTrue(t, prefix != "", "Prefix() must not be empty")
	checkTrue(t, strings.Contains(prefix, fs.PrefixSeparator), "Prefix() %q must contain %q", prefix, fs.PrefixSeparator)
	checkTrue(t, c.fs.String() != "", "String() must not be empty")
	checkTrue(t, c.fs.Separator() != "", "Separator() must not be empty")
	checkTrue(t, c.readable || c.writable, "the file system must be readable or writable")
	id := c.fs.ID()
	checkTrue(t, id != "", "ID() must not be empty")
	checkEqual(t, id, c.fs.ID(), "ID() must be stable")

	root := c.fs.RootDir()
	if root == fs.InvalidFile {
		t.Log("RootDir() is InvalidFile")
		return
	}
	_, rootPath := fs.ParseRawURI(string(root))
	c.checkURI(t, root, rootPath, "RootDir()")
	checkSameFS(t, c.fs, root.FileSystem(), "RootDir() must resolve to the file system")
}

func (c *conformance) testPaths(t *testing.T) {
	sep := c.fs.Separator()

	// CleanPath must be pure: it must not modify the passed slice.
	parts := []string{c.fs.Prefix() + "a", "b", "..", "c", "file.txt"}
	partsCopy := slices.Clone(parts)
	joined := c.fs.CleanPath(parts...)
	checkStrings(t, partsCopy, parts, "CleanPath must not modify the passed uriParts")
	checkFalse(t, strings.HasPrefix(joined, c.fs.Prefix()), "CleanPath must strip the prefix: %q", joined)
	checkFalse(t, strings.Contains(joined, ".."), "CleanPath must clean the path: %q", joined)
	checkTrue(t, strings.HasSuffix(joined, "a"+sep+"c"+sep+"file.txt"), "CleanPath must join with the separator: %q", joined)
	checkEqual(t, joined, c.fs.CleanPath(joined), "CleanPath must be idempotent")

	// The prefix must be stripped from a URI and the path must survive the round trip
	testPath := c.fs.CleanPath(c.cfg.TestDir, "x", "y.txt")
	uri := c.url(testPath)
	checkTrue(t, strings.HasPrefix(uri, c.fs.Prefix()), "URI %q must have prefix %q", uri, c.fs.Prefix())
	c.checkURI(t, fs.File(uri), testPath, "the URI of a path")
	checkEqual(t, testPath, c.fs.CleanPath(uri), "CleanPath(uri) must return the path")
	checkEqual(t, testPath, fs.File(uri).Path(), "File(uri).Path() must return the path")
	checkEqual(t, uri, fs.File(uri).URL(), "File(uri).URL() must return the URI")
	checkSameFS(t, c.fs, fs.File(uri).FileSystem(), "File(uri) must resolve to the file system")

	// File path methods
	f := c.file("x/y.txt")
	checkEqual(t, uri, f.URL(), "File.URL()")
	checkEqual(t, "y.txt", f.Name(), "File.Name()")
	checkEqual(t, c.file("x"), f.Dir(), "File.Dir()")
	c.checkURI(t, f.Dir(), c.path("x"), "File.Dir()")
	checkEqual(t, f, f.Dir().Join("y.txt"), "Dir().Join(Name()) must return the file")
	checkEqual(t, f, f.Dir().Join("skip", "..", "y.txt"), "Join must clean the path")
	c.checkURI(t, f.Dir().Join("y.txt"), testPath, "File.Join()")
	checkEqual(t, ".txt", f.Ext(), "File.Ext()")

	// Names that look URL escaped must not be decoded on the way
	// from a path to a File and back, the URI escapes every "%".
	escPath := c.fs.CleanPath(c.cfg.TestDir, "50% a%20b", "100%25.txt")
	escURI := c.url(escPath)
	c.checkURI(t, fs.File(escURI), escPath, "the URI of a path with escaped names")
	checkEqual(t, escPath, fs.File(escURI).Path(), "File(uri).Path() must not decode the names of the path")
	checkEqual(t, escURI, fs.File(escURI).URL(), "File(uri).URL() must return the URI with escaped names")
	escFile := c.file("50% a%20b/100%25.txt")
	checkEqual(t, escPath, escFile.Path(), "File.Path() with escaped names")
	checkEqual(t, "100%25.txt", escFile.Name(), "File.Name() must not decode the name")
	checkEqual(t, ".txt", escFile.Ext(), "File.Ext() with an escaped name")
	checkEqual(t, "50% a%20b", escFile.Dir().Name(), "File.Dir().Name() must not decode the name")
	checkEqual(t, escFile, escFile.Dir().Join("100%25.txt"), "Dir().Join(Name()) must return the file with escaped names")

	// Aliases
	if aliasFS, ok := c.fs.(fs.PrefixAliasFileSystem); ok {
		for _, alias := range aliasFS.PrefixAliases() {
			aliasURI := alias + strings.TrimPrefix(uri, c.fs.Prefix())
			c.checkURI(t, fs.File(aliasURI), testPath, "a URI with the prefix alias "+alias)
			checkEqual(t, testPath, fs.File(aliasURI).Path(), "File with alias prefix %q must resolve to the same path", alias)
			checkSameFS(t, c.fs, fs.File(aliasURI).FileSystem(), "File with alias prefix %q must resolve to the file system", alias)
		}
	}
}

// writeSeed creates the seed tree through the FileSystem write methods.
func (c *conformance) writeSeed(t *testing.T) {
	root := c.path("")
	info, err := c.fs.Stat(root)
	switch {
	case err == nil && !c.cfg.NoDirectories:
		mustTrue(t, info.IsDir, "Config.TestDir %q must be a directory", root)
	case err != nil && !c.readable:
		// Write-only file systems can't stat, just create the directory
		_ = c.writeFS.MakeDir(root, 0)
	case err != nil:
		mustErrorIs(t, err, os.ErrNotExist, "Stat(Config.TestDir %q)", root)
		mustNoError(t, c.writeFS.MakeDir(root, 0), "creating Config.TestDir %q", root)
	}
	for _, dir := range c.cfg.Seed.dirs() {
		c.makeDir(t, c.path(dir))
	}
	for _, name := range c.cfg.Seed.files() {
		c.writeFile(t, c.path(name), c.cfg.Seed[name])
	}
}

func (c *conformance) checkStatInfo(t *testing.T, rel string, info *fs.FileInfo) {
	t.Helper()
	mustTrue(t, info != nil, "Stat(%q) must not return a nil FileInfo", rel)
	checkTrue(t, info.Exists, "Stat(%q).Exists", rel)
	if info.File != "" {
		checkEqual(t, c.file(rel), info.File, "Stat(%q).File", rel)
		c.checkURI(t, info.File, c.path(rel), fmt.Sprintf("Stat(%q).File", rel))
	}
	if info.Name != "" || rel != "" {
		checkEqual(t, baseName(rel), info.Name, "Stat(%q).Name", rel)
	}
	checkEqual(t, strings.HasPrefix(baseName(rel), "."), info.IsHidden, "Stat(%q).IsHidden must follow the dot rule", rel)
}

func (c *conformance) testReadSeed(t *testing.T) {
	if !c.cfg.NoDirectories {
		info, err := c.fs.Stat(c.path(""))
		mustNoError(t, err, "Stat(TestDir)")
		checkTrue(t, info.IsDir, "TestDir must be a directory")
		checkFalse(t, info.IsRegular, "TestDir must not be regular")
		for _, dir := range c.cfg.Seed.dirs() {
			info, err := c.fs.Stat(c.path(dir))
			mustNoError(t, err, "Stat(%q)", dir)
			c.checkStatInfo(t, dir, info)
			checkTrue(t, info.IsDir, "%q must be a directory", dir)
			checkFalse(t, info.IsRegular, "%q must not be regular", dir)
		}
	}
	for _, name := range c.cfg.Seed.files() {
		content := c.cfg.Seed[name]
		path := c.path(name)
		info, err := c.fs.Stat(path)
		mustNoError(t, err, "Stat(%q)", name)
		c.checkStatInfo(t, name, info)
		checkFalse(t, info.IsDir, "%q must not be a directory", name)
		checkTrue(t, info.IsRegular, "%q must be regular", name)
		checkFalse(t, info.IsSymlink, "%q must not be a symlink", name)
		checkEqual(t, int64(len(content)), info.Size, "Stat(%q).Size", name)

		checkBytes(t, content, c.readAll(t, path), "OpenReader(%q) content", name)

		if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
			got, err := rafs.ReadAll(c.ctx, path)
			if checkNoError(t, err, "ReadAll(%q)", name) {
				checkBytes(t, content, got, "ReadAll(%q) content", name)
			}
		}
		if efs, ok := c.fs.(fs.ExistsFileSystem); ok {
			exists, err := efs.Exists(path)
			checkNoError(t, err, "Exists(%q)", name)
			checkTrue(t, exists, "Exists(%q)", name)
		}
	}
}

func (c *conformance) testReadErrors(t *testing.T) {
	c.run(t, "NotExist", func(t *testing.T) {
		missing := c.path(c.cfg.Seed.unusedName("does-not-exist.txt"))

		_, err := c.fs.Stat(missing)
		checkErrorIs(t, err, os.ErrNotExist, "Stat of a missing file")

		r, err := c.fs.OpenReader(missing)
		if err == nil {
			_ = r.Close()
		}
		checkErrorIs(t, err, os.ErrNotExist, "OpenReader of a missing file")

		if efs, ok := c.fs.(fs.ExistsFileSystem); ok {
			exists, err := efs.Exists(missing)
			checkNoError(t, err, "Exists of a missing file must not fail")
			checkFalse(t, exists, "Exists of a missing file must be false")
		}
		if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
			_, err = rafs.ReadAll(c.ctx, missing)
			checkErrorIs(t, err, os.ErrNotExist, "ReadAll of a missing file")
		}
		if !c.cfg.NoDirectories {
			n := 0
			err = c.fs.ListDir(c.ctx, c.path(c.cfg.Seed.unusedName("does-not-exist-dir")), nil, func(*fs.FileInfo) error { n++; return nil })
			checkErrorIs(t, err, os.ErrNotExist, "ListDir of a missing directory")
			checkEqual(t, 0, n, "ListDir of a missing directory must not list anything")
		}
	})

	c.run(t, "ListFile", func(t *testing.T) {
		sample := c.cfg.Seed.sampleFile()
		if c.cfg.NoDirectories || sample == "" {
			t.Skip("needs directories and a seed file")
		}
		// Listing a file must either fail or list nothing
		n := 0
		err := c.fs.ListDir(c.ctx, c.path(sample), nil, func(*fs.FileInfo) error { n++; return nil })
		checkNotErrorIs(t, err, os.ErrNotExist, "ListDir of a file must not report os.ErrNotExist")
		checkEqual(t, 0, n, "ListDir of a file must not list anything")
	})

	c.run(t, "IsDirectory", func(t *testing.T) {
		if c.cfg.NoDirectories {
			t.Skip("Config.NoDirectories")
		}
		// Opening a directory may fail or the reading,
		// but either must report fs.ErrIsDirectory
		dir := c.path("")
		_, err := c.tryReadAll(dir)
		checkErrorIs(t, err, fs.ErrIsDirectory{}, "OpenReader of the directory TestDir and reading it")
		if rafs, ok := c.fs.(fs.ReadAllFileSystem); ok {
			_, err = rafs.ReadAll(c.ctx, dir)
			checkErrorIs(t, err, fs.ErrIsDirectory{}, "ReadAll of the directory TestDir")
		}
		_, err = c.file("").ReadAll(c.ctx)
		checkErrorIs(t, err, fs.ErrIsDirectory{}, "File.ReadAll of the directory TestDir")
	})
}

func (c *conformance) checkFileInfo(t *testing.T, info *fs.FileInfo, dir string) {
	t.Helper()
	mustTrue(t, info != nil, "ListDir must not pass a nil FileInfo")
	mustNoError(t, info.Validate(), "FileInfo.Validate")
	checkTrue(t, info.Exists, "listed %q must exist", info.Name)
	checkFalse(t, strings.Contains(info.Name, "/"), "FileInfo.Name %q must be a name, not a path", info.Name)
	checkEqual(t, info.Name, info.File.Name(), "FileInfo.Name must equal FileInfo.File.Name()")
	wantPath := c.fs.CleanPath(c.path(dir), info.Name)
	c.checkURI(t, info.File, wantPath, "ListDir FileInfo.File")
	checkSameFS(t, c.fs, info.File.FileSystem(), "FileInfo.File must resolve to the file system under test")
	checkEqual(t, wantPath, info.File.Path(), "FileInfo.File.Path()")
	checkEqual(t, strings.HasPrefix(info.Name, "."), info.IsHidden, "FileInfo.IsHidden must follow the dot rule")

	rel := info.Name
	if dir != "" {
		rel = dir + "/" + info.Name
	}
	if content, ok := c.cfg.Seed[rel]; ok {
		checkFalse(t, info.IsDir, "listed file %q must not be a directory", rel)
		checkTrue(t, info.IsRegular, "listed file %q must be regular", rel)
		checkEqual(t, int64(len(content)), info.Size, "FileInfo.Size of %q", rel)
	} else if slices.Contains(c.cfg.Seed.dirs(), rel) {
		checkTrue(t, info.IsDir, "listed directory %q must be a directory", rel)
		checkFalse(t, info.IsRegular, "listed directory %q must not be regular", rel)
	} else {
		t.Errorf("ListDir(%q) listed unexpected entry %q", dir, info.Name)
	}
	checkTrue(t, info.File.Exists(), "listed %q must exist via the File API", info.File)
}

func (c *conformance) listNames(t *testing.T, dir string, patterns []string) (names []string) {
	t.Helper()
	err := c.fs.ListDir(c.ctx, c.path(dir), patterns, func(info *fs.FileInfo) error {
		c.checkFileInfo(t, info, dir)
		names = append(names, info.Name)
		return nil
	})
	mustNoError(t, err, "ListDir(%q, %q)", dir, patterns)
	sort.Strings(names)
	return names
}

func (c *conformance) testListDir(t *testing.T) {
	seed := c.cfg.Seed
	dirs := append([]string{""}, seed.dirs()...)

	// Every directory of the seed lists exactly its files and sub directories
	for _, dir := range dirs {
		files, subDirs := seed.entriesOf(dir)
		want := append(slices.Clone(files), subDirs...)
		sort.Strings(want)
		checkStrings(t, want, c.listNames(t, dir, nil), "ListDir(%q) entries", dir)
	}

	// Patterns filter by name
	files, subDirs := seed.entriesOf("")
	var wantTxt []string
	for _, f := range files {
		if strings.HasSuffix(f, ".txt") {
			wantTxt = append(wantTxt, f)
		}
	}
	checkStrings(t, wantTxt, c.listNames(t, "", []string{"*.txt"}), "ListDir with pattern *.txt")
	checkStrings(t, nil, c.listNames(t, "", []string{"*.nomatch"}), "ListDir with non matching pattern")
	_, err := fsimpl.MatchAnyPattern("x", []string{"[invalid"})
	checkError(t, err, "a malformed pattern must be an error")

	// Returning an error from the callback stops the listing and returns the error
	if len(files)+len(subDirs) > 0 {
		errStop := errors.New("stop listing")
		n := 0
		err = c.fs.ListDir(c.ctx, c.path(""), nil, func(*fs.FileInfo) error { n++; return errStop })
		checkErrorIs(t, err, errStop, "ListDir must return the callback error")
		checkEqual(t, 1, n, "ListDir must stop after the callback returned an error")
	}

	// ListDirMax through the File API
	{
		dir := c.file("")
		total := len(files) + len(subDirs)
		all, err := dir.ListDirMax(c.ctx, -1)
		mustNoError(t, err, "File.ListDirMax(-1)")
		checkEqual(t, total, len(all), "File.ListDirMax(-1) must list all entries")
		for _, f := range all {
			c.checkURI(t, f, c.fs.CleanPath(c.path(""), f.Name()), "File.ListDirMax File")
			checkTrue(t, f.Exists(), "File.ListDirMax File %q must exist", f)
		}
		limited, err := dir.ListDirMax(c.ctx, 2)
		mustNoError(t, err, "File.ListDirMax(2)")
		checkEqual(t, min(2, total), len(limited), "File.ListDirMax(2) must list at most 2 entries")
		none, err := dir.ListDirMax(c.ctx, 0)
		mustNoError(t, err, "File.ListDirMax(0)")
		checkEqual(t, 0, len(none), "File.ListDirMax(0) must list nothing")
	}

	// ListDirRecursive lists all files (not directories) of the tree
	if ldrfs, ok := c.fs.(fs.ListDirRecursiveFileSystem); ok {
		var got []string
		err := ldrfs.ListDirRecursive(c.ctx, c.path(""), nil, func(info *fs.FileInfo) error {
			mustNoError(t, info.Validate(), "FileInfo.Validate")
			checkFalse(t, info.IsDir, "ListDirRecursive must only list files, got directory %q", info.File)
			rel, err := relPath(c.path(""), info.File.Path(), c.fs.Separator())
			mustNoError(t, err, "listed %q must be below TestDir", info.File)
			c.checkURI(t, info.File, c.path(rel), "ListDirRecursive FileInfo.File")
			checkTrue(t, info.File.Exists(), "listed %q must exist via the File API", info.File)
			checkEqual(t, int64(len(seed[rel])), info.Size, "FileInfo.Size of %q", rel)
			got = append(got, rel)
			return nil
		})
		mustNoError(t, err, "ListDirRecursive")
		sort.Strings(got)
		checkStrings(t, seed.files(), got, "ListDirRecursive must list every seed file")

		var mdFiles []string
		err = ldrfs.ListDirRecursive(c.ctx, c.path(""), []string{"*.md"}, func(info *fs.FileInfo) error {
			mdFiles = append(mdFiles, info.Name)
			return nil
		})
		mustNoError(t, err, "ListDirRecursive with pattern")
		var wantMd []string
		for name := range seed {
			if strings.HasSuffix(name, ".md") {
				wantMd = append(wantMd, baseName(name))
			}
		}
		sort.Strings(wantMd)
		sort.Strings(mdFiles)
		checkStrings(t, wantMd, mdFiles, "ListDirRecursive with pattern *.md")
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
	seed := c.cfg.Seed
	if !c.cfg.NoDirectories {
		dir := c.file("")
		checkTrue(t, dir.Exists(), "TestDir must exist")
		checkTrue(t, dir.IsDir(), "TestDir must be a directory")
		checkNoError(t, dir.CheckIsDir(), "CheckIsDir(TestDir)")
		checkEqual(t, len(seed) == 0, dir.IsEmptyDir(), "TestDir.IsEmptyDir() must be true only for an empty seed")
		for _, sub := range seed.dirs() {
			checkTrue(t, c.file(sub).IsDir(), "%q must be a directory", sub)
			checkEqual(t, c.file(parentDir(sub)), c.file(sub).Dir(), "Dir() of %q", sub)
		}

		// Iterator and callback listing through the File API
		var names []string
		for f, err := range dir.ListDirIter(c.ctx) {
			mustNoError(t, err, "File.ListDirIter")
			names = append(names, f.Name())
		}
		sort.Strings(names)
		files, subDirs := seed.entriesOf("")
		want := append(slices.Clone(files), subDirs...)
		sort.Strings(want)
		checkStrings(t, want, names, "File.ListDirIter entries")

		var recursive []string
		err := dir.ListDirRecursive(c.ctx, func(f fs.File) error {
			rel, err := relPath(c.path(""), f.Path(), c.fs.Separator())
			mustNoError(t, err, "File.ListDirRecursive file %q must be below TestDir", f)
			recursive = append(recursive, rel)
			return nil
		})
		mustNoError(t, err, "File.ListDirRecursive")
		sort.Strings(recursive)
		checkStrings(t, seed.files(), recursive, "File.ListDirRecursive must list every seed file")

		// Relative paths, from TestDir down to the deepest seed file and
		// from its directory up again to a top level file or to TestDir
		if deepest := seed.deepestFile(); depth(deepest) > 0 {
			sep := c.fs.Separator()
			elems := strings.Split(deepest, "/")
			rel, err := dir.RelPathOf(c.file(deepest))
			mustNoError(t, err, "RelPathOf(%q)", deepest)
			checkEqual(t, strings.Join(elems, sep), rel, "TestDir.RelPathOf(%q)", deepest)
			checkEqual(t, c.file(deepest), dir.Join(rel), "Join(RelPathOf(%q)) must return the target", deepest)

			up := slices.Repeat([]string{".."}, len(elems)-1)
			target := c.file("")
			if sample := seed.sampleFile(); depth(sample) == 0 {
				up = append(up, sample)
				target = c.file(sample)
			}
			rel, err = c.file(parentDir(deepest)).RelPathOf(target)
			mustNoError(t, err, "RelPathOf with parent segments")
			checkEqual(t, strings.Join(up, sep), rel, "%q.RelPathOf(%q) with parent segments", parentDir(deepest), target)
		}
	}

	for _, name := range seed.files() {
		content := seed[name]
		f := c.file(name)
		checkEqual(t, baseName(name), f.Name(), "Name() of %q", name)
		checkTrue(t, f.Exists(), "%q must exist", name)
		checkNoError(t, f.CheckExists(), "CheckExists(%q)", name)
		checkFalse(t, f.IsDir(), "%q must not be a directory", name)
		checkErrorIs(t, f.CheckIsDir(), fs.ErrIsNotDirectory{}, "CheckIsDir(%q)", name)
		checkTrue(t, f.IsRegular(), "%q must be regular", name)
		checkTrue(t, f.IsReadable(), "%q must be readable", name)
		checkFalse(t, f.IsSymbolicLink(), "%q must not be a symbolic link", name)
		checkEqual(t, strings.HasPrefix(baseName(name), "."), f.IsHidden(), "IsHidden(%q) must follow the dot rule", name)
		checkEqual(t, int64(len(content)), f.Size(), "Size() of %q", name)
		checkEqual(t, c.file(parentDir(name)), f.Dir(), "Dir() of %q", name)
		checkTrue(t, f.HasAbsPath(), "%q must have an absolute path", name)
		checkEqual(t, f, f.ToAbsPath(), "ToAbsPath() of an absolute path must be a no-op")

		info := f.Info()
		mustTrue(t, info != nil, "Info() of %q must not be nil", name)
		checkTrue(t, info.Exists, "Info().Exists of %q", name)
		checkEqual(t, f, info.File, "Info().File of %q", name)
		checkEqual(t, baseName(name), info.Name, "Info().Name of %q", name)
		checkEqual(t, int64(len(content)), info.Size, "Info().Size of %q", name)

		stat, err := f.Stat()
		mustNoError(t, err, "Stat() of %q", name)
		checkEqual(t, info.Name, stat.Name(), "Stat().Name() must equal Info().Name")
		checkEqual(t, info.Size, stat.Size(), "Stat().Size() must equal Info().Size")
		checkTrue(t, info.Modified.Equal(stat.ModTime()), "Stat().ModTime() %s must equal Info().Modified %s", stat.ModTime(), info.Modified)
		checkTrue(t, info.Modified.Equal(f.Modified()), "Modified() %s must equal Info().Modified %s", f.Modified(), info.Modified)
		checkEqual(t, info.Permissions, f.Permissions(), "Permissions() must equal Info().Permissions")
		checkTrue(t, stat.Mode().IsRegular(), "Stat().Mode().IsRegular() of %q", name)

		data, err := f.ReadAll(c.ctx)
		if checkNoError(t, err, "File.ReadAll(%q)", name) {
			checkBytes(t, content, data, "File.ReadAll(%q) content", name)
		}

		str, err := f.ReadAllString(c.ctx)
		if checkNoError(t, err, "ReadAllString(%q)", name) {
			checkEqual(t, string(content), str, "ReadAllString(%q)", name)
		}

		hash, err := f.ContentHash(c.ctx)
		mustNoError(t, err, "ContentHash(%q)", name)
		checkTrue(t, hash != "", "ContentHash(%q) must not be empty", name)
		expectedHash, err := fs.DefaultContentHash(c.ctx, strings.NewReader(string(content)))
		mustNoError(t, err, "DefaultContentHash")
		checkEqual(t, expectedHash, hash, "ContentHash(%q) must equal the hash of the content", name)

		data, hash, err = f.ReadAllContentHash(c.ctx)
		if checkNoError(t, err, "ReadAllContentHash(%q)", name) {
			checkBytes(t, content, data, "ReadAllContentHash(%q) content", name)
			checkEqual(t, expectedHash, hash, "ReadAllContentHash(%q) hash", name)
		}

		var buf strings.Builder
		n, err := f.WriteTo(&buf)
		if checkNoError(t, err, "WriteTo(%q)", name) {
			checkEqual(t, int64(len(content)), n, "WriteTo(%q) bytes", name)
			checkEqual(t, string(content), buf.String(), "WriteTo(%q) content", name)
		}

		r, err := f.OpenReader()
		mustNoError(t, err, "File.OpenReader(%q)", name)
		rStat, err := r.Stat()
		if checkNoError(t, err, "File.OpenReader(%q).Stat()", name) {
			checkEqual(t, int64(len(content)), rStat.Size(), "File.OpenReader(%q).Stat().Size()", name)
		}
		checkNoError(t, r.Close(), "closing the reader of %q", name)

		rs, err := f.OpenReadSeeker()
		mustNoError(t, err, "File.OpenReadSeeker(%q)", name)
		checkNoError(t, iotest.TestReader(rs, content), "iotest.TestReader of File.OpenReadSeeker(%q)", name)
		c.checkPastEOF(t, rs, int64(len(content)), fmt.Sprintf("File.OpenReadSeeker(%q)", name))
		checkNoError(t, rs.Close(), "closing the read seeker of %q", name)
	}

	missing := c.file(seed.unusedName("does-not-exist.txt"))
	checkFalse(t, missing.Exists(), "a missing file must not exist")
	checkErrorIs(t, missing.CheckExists(), os.ErrNotExist, "CheckExists of a missing file")
	checkFalse(t, missing.IsDir(), "a missing file must not be a directory")
	checkFalse(t, missing.IsReadable(), "a missing file must not be readable")
	checkEqual(t, int64(0), missing.Size(), "a missing file must have size 0")
	checkFalse(t, missing.Info().Exists, "Info().Exists of a missing file")
	_, err := missing.Stat()
	checkErrorIs(t, err, os.ErrNotExist, "File.Stat of a missing file")
	_, err = missing.ReadAll(c.ctx)
	checkErrorIs(t, err, os.ErrNotExist, "File.ReadAll of a missing file")
	r, err := missing.OpenReader()
	if err == nil {
		_ = r.Close()
	}
	checkErrorIs(t, err, os.ErrNotExist, "File.OpenReader of a missing file")
}

// checkPastEOF checks ReadAt and Seek beyond the end of the size bytes of r.
func (c *conformance) checkPastEOF(t *testing.T, r fs.ReadAtSeekCloser, size int64, what string) {
	t.Helper()
	buf := make([]byte, 16)

	n, err := r.ReadAt(buf, size)
	checkEqual(t, 0, n, "%s.ReadAt at the end must read nothing", what)
	checkErrorIs(t, err, io.EOF, "%s.ReadAt at the end", what)
	n, err = r.ReadAt(buf, size+100)
	checkEqual(t, 0, n, "%s.ReadAt past the end must read nothing", what)
	checkErrorIs(t, err, io.EOF, "%s.ReadAt past the end", what)

	end, err := r.Seek(0, io.SeekEnd)
	if checkNoError(t, err, "%s.Seek(0, io.SeekEnd)", what) {
		checkEqual(t, size, end, "%s.Seek(0, io.SeekEnd) must return the size", what)
	}
	_, err = r.Seek(-1, io.SeekStart)
	checkError(t, err, "%s.Seek before the start must fail", what)

	// Seeking past the end may fail, else reading there must be at EOF
	pos, err := r.Seek(size+100, io.SeekStart)
	if err != nil {
		return
	}
	checkEqual(t, size+100, pos, "%s.Seek past the end must return the new offset", what)
	n, err = r.Read(buf)
	checkEqual(t, 0, n, "%s.Read after a Seek past the end must read nothing", what)
	checkErrorIs(t, err, io.EOF, "%s.Read after a Seek past the end", what)
}

// testLstat checks the Lstat method of a fs.SymbolicLinkFileSystem
// on the seed, which has no symbolic links.
func (c *conformance) testLstat(t *testing.T) {
	slfs, ok := c.fs.(fs.SymbolicLinkFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.SymbolicLinkFileSystem", c.fs)
	}
	for _, name := range c.cfg.Seed.files() {
		info, err := slfs.Lstat(c.path(name))
		if !checkNoError(t, err, "Lstat(%q)", name) {
			continue
		}
		checkTrue(t, info.Exists, "Lstat(%q).Exists", name)
		checkFalse(t, info.IsSymlink, "Lstat(%q).IsSymlink of a regular file", name)
		checkTrue(t, info.IsRegular, "Lstat(%q).IsRegular", name)
		checkEqual(t, int64(len(c.cfg.Seed[name])), info.Size, "Lstat(%q).Size", name)
	}
	if !c.cfg.NoDirectories {
		for _, dir := range c.cfg.Seed.dirs() {
			info, err := slfs.Lstat(c.path(dir))
			if checkNoError(t, err, "Lstat(%q)", dir) {
				checkTrue(t, info.IsDir, "Lstat(%q).IsDir", dir)
				checkFalse(t, info.IsSymlink, "Lstat(%q).IsSymlink of a directory", dir)
			}
		}
	}
	_, err := slfs.Lstat(c.path(c.cfg.Seed.unusedName("does-not-exist.txt")))
	checkErrorIs(t, err, os.ErrNotExist, "Lstat of a missing file")
}

// testWriteOnly checks that a write-only file system rejects reads
// with fs.ErrWriteOnlyFileSystem through the File API.
func (c *conformance) testWriteOnly(t *testing.T) {
	name := c.cfg.Seed.sampleFile()
	if name == "" {
		name = "write-only.txt"
	}
	f := c.file(name)
	_, err := f.OpenReader()
	checkErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "File.OpenReader on a write-only file system")
	_, err = f.ReadAll(c.ctx)
	checkErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "File.ReadAll on a write-only file system")
	_, err = f.Stat()
	checkErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "File.Stat on a write-only file system")
	err = c.file("").ListDir(c.ctx, func(fs.File) error { return nil })
	checkErrorIs(t, err, fs.ErrWriteOnlyFileSystem, "File.ListDir on a write-only file system")
	checkFalse(t, f.Exists(), "File.Exists on a write-only file system")
	checkFalse(t, f.IsReadable(), "File.IsReadable on a write-only file system")
}

// testReadOnly checks that a read-only file system rejects writes
// with fs.ErrReadOnlyFileSystem through the File API.
func (c *conformance) testReadOnly(t *testing.T) {
	newFile := c.file(c.cfg.Seed.unusedName("read-only-test.txt"))
	_, err := newFile.OpenWriter()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.OpenWriter on a read-only file system")
	_, err = newFile.OpenAppendWriter()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.OpenAppendWriter on a read-only file system")
	_, err = newFile.OpenReadWriter()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.OpenReadWriter on a read-only file system")
	err = newFile.WriteAll(c.ctx, []byte("x"))
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.WriteAll on a read-only file system")
	err = newFile.Append(c.ctx, []byte("x"))
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.Append on a read-only file system")
	err = newFile.Touch()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.Touch on a read-only file system")
	newDir := c.file(c.cfg.Seed.unusedName("read-only-dir"))
	err = newDir.MakeDir()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.MakeDir on a read-only file system")
	err = newDir.MakeAllDirs()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.MakeAllDirs on a read-only file system")

	name := c.cfg.Seed.sampleFile()
	if name == "" {
		return // No existing file to modify
	}
	existing := c.file(name)
	err = existing.Remove()
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.Remove on a read-only file system")
	err = existing.RemoveRecursive(c.ctx)
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.RemoveRecursive on a read-only file system")
	_, err = existing.Rename(c.ctx, newFile.Name())
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.Rename on a read-only file system")
	err = existing.MoveTo(c.ctx, newFile)
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.MoveTo on a read-only file system")
	err = existing.Truncate(c.ctx, 1)
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.Truncate on a read-only file system")
	err = existing.SetPermissions(fs.AllRead)
	checkErrorIs(t, err, fs.ErrReadOnlyFileSystem, "File.SetPermissions on a read-only file system")
	checkFalse(t, existing.IsWritable(), "File.IsWritable on a read-only file system")
	checkTrue(t, existing.Exists(), "the seed must still exist after rejected writes")
	got, err := existing.ReadAll(c.ctx)
	if checkNoError(t, err, "reading %q after rejected writes", name) {
		checkBytes(t, c.cfg.Seed[name], got, "the seed content must be unchanged after rejected writes")
	}
}

// cleanup removes everything the suite created below TestDir.
func (c *conformance) cleanup(t *testing.T) {
	err := c.file("").RemoveDirContentsRecursive(c.ctx)
	mustNoError(t, err, "removing the suite files below TestDir")
	if !c.cfg.NoDirectories {
		checkTrue(t, c.file("").IsEmptyDir(), "TestDir must be empty after cleanup")
	}
}

// testClose closes the file system and checks
// that every operation reports fs.ErrFileSystemClosed after that.
func (c *conformance) testClose(t *testing.T) {
	registered := fs.IsRegistered(c.fs)
	mustNoError(t, c.fs.Close(), "Close")
	checkNoError(t, c.fs.Close(), "Close must be idempotent")
	if c.isLocalFS() {
		t.Log("fs.Local can't be closed, it stays usable")
		return
	}
	if registered {
		checkFalse(t, fs.IsRegistered(c.fs), "Close must unregister the file system")
	}
	// An operation that blocks, for example on a lock that another
	// operation leaked, is reported, and the following ones get less time
	timeout := closedOpTimeout
	for _, op := range c.closedOperations() {
		c.run(t, op.name, func(t *testing.T) {
			err, returned := callOp(op.call, timeout)
			if !returned {
				t.Errorf("%s after Close did not return within %s", op.name, timeout)
				timeout = closedOpTimeout / 20
				return
			}
			checkErrorIs(t, err, fs.ErrFileSystemClosed, "%s after Close", op.name)
		})
	}
}

// closedOpTimeout is the time an operation after Close gets to return.
const closedOpTimeout = 10 * time.Second

// operation is a call of a file system method for the checks after Close.
type operation struct {
	name string
	call func() error
}

// callOp calls op in a goroutine and returns its error,
// or a panic of op as error. It reports false if op did not return
// within timeout, the goroutine of op is left behind then.
func callOp(op func() error, timeout time.Duration) (err error, returned bool) {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("panic: %v\n%s", p, debug.Stack())
			}
		}()
		done <- op()
	}()
	select {
	case err = <-done:
		return err, true
	case <-time.After(timeout):
		return nil, false
	}
}

// closeOpened closes a reader or writer that an operation
// on a closed file system should not have returned.
func closeOpened(closer io.Closer) {
	if a, ok := closer.(fs.WriteAborter); ok {
		_ = a.Abort()
	}
	if closer != nil {
		_ = closer.Close()
	}
}

// closedOperations returns a call of every method that the file system
// implements and that is allowed by ReadableWritable, for the checks after Close.
func (c *conformance) closedOperations() []operation {
	var (
		ctx    = c.ctx
		dir    = c.path("")
		file   = c.path(c.cfg.Seed.unusedName("closed.txt"))
		other  = c.path(c.cfg.Seed.unusedName("closed-other.txt"))
		newDir = c.path(c.cfg.Seed.unusedName("closed-dir"))
		nop    = func(*fs.FileInfo) error { return nil }
		ops    []operation
	)
	if sample := c.cfg.Seed.sampleFile(); sample != "" {
		file = c.path(sample)
	}
	add := func(name string, call func() error) {
		ops = append(ops, operation{name: name, call: call})
	}
	if c.readable {
		add("Stat", func() error { _, err := c.fs.Stat(file); return err })
		add("ListDir", func() error { return c.fs.ListDir(ctx, dir, nil, nop) })
		add("OpenReader", func() error {
			r, err := c.fs.OpenReader(file)
			if err == nil {
				closeOpened(r)
			}
			return err
		})
		if f, ok := c.fs.(fs.ExistsFileSystem); ok {
			add("Exists", func() error { _, err := f.Exists(file); return err })
		}
		if f, ok := c.fs.(fs.ReadAllFileSystem); ok {
			add("ReadAll", func() error { _, err := f.ReadAll(ctx, file); return err })
		}
		if f, ok := c.fs.(fs.ListDirRecursiveFileSystem); ok {
			add("ListDirRecursive", func() error { return f.ListDirRecursive(ctx, dir, nil, nop) })
		}
		if f, ok := c.fs.(fs.WatchFileSystem); ok {
			add("Watch", func() error {
				cancel, err := f.Watch(file, func(fs.File, fs.Event) {})
				if err == nil && cancel != nil {
					_ = cancel()
				}
				return err
			})
		}
		if f, ok := c.fs.(fs.UserFileSystem); ok {
			add("User", func() error { _, err := f.User(file); return err })
		}
		if f, ok := c.fs.(fs.GroupFileSystem); ok {
			add("Group", func() error { _, err := f.Group(file); return err })
		}
		if f, ok := c.fs.(fs.SymbolicLinkFileSystem); ok {
			add("Lstat", func() error { _, err := f.Lstat(file); return err })
			add("ReadSymbolicLink", func() error { _, err := f.ReadSymbolicLink(file); return err })
		}
		if f, ok := c.fs.(fs.XAttrFileSystem); ok {
			add("ListXAttr", func() error { _, err := f.ListXAttr(file, true); return err })
			add("GetXAttr", func() error { _, err := f.GetXAttr(file, "user.gofs-test", true); return err })
		}
	}
	if c.writable && c.writeFS != nil {
		add("OpenWriter", func() error {
			w, err := c.writeFS.OpenWriter(other, 0)
			if err == nil {
				closeOpened(w)
			}
			return err
		})
		add("MakeDir", func() error { return c.writeFS.MakeDir(newDir, 0) })
		add("Remove", func() error { return c.writeFS.Remove(file) })
		if f, ok := c.fs.(fs.WriteAllFileSystem); ok {
			add("WriteAll", func() error { return f.WriteAll(ctx, other, []byte("closed"), 0) })
		}
		if f, ok := c.fs.(fs.AppendFileSystem); ok {
			add("Append", func() error { return f.Append(ctx, other, []byte("closed"), 0) })
		}
		if f, ok := c.fs.(fs.AppendWriterFileSystem); ok {
			add("OpenAppendWriter", func() error {
				w, err := f.OpenAppendWriter(other, 0)
				if err == nil {
					closeOpened(w)
				}
				return err
			})
		}
		if f, ok := c.fs.(fs.ReadWriterFileSystem); ok && c.readable {
			add("OpenReadWriter", func() error {
				rw, err := f.OpenReadWriter(other, 0)
				if err == nil {
					closeOpened(rw)
				}
				return err
			})
		}
		if f, ok := c.fs.(fs.TruncateFileSystem); ok {
			add("Truncate", func() error { return f.Truncate(ctx, file, 1) })
		}
		if f, ok := c.fs.(fs.TouchFileSystem); ok {
			add("Touch", func() error { return f.Touch(other, 0) })
		}
		if f, ok := c.fs.(fs.MakeAllDirsFileSystem); ok {
			add("MakeAllDirs", func() error { return f.MakeAllDirs(newDir, 0) })
		}
		if f, ok := c.fs.(fs.RemoveAllFileSystem); ok {
			add("RemoveAll", func() error { return f.RemoveAll(ctx, file) })
		}
		if f, ok := c.fs.(fs.CopyFileSystem); ok {
			add("CopyFile", func() error { return f.CopyFile(ctx, file, other) })
		}
		if f, ok := c.fs.(fs.MoveFileSystem); ok {
			add("Move", func() error { return f.Move(ctx, file, other) })
		}
		if f, ok := c.fs.(fs.PermissionsFileSystem); ok {
			add("SetPermissions", func() error { return f.SetPermissions(file, fs.UserReadWrite) })
		}
		if f, ok := c.fs.(fs.UserFileSystem); ok {
			add("SetUser", func() error { return f.SetUser(file, "nobody") })
		}
		if f, ok := c.fs.(fs.GroupFileSystem); ok {
			add("SetGroup", func() error { return f.SetGroup(file, "nobody") })
		}
		if f, ok := c.fs.(fs.SymbolicLinkFileSystem); ok {
			add("CreateSymbolicLink", func() error { return f.CreateSymbolicLink(file, other) })
		}
		if f, ok := c.fs.(fs.XAttrFileSystem); ok {
			add("SetXAttr", func() error { return f.SetXAttr(file, "user.gofs-test", []byte("x"), 0, true) })
			add("RemoveXAttr", func() error { return f.RemoveXAttr(file, "user.gofs-test", true) })
		}
	}
	return ops
}

// isLocal reports whether fileSystem is the local file system
// or a SubFileSystem view of it.
func isLocal(fileSystem fs.FileSystem) bool {
	for {
		switch f := fileSystem.(type) {
		case *fs.LocalFileSystem:
			return true
		case *fs.SubFileSystem:
			fileSystem = f.Parent()
		default:
			return false
		}
	}
}
