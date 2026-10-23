package fstest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	fs "github.com/ungerik/go-fs"
)

// The checks of this file verify the overwrite semantics
// of moving, renaming and copying:
//
//	| Destination        | Move / Rename                     | CopyFile          |
//	|--------------------|-----------------------------------|-------------------|
//	| missing            | proceeds                          | proceeds          |
//	| existing file      | replaced (a directory source:     | replaced          |
//	|                    | fs.ErrIsNotDirectory)             |                   |
//	| existing directory | error wrapping os.ErrExist,       | fs.ErrIsDirectory |
//	|                    | never merged or deleted           |                   |
//
// A destination equal to the source is a no-op, moving a directory
// into itself or one of its descendants is an error wrapping os.ErrInvalid.
// File.MoveTo and fs.CopyFile resolve an existing directory destination
// to "into the directory" before they apply these rules.
//
// Every sub-test works in its own directory below the work directory
// with these files, created by setupMove:
//
//	src.txt          "source"
//	dest.txt         "destination with longer content"
//	srcdir/child.txt "child"
//	destdir/keep.txt "keep"
//	emptydir/

const (
	srcContent   = "source"
	destContent  = "destination with longer content"
	childContent = "child"
	keepContent  = "keep"
)

// setupMove creates the files of a move check in the directory
// dir below the work directory and returns dir with a slash appended.
func (c *conformance) setupMove(t *testing.T, dir string) string {
	t.Helper()
	dir += "/"
	c.put(t, dir+"src.txt", srcContent)
	c.put(t, dir+"dest.txt", destContent)
	c.put(t, dir+"srcdir/child.txt", childContent)
	c.put(t, dir+"destdir/keep.txt", keepContent)
	c.mkdir(t, dir+"emptydir")
	return dir
}

// checkSrcIntact checks that the sources of setupMove are unchanged.
func (c *conformance) checkSrcIntact(t *testing.T, dir, what string) {
	t.Helper()
	c.checkContent(t, dir+"src.txt", srcContent, "%s must keep the source file", what)
	c.checkContent(t, dir+"srcdir/child.txt", childContent, "%s must keep the source directory", what)
}

// checkDestIntact checks that the destinations of setupMove are unchanged:
// nothing deleted, nothing merged into the directory.
func (c *conformance) checkDestIntact(t *testing.T, dir, what string) {
	t.Helper()
	c.checkContent(t, dir+"dest.txt", destContent, "%s must keep the destination file", what)
	c.checkContent(t, dir+"destdir/keep.txt", keepContent, "%s must keep the content of the destination directory", what)
	c.checkMissing(t, dir+"destdir/src.txt", "%s must not move or copy into the destination directory", what)
	c.checkMissing(t, dir+"destdir/child.txt", "%s must not merge into the destination directory", what)
	c.checkMissing(t, dir+"destdir/srcdir", "%s must not move or copy into the destination directory", what)
	c.checkIsDir(t, dir+"emptydir", "%s must keep an empty destination directory", what)
	c.checkMissing(t, dir+"emptydir/child.txt", "%s must not merge into an empty destination directory", what)
}

// intoItselfTimeout limits a move of a directory into itself, so that
// an implementation that copies the directory into itself endlessly
// is reported instead of blocking the suite.
const intoItselfTimeout = 10 * time.Second

// checkIntoItself calls move, which moves a directory into itself
// or one of its descendants, with a context that is canceled after
// intoItselfTimeout, and checks that it fails with os.ErrInvalid.
func checkIntoItself(t *testing.T, ctx context.Context, move func(ctx context.Context) error, format string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, intoItselfTimeout)
	defer cancel()
	err := move(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("%s: did not return within %s and was canceled, it probably copies the directory into itself endlessly", fmt.Sprintf(format, args...), intoItselfTimeout)
		return
	}
	checkErrorIs(t, err, os.ErrInvalid, format, args...)
}

// rejectedMove is a move or copy of the files of setupMove that must fail.
type rejectedMove struct {
	src, dest, what string
}

