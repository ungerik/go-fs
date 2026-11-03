package fstest

import (
	"errors"
	iofs "io/fs"
	"os"
	"os/user"
	"sort"
	"strings"
	"testing"
	stdfstest "testing/fstest"
	"time"

	fs "github.com/ungerik/go-fs"
)

// The checks of this file cover what every file system shares through the
// optional path interfaces and the File helpers built on the FileSystem
// methods: URIFileSystem, VolumeNameFileSystem, AbsPathFileSystem,
// HiddenFileSystem, UserFileSystem, GroupFileSystem and WatchFileSystem,
// the listing helpers of File, its io/fs view File.StdFS, and the write
// helpers that encode content or rename and remove by name.

// testPathInterfaces checks the optional path interfaces and the pure path
// helpers of File, which depend on the separator and volume of the file
// system.
func (c *conformance) testPathInterfaces(t *testing.T) {
	testPath := c.fs.CleanPath(c.cfg.TestDir, "x", "y.txt")

	c.run(t, "URIFileSystem", func(t *testing.T) {
		u, ok := c.fs.(fs.URIFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.URIFileSystem", c.fs)
		}
		checkEqual(t, testPath, u.PathFromURI(u.URIFromPath(testPath)), "PathFromURI(URIFromPath(path)) must return the path")
		dir, name := u.SplitDirAndName(testPath)
		checkEqual(t, "y.txt", name, "SplitDirAndName(%q) name", testPath)
		checkEqual(t, c.fs.CleanPath(c.cfg.TestDir, "x"), dir, "SplitDirAndName(%q) dir", testPath)
	})

	c.run(t, "VolumeName", func(t *testing.T) {
		v, ok := c.fs.(fs.VolumeNameFileSystem)
		if !ok {
			checkEqual(t, "", c.file("x/y.txt").VolumeName(), "File.VolumeName without fs.VolumeNameFileSystem")
			return
		}
		volume := v.VolumeName(testPath)
		checkTrue(t, strings.HasPrefix(testPath, volume), "VolumeName(%q) = %q must be a prefix of the path", testPath, volume)
		checkEqual(t, v.VolumeName(c.path("")), volume, "a path below TestDir must be on the volume of TestDir")
		checkEqual(t, volume, c.file("x/y.txt").VolumeName(), "File.VolumeName must return the VolumeName of the path")
	})

	c.run(t, "AbsPath", func(t *testing.T) {
		f := c.file("x/y.txt")
		a, ok := c.fs.(fs.AbsPathFileSystem)
		if !ok {
			// Every path is absolute
			checkEqual(t, testPath, f.AbsPath(), "File.AbsPath without fs.AbsPathFileSystem")
			checkTrue(t, f.HasAbsPath(), "File.HasAbsPath without fs.AbsPathFileSystem")
			return
		}
		mustTrue(t, a.IsAbsPath(testPath), "IsAbsPath(%q): the paths below TestDir must be absolute", testPath)
		checkTrue(t, f.HasAbsPath(), "File.HasAbsPath below TestDir")
		checkEqual(t, testPath, a.AbsPath(testPath), "AbsPath of an absolute path")
		checkEqual(t, testPath, f.AbsPath(), "File.AbsPath of an absolute path")

		base := c.fs.CleanPath(c.cfg.TestDir, "a", "b")
		target := c.fs.CleanPath(c.cfg.TestDir, "a", "c", "d.txt")
		rel, err := a.RelPath(base, target)
		if checkNoError(t, err, "RelPath(%q, %q)", base, target) {
			checkEqual(t, target, c.fs.CleanPath(base, rel), "joining the base with RelPath(%q, %q) = %q must return the target", base, target, rel)
		}
		rel, err = a.RelPath(base, base)
		if checkNoError(t, err, "RelPath of identical paths") {
			checkEqual(t, ".", rel, "RelPath of identical paths")
		}
	})

	c.run(t, "FileHelpers", func(t *testing.T) {
		f := c.file("x/Report.TXT")
		checkEqual(t, ".txt", f.ExtLower(), "File.ExtLower")
		checkEqual(t, c.file("x/Report"), f.TrimExt(), "File.TrimExt")
		dir, name := f.DirAndName()
		checkEqual(t, c.file("x"), dir, "File.DirAndName dir")
		checkEqual(t, "Report.TXT", name, "File.DirAndName name")
		checkEqual(t, c.file("x/n-7.txt"), c.file("x").Joinf("n-%d.txt", 7), "File.Joinf")
		checkEqual(t, strings.ReplaceAll(f.Path(), c.fs.Separator(), "/"), f.PathWithSlashes(), "File.PathWithSlashes")
		checkEqual(t, string(f), f.RawURI(), "File.RawURI")
	})
}

