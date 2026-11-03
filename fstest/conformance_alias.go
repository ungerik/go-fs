package fstest

import (
	"errors"
	"os"
	"testing"

	fs "github.com/ungerik/go-fs"
)

// The checks of this file verify that copying and moving never lose data
// when the destination names the source another way: through a symbolic
// link to the source, a link to a parent directory, another hard link,
// another case of the name on a case-insensitive file system, or another
// Unicode normalization of the name on a file system that matches names by
// normalization. A file system that can't tell such a destination from
// another file must still not truncate the source before it is read, or
// remove the source after it copied it onto itself.
//
// The checks don't prescribe the outcome, only that the data survives:
//
//	| Operation result | The content must be readable at |
//	|------------------|---------------------------------|
//	| error            | the source                      |
//	| copy succeeded   | the source and the destination  |
//	| move succeeded   | the destination                 |
//
// An alias that the file system can't create, or that doesn't resolve to
// the source, is skipped: links need a fs.SymbolicLinkFileSystem, hard
// links Config.HardLink, and the case and normalization aliases a file
// system that matches names that way.

// alias creates a destination that names the same file or directory as
// a source another way.
type alias struct {
	name string
	// dir tells that source and destination are directories, else files
	dir bool
	// setup creates the source and the destination below dir, a directory
	// relative to the work directory ending with a slash, and returns
	// their paths relative to the work directory. The content of a file
	// source is srcContent, a directory source has a child.txt with
	// childContent.
	setup func(c *conformance, t *testing.T, dir string) (src, dest string)
}

// aliasOp is a copy or move from a source to a destination alias.
type aliasOp struct {
	name string
	// dir tells that the operation is run on the directory aliases,
	// else on the file aliases
	dir bool
	// copies tells that the source stays after a successful operation
	copies bool
	op     func(c *conformance, src, dest fs.File) error
}

var aliasOps = []aliasOp{
	{name: "CopyFile", copies: true, op: func(c *conformance, src, dest fs.File) error {
		return fs.CopyFile(c.ctx, src, dest)
	}},
	{name: "Move", op: func(c *conformance, src, dest fs.File) error {
		return fs.Move(c.ctx, src, dest)
	}},
	{name: "CopyRecursive", dir: true, copies: true, op: func(c *conformance, src, dest fs.File) error {
		return fs.CopyRecursive(c.ctx, src, dest)
	}},
	{name: "MoveDir", dir: true, op: func(c *conformance, src, dest fs.File) error {
		return fs.Move(c.ctx, src, dest)
	}},
}

var aliases = []alias{
	{name: "LinkToSource", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"src.txt", srcContent)
		c.symlink(t, dir+"src.txt", dir+"dest.txt")
		return dir + "src.txt", dir + "dest.txt"
	}},
	{name: "LinkOntoTarget", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"target.txt", srcContent)
		c.symlink(t, dir+"target.txt", dir+"src.txt")
		return dir + "src.txt", dir + "target.txt"
	}},
	{name: "ThroughParentLink", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"real/src.txt", srcContent)
		c.symlink(t, dir+"real", dir+"link")
		return dir + "real/src.txt", dir + "link/src.txt"
	}},
	{name: "HardLink", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"src.txt", srcContent)
		c.hardLink(t, dir+"src.txt", dir+"dest.txt")
		return dir + "src.txt", dir + "dest.txt"
	}},
	{name: "OtherCase", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"src.txt", srcContent)
		return dir + "src.txt", dir + "SRC.TXT"
	}},
	{name: "OtherNormalization", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		// "é" composed (NFC) and decomposed (NFD)
		c.put(t, dir+"é.txt", srcContent)
		return dir + "é.txt", dir + "é.txt"
	}},
	{name: "OtherCaseThroughParentLink", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"real/src.txt", srcContent)
		c.symlink(t, dir+"real", dir+"link")
		return dir + "real/src.txt", dir + "LINK/SRC.TXT"
	}},
	{name: "SourceThroughParentLinkOtherCase", setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		// The source through the link, the destination its real path under another case
		c.put(t, dir+"real/src.txt", srcContent)
		c.symlink(t, dir+"real", dir+"link")
		return dir + "link/src.txt", dir + "REAL/SRC.TXT"
	}},

	{name: "LinkToSourceDir", dir: true, setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"srcdir/child.txt", childContent)
		c.symlink(t, dir+"srcdir", dir+"destdir")
		return dir + "srcdir", dir + "destdir"
	}},
	{name: "ThroughParentLinkDir", dir: true, setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"real/srcdir/child.txt", childContent)
		c.symlink(t, dir+"real", dir+"link")
		return dir + "real/srcdir", dir + "link/srcdir"
	}},
	{name: "OtherCaseDir", dir: true, setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		c.put(t, dir+"srcdir/child.txt", childContent)
		return dir + "srcdir", dir + "SRCDIR"
	}},
	{name: "HardLinkedChild", dir: true, setup: func(c *conformance, t *testing.T, dir string) (string, string) {
		// Not the same directory, but every file of it is also a file of
		// the destination, which a copy must not truncate before it is read
		c.put(t, dir+"srcdir/child.txt", childContent)
		c.mkdir(t, dir+"destdir")
		c.hardLink(t, dir+"srcdir/child.txt", dir+"destdir/child.txt")
		return dir + "srcdir", dir + "destdir"
	}},
}