// checkRejected runs every move in a fresh directory below the work
// directory, so that a move that wrongly succeeds doesn't change the
// files of the others, and checks that it fails with an error matching
// target and leaves the sources and destinations unchanged.
func (c *conformance) checkRejected(t *testing.T, base string, moves []rejectedMove, target error, op func(dir, src, dest string) error) {
	t.Helper()
	for i, m := range moves {
		dir := c.setupMove(t, fmt.Sprintf("%s-%d", base, i+1))
		checkErrorIs(t, op(dir, m.src, m.dest), target, "%s", m.what)
		c.checkSrcIntact(t, dir, m.what)
		c.checkDestIntact(t, dir, m.what)
	}
}

// testMoveFileSystem checks the native Move of a fs.MoveFileSystem,
// which always gets the final destination path.
func (c *conformance) testMoveFileSystem(t *testing.T) {
	mfs, ok := c.fs.(fs.MoveFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.MoveFileSystem", c.fs)
	}
	move := func(dir, src, dest string) error {
		return mfs.Move(c.ctx, c.wpath(dir+src), c.wpath(dir+dest))
	}

	c.run(t, "Self", func(t *testing.T) {
		dir := c.setupMove(t, "move-self")
		checkNoError(t, move(dir, "src.txt", "src.txt"), "Move of a file onto itself must be a no-op")
		checkNoError(t, move(dir, "srcdir", "srcdir"), "Move of a directory onto itself must be a no-op")
		c.checkSrcIntact(t, dir, "Move onto itself")
	})

	c.run(t, "ToMissing", func(t *testing.T) {
		dir := c.setupMove(t, "move-missing")
		if checkNoError(t, move(dir, "src.txt", "new.txt"), "Move of a file to a missing path") {
			c.checkContent(t, dir+"new.txt", srcContent, "Move of a file")
			c.checkMissing(t, dir+"src.txt", "Move of a file must remove the source")
		}
		if checkNoError(t, move(dir, "srcdir", "newdir"), "Move of a directory to a missing path") {
			c.checkContent(t, dir+"newdir/child.txt", childContent, "Move of a directory must move its content")
			c.checkMissing(t, dir+"srcdir", "Move of a directory must remove the source")
		}
	})

	c.run(t, "Overwrite", func(t *testing.T) {
		dir := c.setupMove(t, "move-overwrite")
		if checkNoError(t, move(dir, "src.txt", "dest.txt"), "Move of a file onto an existing file must replace it") {
			c.checkContent(t, dir+"dest.txt", srcContent, "Move onto an existing file must replace its content")
			c.checkMissing(t, dir+"src.txt", "Move onto an existing file must remove the source")
		}
	})

	c.run(t, "OntoDirectory", func(t *testing.T) {
		c.checkRejected(t, "move-onto-dir", []rejectedMove{
			{"src.txt", "destdir", "Move of a file onto an existing directory"},
			{"srcdir", "destdir", "Move of a directory onto an existing directory"},
			{"src.txt", "emptydir", "Move of a file onto an existing empty directory"},
			{"srcdir", "emptydir", "Move of a directory onto an existing empty directory"},
		}, os.ErrExist, move)
	})

	c.run(t, "DirectoryOntoFile", func(t *testing.T) {
		c.checkRejected(t, "move-dir-onto-file", []rejectedMove{
			{"srcdir", "dest.txt", "Move of a directory onto an existing file"},
		}, fs.ErrIsNotDirectory{}, move)
	})

	c.run(t, "IntoItself", func(t *testing.T) {
		dir := c.setupMove(t, "move-into-itself-1")
		checkIntoItself(t, c.ctx, func(ctx context.Context) error {
			return mfs.Move(ctx, c.wpath(dir+"srcdir"), c.wpath(dir+"srcdir/new"))
		}, "Move of a directory into itself")
		c.checkMissing(t, dir+"srcdir/new", "Move of a directory into itself must not create the destination")
		c.checkSrcIntact(t, dir, "Move of a directory into itself")

		dir = c.setupMove(t, "move-into-itself-2")
		c.mkdir(t, dir+"srcdir/sub")
		checkIntoItself(t, c.ctx, func(ctx context.Context) error {
			return mfs.Move(ctx, c.wpath(dir+"srcdir"), c.wpath(dir+"srcdir/sub/new"))
		}, "Move of a directory into a descendant")
		c.checkMissing(t, dir+"srcdir/sub/new", "Move of a directory into a descendant must not create the destination")
		c.checkSrcIntact(t, dir, "Move of a directory into a descendant")
	})
}