// testListingHelpers checks the listing helpers of File against the seed.
func (c *conformance) testListingHelpers(t *testing.T) {
	seed := c.cfg.Seed
	dir := c.file("")
	files, subDirs := seed.entriesOf("")

	c.run(t, "ListDirInfo", func(t *testing.T) {
		isDir := map[string]bool{}
		mustNoError(t, dir.ListDirInfo(c.ctx, func(info *fs.FileInfo) error {
			isDir[info.Name] = info.IsDir
			return nil
		}), "File.ListDirInfo")
		checkEqual(t, len(files)+len(subDirs), len(isDir), "File.ListDirInfo must list every entry once, listed %v", isDir)
		for _, name := range files {
			listedDir, found := isDir[name]
			checkTrue(t, found && !listedDir, "File.ListDirInfo must list the file %q", name)
		}
		for _, name := range subDirs {
			checkTrue(t, isDir[name], "File.ListDirInfo must list the directory %q with IsDir", name)
		}
	})

	c.run(t, "ListDirRecursiveIter", func(t *testing.T) {
		var listed []string
		for f, err := range dir.ListDirRecursiveIter(c.ctx) {
			mustNoError(t, err, "File.ListDirRecursiveIter")
			listed = append(listed, c.relOf(t, f))
		}
		sort.Strings(listed)
		checkStrings(t, seed.files(), listed, "File.ListDirRecursiveIter must yield every seed file")
	})

	c.run(t, "ListDirRecursiveMax", func(t *testing.T) {
		all, err := dir.ListDirRecursiveMax(c.ctx, -1)
		mustNoError(t, err, "File.ListDirRecursiveMax(-1)")
		listed := make([]string, len(all))
		for i, f := range all {
			listed[i] = c.relOf(t, f)
		}
		sort.Strings(listed)
		checkStrings(t, seed.files(), listed, "File.ListDirRecursiveMax(-1) must return every seed file")
		if len(seed) > 1 {
			some, err := dir.ListDirRecursiveMax(c.ctx, 1)
			if checkNoError(t, err, "File.ListDirRecursiveMax(1)") {
				checkEqual(t, 1, len(some), "File.ListDirRecursiveMax(1) must return one file")
			}
		}
	})

	c.run(t, "Glob", func(t *testing.T) {
		glob := func(pattern string) []string {
			t.Helper()
			var matched []string
			for f, err := range dir.Glob(c.ctx, pattern) {
				mustNoError(t, err, "File.Glob(%q)", pattern)
				matched = append(matched, c.relOf(t, f))
			}
			sort.Strings(matched)
			return matched
		}
		all := append(append([]string{}, files...), subDirs...)
		sort.Strings(all)
		checkStrings(t, all, glob("*"), `File.Glob("*") must match every entry of the directory`)
		checkStrings(t, subDirs, glob("*/"), `File.Glob("*/") must match only the directories`)
		checkStrings(t, []string{""}, glob(""), `File.Glob("") must yield the directory itself`)
		if deepest := seed.deepestFile(); deepest != "" {
			checkStrings(t, []string{deepest}, glob(deepest), "File.Glob of the path of a seed file")
		}
		checkStrings(t, nil, glob("no-such-dir/*"), "File.Glob below a missing directory must match nothing")
	})

	c.run(t, "StdFS", func(t *testing.T) {
		// The io/fs view of the directory passes the checks of the standard library
		checkNoError(t, stdfstest.TestFS(dir.StdFS(), seed.files()...), "testing/fstest.TestFS(File.StdFS())")
	})

	c.run(t, "StdDirEntry", func(t *testing.T) {
		sample := seed.sampleFile()
		if sample == "" {
			t.Skip("the seed has no files")
		}
		entry := c.file(sample).StdDirEntry()
		checkEqual(t, baseName(sample), entry.Name(), "StdDirEntry.Name")
		checkFalse(t, entry.IsDir(), "StdDirEntry.IsDir of a file")
		checkEqual(t, iofs.FileMode(0), entry.Type()&iofs.ModeType, "StdDirEntry.Type of a regular file")
		info, err := entry.Info()
		if checkNoError(t, err, "StdDirEntry.Info") {
			checkEqual(t, int64(len(seed[sample])), info.Size(), "StdDirEntry.Info().Size")
		}
	})
}