// testAliases runs every operation of aliasOps against every alias
// that the file system supports, each in its own directory.
func (c *conformance) testAliases(t *testing.T) {
	for _, op := range aliasOps {
		c.run(t, op.name, func(t *testing.T) {
			for _, a := range aliases {
				if a.dir != op.dir {
					continue
				}
				c.run(t, a.name, func(t *testing.T) {
					c.checkAlias(t, op, a)
				})
			}
		})
	}

	c.run(t, "SubFileSystemRoot", c.testSubFileSystemRoot)
}

// checkAlias sets up the alias a, skips t if the destination doesn't name
// the source, runs op from the source to the destination and checks that
// the data survived.
func (c *conformance) checkAlias(t *testing.T, op aliasOp, a alias) {
	t.Helper()
	src, dest := a.setup(c, t, "alias-"+op.name+"-"+a.name+"/")
	child, content := "", srcContent
	if a.dir {
		child, content = "/child.txt", childContent
	}
	if data, err := c.tryReadAll(c.wpath(dest + child)); err != nil || string(data) != content {
		t.Skipf("%s doesn't reach the source on this file system", dest+child)
	}

	what := op.name + " from " + src + " to the alias " + dest
	if err := op.op(c, c.wfile(src), c.wfile(dest)); err != nil {
		c.checkContent(t, src+child, content, "%s failed with %s, so the source must keep its data", what, describeErr(err))
		return
	}
	c.checkContent(t, dest+child, content, "%s succeeded, so the destination must have the data of the source", what)
	if op.copies {
		c.checkContent(t, src+child, content, "%s succeeded, so the source must keep its data", what)
	}
}

// testSubFileSystemRoot checks that fs.Move refuses to move the root
// directory of a SubFileSystem of the file system into the file system,
// which would leave the registered SubFileSystem without a root.
func (c *conformance) testSubFileSystemRoot(t *testing.T) {
	const dir = "alias-sub-root/"
	c.put(t, dir+"root/child.txt", childContent)
	sub, err := fs.NewSubFileSystem(c.fs, c.wpath(dir+"root"), "")
	mustNoError(t, err, "NewSubFileSystem")
	mustNoError(t, fs.Register(sub), "Register(SubFileSystem)")
	t.Cleanup(func() { fs.Unregister(sub) })

	err = fs.Move(c.ctx, sub.RootDir(), c.wfile(dir+"moved"))
	checkErrorIs(t, err, os.ErrInvalid, "fs.Move of the root directory of a SubFileSystem into its parent")
	c.checkContent(t, dir+"root/child.txt", childContent, "fs.Move of the root directory of a SubFileSystem must keep it")
}

// symlink creates a symbolic link at the path link relative to the work
// directory to the absolute path of target, or skips t if the file system
// has no symbolic links.
func (c *conformance) symlink(t *testing.T, target, link string) {
	t.Helper()
	slfs, ok := c.fs.(fs.SymbolicLinkFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.SymbolicLinkFileSystem", c.fs)
	}
	err := slfs.CreateSymbolicLink(c.wpath(target), c.wpath(link))
	if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, os.ErrPermission) {
		t.Skipf("symbolic links not available: %v", err)
	}
	mustNoError(t, err, "CreateSymbolicLink(%q, %q)", target, link)
}

// hardLink creates newRel as another hard link of the file existingRel,
// both relative to the work directory, with Config.HardLink, or skips t
// if Config.HardLink is nil.
func (c *conformance) hardLink(t *testing.T, existingRel, newRel string) {
	t.Helper()
	if c.cfg.HardLink == nil {
		t.Skip("Config.HardLink is nil")
	}
	mustNoError(t, c.cfg.HardLink(c.wpath(existingRel), c.wpath(newRel)), "Config.HardLink(%q, %q)", existingRel, newRel)
}