// testCopyFileSystem checks the native CopyFile of a fs.CopyFileSystem,
// which always gets the final destination path.
func (c *conformance) testCopyFileSystem(t *testing.T) {
	cfs, ok := c.fs.(fs.CopyFileSystem)
	if !ok {
		t.Skipf("%T does not implement fs.CopyFileSystem", c.fs)
	}
	copyFile := func(dir, src, dest string) error {
		return cfs.CopyFile(c.ctx, c.wpath(dir+src), c.wpath(dir+dest))
	}

	c.run(t, "Self", func(t *testing.T) {
		dir := c.setupMove(t, "copy-fs-self")
		checkNoError(t, copyFile(dir, "src.txt", "src.txt"), "CopyFile of a file onto itself must be a no-op")
		c.checkSrcIntact(t, dir, "CopyFile onto itself")
	})

	c.run(t, "ToMissing", func(t *testing.T) {
		dir := c.setupMove(t, "copy-fs-missing")
		if checkNoError(t, copyFile(dir, "src.txt", "new.txt"), "CopyFile to a missing path") {
			c.checkContent(t, dir+"new.txt", srcContent, "CopyFile")
		}
		c.checkSrcIntact(t, dir, "CopyFile")
	})

	c.run(t, "Overwrite", func(t *testing.T) {
		dir := c.setupMove(t, "copy-fs-overwrite")
		if checkNoError(t, copyFile(dir, "src.txt", "dest.txt"), "CopyFile onto an existing file must replace it") {
			c.checkContent(t, dir+"dest.txt", srcContent, "CopyFile onto an existing file must replace its content")
		}
		c.checkSrcIntact(t, dir, "CopyFile onto an existing file")
	})

	c.run(t, "OntoDirectory", func(t *testing.T) {
		c.checkRejected(t, "copy-fs-onto-dir", []rejectedMove{
			{"src.txt", "destdir", "CopyFile onto an existing directory"},
			{"src.txt", "emptydir", "CopyFile onto an existing empty directory"},
		}, fs.ErrIsDirectory{}, copyFile)
	})
}