// relOf returns the path of f relative to TestDir with slashes,
// "" for TestDir itself.
func (c *conformance) relOf(t *testing.T, f fs.File) string {
	t.Helper()
	if f.Path() == c.path("") {
		return ""
	}
	rel, err := relPath(c.path(""), f.Path(), c.fs.Separator())
	mustNoError(t, err, "%s must be below TestDir", f)
	return rel
}

// testHidden checks that the IsHidden of a fs.HiddenFileSystem agrees
// with the IsHidden of the FileInfo of Stat.
func (c *conformance) testHidden(t *testing.T) {
	h, ok := c.fs.(fs.HiddenFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.HiddenFileSystem", c.fs)
	}
	for _, name := range c.cfg.Seed.files() {
		info, err := c.fs.Stat(c.path(name))
		if checkNoError(t, err, "Stat(%q)", name) {
			checkEqual(t, info.IsHidden, h.IsHidden(c.path(name)), "IsHidden(%q) must agree with Stat().IsHidden", name)
		}
	}
}

// testOwnership checks that the owner and group that a fs.UserFileSystem
// and fs.GroupFileSystem report can be set again, which changes nothing.
func (c *conformance) testOwnership(t *testing.T) {
	c.run(t, "User", func(t *testing.T) {
		u, ok := c.fs.(fs.UserFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.UserFileSystem", c.fs)
		}
		c.put(t, "ownership/user.txt", "user")
		path := c.wpath("ownership/user.txt")
		owner, err := u.User(path)
		if errors.Is(err, errors.ErrUnsupported) || errors.As(err, new(user.UnknownUserIdError)) {
			// Also a local file whose uid has no passwd entry, like in a container
			t.Skipf("User not available: %v", err)
		}
		mustNoError(t, err, "User")
		if checkNoError(t, u.SetUser(path, owner), "SetUser to the reported user %q", owner) {
			got, err := u.User(path)
			if checkNoError(t, err, "User after SetUser") {
				checkEqual(t, owner, got, "User after SetUser to the same user")
			}
		}
		_, err = u.User(c.wpath("ownership/missing.txt"))
		checkErrorIs(t, err, os.ErrNotExist, "User of a missing file")
	})

	c.run(t, "Group", func(t *testing.T) {
		g, ok := c.fs.(fs.GroupFileSystem)
		if !ok {
			t.Skipf("%T does not implement fs.GroupFileSystem", c.fs)
		}
		c.put(t, "ownership/group.txt", "group")
		path := c.wpath("ownership/group.txt")
		group, err := g.Group(path)
		if errors.Is(err, errors.ErrUnsupported) || errors.As(err, new(user.UnknownGroupIdError)) {
			// Also a local file whose gid has no group entry, like in a container
			t.Skipf("Group not available: %v", err)
		}
		mustNoError(t, err, "Group")
		if checkNoError(t, g.SetGroup(path, group), "SetGroup to the reported group %q", group) {
			got, err := g.Group(path)
			if checkNoError(t, err, "Group after SetGroup") {
				checkEqual(t, group, got, "Group after SetGroup to the same group")
			}
		}
		_, err = g.Group(c.wpath("ownership/missing.txt"))
		checkErrorIs(t, err, os.ErrNotExist, "Group of a missing file")
	})
}

// watchTimeout limits the wait for the event of a write to a watched
// directory. A variable for the tests of the suite.
var watchTimeout = 10 * time.Second

// testWatch checks that a fs.WatchFileSystem reports a file created in
// a watched directory and that the cancel function of the watch succeeds.
func (c *conformance) testWatch(t *testing.T) {
	w, ok := c.fs.(fs.WatchFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.WatchFileSystem", c.fs)
	}
	c.mkdir(t, "watch")
	names := make(chan string, 64)
	cancel, err := w.Watch(c.wpath("watch"), func(f fs.File, _ fs.Event) {
		select {
		case names <- f.Name():
		default:
		}
	})
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("Watch not available: %v", err)
	}
	mustNoError(t, err, "Watch of a directory")
	canceled := false
	t.Cleanup(func() {
		if !canceled {
			_ = cancel()
		}
	})

	c.put(t, "watch/created.txt", "created")
	timeout := time.After(watchTimeout)
	for reported := false; !reported; {
		select {
		case name := <-names:
			reported = name == "created.txt"
		case <-timeout:
			t.Fatalf("no event for a file created in the watched directory within %s", watchTimeout)
		}
	}
	checkNoError(t, cancel(), "the cancel function of Watch")
	canceled = true
}

// testWriteHelpers checks the write helpers of File that encode content,
// rename by name and remove the files of a directory.
func (c *conformance) testWriteHelpers(t *testing.T) {
	c.mkdir(t, "helpers")
	dir := c.wfile("helpers")

	c.run(t, "String", func(t *testing.T) {
		f := dir.Join("string.txt")
		mustNoError(t, f.WriteAllString(c.ctx, "hello"), "File.WriteAllString")
		got, err := f.ReadAllString(c.ctx)
		if checkNoError(t, err, "File.ReadAllString") {
			checkEqual(t, "hello", got, "File.ReadAllString after WriteAllString")
		}
	})

	type record struct {
		Name string `json:"name" xml:"name"`
		N    int    `json:"n" xml:"n"`
	}
	want := record{Name: "a", N: 1}

	c.run(t, "JSON", func(t *testing.T) {
		f := dir.Join("data.json")
		mustNoError(t, f.WriteJSONIndent(c.ctx, want, "  "), "File.WriteJSONIndent")
		raw, err := f.ReadAllString(c.ctx)
		mustNoError(t, err, "File.ReadAllString")
		checkTrue(t, strings.Contains(raw, "\n  \"name\""), "File.WriteJSONIndent must indent: %q", raw)
		var got record
		if checkNoError(t, f.ReadJSON(c.ctx, &got), "File.ReadJSON") {
			checkEqual(t, want, got, "File.ReadJSON after WriteJSONIndent")
		}
	})

	c.run(t, "XML", func(t *testing.T) {
		f := dir.Join("data.xml")
		mustNoError(t, f.WriteXMLIndent(c.ctx, want, "  "), "File.WriteXMLIndent")
		var got record
		if checkNoError(t, f.ReadXML(c.ctx, &got), "File.ReadXML") {
			checkEqual(t, want, got, "File.ReadXML after WriteXMLIndent")
		}
	})

	c.run(t, "Renamef", func(t *testing.T) {
		c.put(t, "helpers/rename.txt", "rename")
		renamed, err := dir.Join("rename.txt").Renamef(c.ctx, "renamed-%d.txt", 2)
		mustNoError(t, err, "File.Renamef")
		checkEqual(t, dir.Join("renamed-2.txt"), renamed, "File.Renamef must return the renamed file")
		c.checkContent(t, "helpers/renamed-2.txt", "rename", "File.Renamef")
		c.checkMissing(t, "helpers/rename.txt", "File.Renamef must remove the old name")
	})

	c.run(t, "RemoveDirContents", func(t *testing.T) {
		c.put(t, "helpers/clean/a.txt", "a")
		c.put(t, "helpers/clean/b.log", "b")
		c.put(t, "helpers/clean/sub/c.txt", "c")
		clean := dir.Join("clean")
		mustNoError(t, clean.RemoveDirContents(c.ctx, "*.log"), "File.RemoveDirContents with a pattern")
		c.checkMissing(t, "helpers/clean/b.log", "File.RemoveDirContents must remove a matching file")
		c.checkContent(t, "helpers/clean/a.txt", "a", "File.RemoveDirContents must keep a file that doesn't match")
		mustNoError(t, clean.RemoveDirContents(c.ctx), "File.RemoveDirContents")
		c.checkMissing(t, "helpers/clean/a.txt", "File.RemoveDirContents must remove every file")
		c.checkContent(t, "helpers/clean/sub/c.txt", "c", "File.RemoveDirContents must keep sub-directories and their files")
	})
}