// testRename checks File.Rename, natively or emulated.
// The new name is a name in the same directory, never a directory to move into.
func (c *conformance) testRename(t *testing.T) {
	rename := func(t *testing.T, dir, src, newName string) error {
		t.Helper()
		renamed, err := c.wfile(dir+src).Rename(c.ctx, newName)
		if err == nil {
			checkEqual(t, c.wfile(dir+newName), renamed, "File.Rename(%q) must return the renamed file", newName)
			c.checkURI(t, renamed, c.wpath(dir+newName), "the File returned by File.Rename")
		}
		return err
	}

	c.run(t, "Self", func(t *testing.T) {
		dir := c.setupMove(t, "rename-self")
		checkNoError(t, rename(t, dir, "src.txt", "src.txt"), "File.Rename of a file to its own name must be a no-op")
		checkNoError(t, rename(t, dir, "srcdir", "srcdir"), "File.Rename of a directory to its own name must be a no-op")
		c.checkSrcIntact(t, dir, "File.Rename to its own name")
	})

	c.run(t, "ToMissing", func(t *testing.T) {
		dir := c.setupMove(t, "rename-missing")
		if checkNoError(t, rename(t, dir, "src.txt", "new.txt"), "File.Rename of a file") {
			c.checkContent(t, dir+"new.txt", srcContent, "File.Rename of a file")
			c.checkMissing(t, dir+"src.txt", "File.Rename of a file must remove the source")
		}
		if checkNoError(t, rename(t, dir, "srcdir", "newdir"), "File.Rename of a non-empty directory") {
			c.checkContent(t, dir+"newdir/child.txt", childContent, "File.Rename of a directory must keep its content")
			c.checkMissing(t, dir+"srcdir", "File.Rename of a directory must remove the source")
		}
	})

	c.run(t, "Overwrite", func(t *testing.T) {
		dir := c.setupMove(t, "rename-overwrite")
		if checkNoError(t, rename(t, dir, "src.txt", "dest.txt"), "File.Rename of a file to the name of an existing file must replace it") {
			c.checkContent(t, dir+"dest.txt", srcContent, "File.Rename onto an existing file must replace its content")
			c.checkMissing(t, dir+"src.txt", "File.Rename onto an existing file must remove the source")
		}
	})

	c.run(t, "OntoDirectory", func(t *testing.T) {
		c.checkRejected(t, "rename-onto-dir", []rejectedMove{
			{"src.txt", "destdir", "File.Rename of a file to the name of an existing directory"},
			{"srcdir", "destdir", "File.Rename of a directory to the name of an existing directory"},
			{"src.txt", "emptydir", "File.Rename of a file to the name of an existing empty directory"},
			{"srcdir", "emptydir", "File.Rename of a directory to the name of an existing empty directory"},
		}, os.ErrExist, func(dir, src, newName string) error { return rename(t, dir, src, newName) })
	})

	c.run(t, "DirectoryOntoFile", func(t *testing.T) {
		c.checkRejected(t, "rename-dir-onto-file", []rejectedMove{
			{"srcdir", "dest.txt", "File.Rename of a directory to the name of an existing file"},
		}, fs.ErrIsNotDirectory{}, func(dir, src, newName string) error { return rename(t, dir, src, newName) })
	})

	c.run(t, "InvalidName", func(t *testing.T) {
		dir := c.setupMove(t, "rename-invalid")
		names := []string{"", ".", "..", "x/y"}
		if sep := c.fs.Separator(); sep != "/" {
			names = append(names, "x"+sep+"y")
		}
		for _, name := range names {
			_, err := c.wfile(dir+"src.txt").Rename(c.ctx, name)
			checkError(t, err, "File.Rename(%q) must fail", name)
		}
		c.checkMissing(t, dir+"x", "File.Rename with a separator in the name must not create a directory")
		c.checkSrcIntact(t, dir, "File.Rename with an invalid name")
		c.checkDestIntact(t, dir, "File.Rename with an invalid name")
	})
}

// testMoveTo checks File.MoveTo, which moves into an existing directory.
func (c *conformance) testMoveTo(t *testing.T) {
	moveTo := func(dir, src, dest string) error {
		return c.wfile(dir+src).MoveTo(c.ctx, c.wfile(dir+dest))
	}

	c.run(t, "Self", func(t *testing.T) {
		dir := c.setupMove(t, "move-to-self")
		checkNoError(t, moveTo(dir, "src.txt", "src.txt"), "File.MoveTo of a file onto itself must be a no-op")
		checkNoError(t, moveTo(dir, "srcdir", "srcdir"), "File.MoveTo of a directory onto itself must be a no-op")
		c.checkSrcIntact(t, dir, "File.MoveTo onto itself")
	})

	c.run(t, "Overwrite", func(t *testing.T) {
		dir := c.setupMove(t, "move-to-overwrite")
		if checkNoError(t, moveTo(dir, "src.txt", "dest.txt"), "File.MoveTo of a file onto an existing file must replace it") {
			c.checkContent(t, dir+"dest.txt", srcContent, "File.MoveTo onto an existing file must replace its content")
			c.checkMissing(t, dir+"src.txt", "File.MoveTo onto an existing file must remove the source")
		}
	})

	c.run(t, "IntoDirectory", func(t *testing.T) {
		dir := c.setupMove(t, "move-to-into-dir")
		if checkNoError(t, moveTo(dir, "src.txt", "destdir"), "File.MoveTo of a file into an existing directory") {
			c.checkContent(t, dir+"destdir/src.txt", srcContent, "File.MoveTo must move the file into the directory")
			c.checkMissing(t, dir+"src.txt", "File.MoveTo into a directory must remove the source")
		}
		if checkNoError(t, moveTo(dir, "srcdir", "destdir"), "File.MoveTo of a directory into an existing directory") {
			c.checkContent(t, dir+"destdir/srcdir/child.txt", childContent, "File.MoveTo must move the directory into the directory")
			c.checkMissing(t, dir+"srcdir", "File.MoveTo of a directory into a directory must remove the source")
		}
		c.checkContent(t, dir+"destdir/keep.txt", keepContent, "File.MoveTo into a directory must keep its content")
	})

	c.run(t, "DirectoryOntoFile", func(t *testing.T) {
		c.checkRejected(t, "move-to-dir-onto-file", []rejectedMove{
			{"srcdir", "dest.txt", "File.MoveTo of a directory onto an existing file"},
		}, fs.ErrIsNotDirectory{}, moveTo)
	})

	c.run(t, "IntoItself", func(t *testing.T) {
		dir := c.setupMove(t, "move-to-into-itself-1")
		checkIntoItself(t, c.ctx, func(ctx context.Context) error {
			return c.wfile(dir+"srcdir").MoveTo(ctx, c.wfile(dir+"srcdir/new"))
		}, "File.MoveTo of a directory into itself")
		c.checkMissing(t, dir+"srcdir/new", "File.MoveTo of a directory into itself must not create the destination")
		c.checkSrcIntact(t, dir, "File.MoveTo of a directory into itself")

		// An existing directory destination resolves to srcdir/sub/srcdir
		dir = c.setupMove(t, "move-to-into-itself-2")
		c.mkdir(t, dir+"srcdir/sub")
		checkIntoItself(t, c.ctx, func(ctx context.Context) error {
			return c.wfile(dir+"srcdir").MoveTo(ctx, c.wfile(dir+"srcdir/sub"))
		}, "File.MoveTo of a directory into a descendant")
		c.checkMissing(t, dir+"srcdir/sub/srcdir", "File.MoveTo of a directory into a descendant must not create the destination")
		c.checkSrcIntact(t, dir, "File.MoveTo of a directory into a descendant")
	})
}

// testCopyFile checks fs.CopyFile, which copies into an existing directory.
func (c *conformance) testCopyFile(t *testing.T) {
	copyFile := func(dir, src, dest string) error {
		return fs.CopyFile(c.ctx, c.wfile(dir+src), c.wfile(dir+dest))
	}

	c.run(t, "Self", func(t *testing.T) {
		dir := c.setupMove(t, "copy-self")
		checkNoError(t, copyFile(dir, "src.txt", "src.txt"), "fs.CopyFile of a file onto itself must be a no-op")
		c.checkSrcIntact(t, dir, "fs.CopyFile onto itself")
	})

	c.run(t, "Overwrite", func(t *testing.T) {
		dir := c.setupMove(t, "copy-overwrite")
		if checkNoError(t, copyFile(dir, "src.txt", "dest.txt"), "fs.CopyFile onto an existing file must replace it") {
			c.checkContent(t, dir+"dest.txt", srcContent, "fs.CopyFile onto an existing file must replace its content")
		}
		c.checkSrcIntact(t, dir, "fs.CopyFile onto an existing file")
	})

	c.run(t, "IntoDirectory", func(t *testing.T) {
		dir := c.setupMove(t, "copy-into-dir")
		if checkNoError(t, copyFile(dir, "src.txt", "destdir"), "fs.CopyFile into an existing directory") {
			c.checkContent(t, dir+"destdir/src.txt", srcContent, "fs.CopyFile must copy the file into the directory")
		}
		c.checkContent(t, dir+"destdir/keep.txt", keepContent, "fs.CopyFile into a directory must keep its content")
		c.checkSrcIntact(t, dir, "fs.CopyFile into an existing directory")
	})

	c.run(t, "MissingParents", func(t *testing.T) {
		dir := c.setupMove(t, "copy-missing-parents")
		if checkNoError(t, copyFile(dir, "src.txt", "new/parents/dest.txt"), "fs.CopyFile must create missing parent directories") {
			c.checkContent(t, dir+"new/parents/dest.txt", srcContent, "fs.CopyFile into new parent directories")
		}
	})
}
