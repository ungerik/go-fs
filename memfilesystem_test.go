package fs

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMemFileSystem(t *testing.T) {
	for _, sep := range []string{`/`, `\`} {
		fs, err := NewMemFileSystem(sep)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
		require.True(t, fs.RootDir().Exists(), "root dir exists")
		require.True(t, fs.RootDir().IsDir(), "root is dir")

		fs = fs.WithVolume("C:")
		require.True(t, strings.HasSuffix(fs.Prefix(), "C:"))
		require.True(t, strings.HasSuffix(string(fs.RootDir()), "C:"+sep))

		err = fs.Close()
		require.NoError(t, err)
		require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
		require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")
	}
}

func TestNewSingleMemFileSystem(t *testing.T) {
	fs, f, err := NewSingleMemFileSystem(NewMemFile("test.txt", []byte("Hello, World!")))
	require.NoError(t, err, "NewSingleMemFileSystem")

	t.Cleanup(func() { _ = fs.Close() })

	// Check fs
	require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
	require.True(t, fs.RootDir().Exists(), "root directory exists")
	require.True(t, fs.RootDir().IsDir(), "root is a directory")
	files, err := fs.RootDir().ListDirMax(-1)
	require.NoError(t, err, "ListDirMax")
	require.Len(t, files, 1, "root directory contains one file")
	require.Equal(t, "test.txt", files[0].Name(), "root directory contains test.txt")

	// Check non-existent file
	require.False(t, fs.RootDir().Join("non-existent.txt").Exists(), "non-existent.txt does not exists")
	require.False(t, fs.RootDir().Join("non-existent.txt").IsDir(), "non-existent.txt is not a directory")
	require.False(t, f.Join("non-existent.txt").Exists(), "test.txt/non-existent.txt does not exists")

	// Check test.txt
	require.True(t, f.Exists(), "test.txt exists")
	require.False(t, f.IsDir(), "test.txt is not a directory")
	require.True(t, f.Dir().Exists(), "root directory exists")
	require.True(t, f.Dir().IsDir(), "root is a directory")
	content, err := f.ReadAllString()
	require.NoError(t, err, "ReadAllString")
	require.Equal(t, "Hello, World!", content)

	err = fs.Close()
	require.NoError(t, err, "Close")
	require.False(t, f.Exists(), "test.txt does not exist after close")
	require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
	require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")
}

func TestMemFileSystem(t *testing.T) {
	memFS, err := NewMemFileSystem("/")
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, memFS.Close())
	})

	testDir := "/test"
	err = memFS.MakeDir(testDir, nil)
	require.NoError(t, err, "Failed to create test directory")

	RunFileSystemTests(
		t.Context(),
		t,
		memFS,
		"memory file system", // name
		memFS.Prefix(),       // prefix
		testDir,              // testDir
	)
}

func TestMemFileSystem_FullFeatures(t *testing.T) {
	t.Run("Rename", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("a.txt", []byte("hello")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		src := memFS.RootDir().Join("a.txt")
		require.True(t, src.Exists())

		renamed, err := src.Rename("b.txt")
		require.NoError(t, err)
		require.True(t, renamed.Exists(), "new path exists")
		require.False(t, src.Exists(), "old path gone")
		require.Equal(t, "b.txt", renamed.Name())
		content, err := renamed.ReadAllString()
		require.NoError(t, err)
		require.Equal(t, "hello", content, "content preserved across rename")

		// Renaming a directory works the same way.
		require.NoError(t, memFS.MakeDir("/d1", nil))
		dir := memFS.RootDir().Join("d1")
		dir2, err := dir.Rename("d2")
		require.NoError(t, err)
		require.True(t, dir2.IsDir())
		require.False(t, dir.Exists())

		// Rejects separator in newName.
		_, err = renamed.Rename("with/slash.txt")
		require.Error(t, err)

		// Rejects collision.
		_, err = memFS.RootDir().Join("c.txt").Rename("b.txt")
		require.Error(t, err) // source missing — also an error
		require.NoError(t, memFS.WriteAll(t.Context(), "/c.txt", []byte("c"), nil))
		_, err = memFS.RootDir().Join("c.txt").Rename("b.txt")
		require.Error(t, err, "should reject existing target name")
	})

	t.Run("Move", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.MakeAllDirs("/sub/nested", nil))

		// Move into existing directory uses base name.
		require.NoError(t, memFS.Move("/a.txt", "/sub"))
		require.True(t, memFS.RootDir().Join("sub", "a.txt").Exists())
		require.False(t, memFS.RootDir().Join("a.txt").Exists())

		// Move to explicit new path.
		require.NoError(t, memFS.Move("/sub/a.txt", "/sub/nested/renamed.txt"))
		require.True(t, memFS.RootDir().Join("sub", "nested", "renamed.txt").Exists())
		require.False(t, memFS.RootDir().Join("sub", "a.txt").Exists())

		// Collision is rejected.
		require.NoError(t, memFS.WriteAll(t.Context(), "/sub/blocker.txt", []byte("x"), nil))
		require.Error(t, memFS.Move("/sub/nested/renamed.txt", "/sub/blocker.txt"))

		// Missing source.
		require.Error(t, memFS.Move("/nope.txt", "/sub"))

		// Moving a directory into a descendant is rejected and leaves the
		// source intact (regression for an orphaning bug).
		require.NoError(t, memFS.MakeAllDirs("/cyc/inner", nil))
		require.Error(t, memFS.Move("/cyc", "/cyc/inner/sub"), "into descendant")
		require.True(t, memFS.RootDir().Join("cyc").Exists(), "source survives rejected move")
		require.True(t, memFS.RootDir().Join("cyc", "inner").Exists(), "subtree survives rejected move")

		// Move(src, src) on a regular file is a no-op, matching
		// LocalFileSystem and the MoveFileSystem contract.
		require.NoError(t, memFS.WriteAll(t.Context(), "/same.txt", []byte("z"), nil))
		require.NoError(t, memFS.Move("/same.txt", "/same.txt"), "Move(file, file) must be a no-op")
		got, err := memFS.RootDir().Join("same.txt").ReadAllString()
		require.NoError(t, err)
		require.Equal(t, "z", got, "file content preserved")

		// Move(src, src) on a directory is also a no-op (the directory-
		// append rule does not apply when src and dest are already equal).
		require.NoError(t, memFS.Move("/cyc", "/cyc"), "Move(dir, dir) must be a no-op")
		require.True(t, memFS.RootDir().Join("cyc", "inner").Exists(), "subtree preserved")

		// Move(/a, /) becomes Move(/a, /a) after the directory-append step
		// and must also be a no-op.
		require.NoError(t, memFS.MakeDir("/movable", nil))
		require.NoError(t, memFS.Move("/movable", "/"), "Move(/a, /) collapses to a no-op")
		require.True(t, memFS.RootDir().Join("movable").Exists(), "subject survives the no-op")
	})

	t.Run("Permissions_User_Group", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.SetPermissions("/a.txt", AllReadWrite))
		require.NoError(t, memFS.SetUser("/a.txt", "alice"))
		require.NoError(t, memFS.SetGroup("/a.txt", "staff"))

		u, err := memFS.User("/a.txt")
		require.NoError(t, err)
		require.Equal(t, "alice", u)

		g, err := memFS.Group("/a.txt")
		require.NoError(t, err)
		require.Equal(t, "staff", g)

		// Missing file -> error
		_, err = memFS.User("/missing.txt")
		require.Error(t, err)
		require.Error(t, memFS.SetUser("/missing.txt", "bob"))

		// Read-only FS rejects setters
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.SetUser("/a.txt", "bob"), ErrReadOnlyFileSystem)
		require.ErrorIs(t, memFS.SetGroup("/a.txt", "wheel"), ErrReadOnlyFileSystem)
		require.ErrorIs(t, memFS.SetPermissions("/a.txt", AllRead), ErrReadOnlyFileSystem)
		memFS.SetReadOnly(false)
	})

	t.Run("XAttr", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.SetXAttr("/a.txt", "user.tag", []byte("v1"), 0, true))
		require.NoError(t, memFS.SetXAttr("/a.txt", "user.other", []byte("v2"), 0, true))

		got, err := memFS.GetXAttr("/a.txt", "user.tag", true)
		require.NoError(t, err)
		require.Equal(t, []byte("v1"), got)

		names, err := memFS.ListXAttr("/a.txt", true)
		require.NoError(t, err)
		require.Equal(t, []string{"user.other", "user.tag"}, names, "sorted keys")

		// XATTR_CREATE rejects existing
		require.Error(t, memFS.SetXAttr("/a.txt", "user.tag", []byte("new"), xattrCreate, true))
		// XATTR_REPLACE rejects missing
		require.Error(t, memFS.SetXAttr("/a.txt", "user.absent", []byte("z"), xattrReplace, true))

		// Remove and verify
		require.NoError(t, memFS.RemoveXAttr("/a.txt", "user.tag", true))
		_, err = memFS.GetXAttr("/a.txt", "user.tag", true)
		require.Error(t, err)
		require.Error(t, memFS.RemoveXAttr("/a.txt", "user.tag", true), "removing missing attr errors")
	})

	t.Run("ListDirMax", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/",
			NewMemFile("a.txt", []byte("a")),
			NewMemFile("b.txt", []byte("b")),
			NewMemFile("c.log", []byte("c")),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		all, err := memFS.ListDirMax(t.Context(), "/", -1, nil)
		require.NoError(t, err)
		require.Len(t, all, 3)

		two, err := memFS.ListDirMax(t.Context(), "/", 2, nil)
		require.NoError(t, err)
		require.Len(t, two, 2)

		zero, err := memFS.ListDirMax(t.Context(), "/", 0, nil)
		require.NoError(t, err)
		require.Empty(t, zero)

		txt, err := memFS.ListDirMax(t.Context(), "/", -1, []string{"*.txt"})
		require.NoError(t, err)
		require.Len(t, txt, 2)
	})

	t.Run("ListDirInfoRecursive", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/")
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.MakeAllDirs("/a/b", nil))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/b/c.txt", []byte("c"), nil))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/b/d.txt", []byte("d"), nil))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/e.txt", []byte("e"), nil))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/skip.bin", []byte("x"), nil))

		var collected []string
		err = memFS.ListDirInfoRecursive(t.Context(), "/a", func(info *FileInfo) error {
			collected = append(collected, info.Name)
			return nil
		}, []string{"*.txt"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"c.txt", "d.txt", "e.txt"}, collected)

		// Context cancel
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err = memFS.ListDirInfoRecursive(ctx, "/a", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("Symlinks", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("real.txt", []byte("R")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.CreateSymbolicLink("/real.txt", "/link"))
		require.True(t, memFS.IsSymbolicLink("/link"))
		require.False(t, memFS.IsSymbolicLink("/real.txt"))

		target, err := memFS.ReadSymbolicLink("/link")
		require.NoError(t, err)
		require.Equal(t, "/real.txt", target)

		// ReadSymbolicLink on a regular file errors
		_, err = memFS.ReadSymbolicLink("/real.txt")
		require.Error(t, err)

		// Collision: creating link where node already exists
		require.Error(t, memFS.CreateSymbolicLink("/real.txt", "/link"))

		// Removing the link does not touch the target
		require.NoError(t, memFS.Remove("/link"))
		require.False(t, memFS.IsSymbolicLink("/link"))
		require.True(t, memFS.RootDir().Join("real.txt").Exists())
	})

	t.Run("Watch", func(t *testing.T) {
		memFS, err := NewMemFileSystem("/", NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		events := make(chan Event, 16)
		cancelDir, err := memFS.Watch("/", func(_ File, e Event) { events <- e })
		require.NoError(t, err)

		// Touch existing -> Chmod
		require.NoError(t, memFS.Touch("/a.txt", nil))

		// WriteAll on new file -> Create|Write
		require.NoError(t, memFS.WriteAll(t.Context(), "/b.txt", []byte("b"), nil))

		// SetPermissions -> Chmod
		require.NoError(t, memFS.SetPermissions("/a.txt", AllRead))

		// Remove -> Remove
		require.NoError(t, memFS.Remove("/b.txt"))

		// Collect events with a short timeout
		var (
			seenWrite bool
			seenChmod bool
			seenRem   bool
			deadline  = time.After(2 * time.Second)
		)
		for !(seenWrite && seenChmod && seenRem) {
			select {
			case e := <-events:
				if e.HasWrite() {
					seenWrite = true
				}
				if e.HasChmod() {
					seenChmod = true
				}
				if e.HasRemove() {
					seenRem = true
				}
			case <-deadline:
				t.Fatalf("timed out waiting for events: write=%v chmod=%v remove=%v", seenWrite, seenChmod, seenRem)
			}
		}

		// Cancel and verify no further events flow
		require.NoError(t, cancelDir())

		// Drain anything still queued from earlier dispatches
		for {
			select {
			case <-events:
			case <-time.After(50 * time.Millisecond):
				goto drained
			}
		}
	drained:
		require.NoError(t, memFS.WriteAll(t.Context(), "/c.txt", []byte("c"), nil))
		select {
		case e := <-events:
			t.Fatalf("unexpected event after cancel: %s", e)
		case <-time.After(100 * time.Millisecond):
		}

		// Close should not crash on subsequent mutations
		require.NoError(t, memFS.Close())
	})
}

// newTestMemFS returns a MemFileSystem that is cleaned up at the end of the
// test. Closing twice (here + manual Close inside the test) is a no-op.
func newTestMemFS(t *testing.T, initial ...MemFile) *MemFileSystem {
	t.Helper()
	memFS, err := NewMemFileSystem("/", initial...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	return memFS
}

func TestMemFileSystem_Constructor_EdgeCases(t *testing.T) {
	t.Run("InvalidSeparator", func(t *testing.T) {
		for _, sep := range []string{"", ":", "//", "|"} {
			_, err := NewMemFileSystem(sep)
			require.Error(t, err, "separator %q must be rejected", sep)
		}
	})

	t.Run("EmptyInitialFileName", func(t *testing.T) {
		_, err := NewMemFileSystem("/", MemFile{FileName: ""})
		require.Error(t, err, "empty initial filename must be rejected")
	})

	t.Run("InitialFilesWithPath", func(t *testing.T) {
		memFS, err := NewMemFileSystem(
			"/",
			NewMemFile("docs/readme.txt", []byte("R")),
			NewMemFile("docs/sub/deep.txt", []byte("D")),
			NewMemFile("top.txt", []byte("T")),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.True(t, memFS.Exists("/docs"))
		require.True(t, memFS.Exists("/docs/sub"))
		require.True(t, memFS.Exists("/docs/readme.txt"))
		require.True(t, memFS.Exists("/docs/sub/deep.txt"))
		require.True(t, memFS.Exists("/top.txt"))

		data, err := memFS.ReadAll(t.Context(), "/docs/sub/deep.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("D"), data)
	})

	t.Run("Registered", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.True(t, IsRegistered(memFS), "fresh fs is registered")
	})
}

func TestMemFileSystem_WithIDVolume(t *testing.T) {
	memFS := newTestMemFS(t)
	origPrefix := memFS.Prefix()

	// WithID(sameID) returns identity, no re-registration churn.
	id, _ := memFS.ID()
	same := memFS.WithID(id)
	require.Same(t, memFS, same)
	require.Equal(t, origPrefix, memFS.Prefix())

	// New ID changes the prefix and keeps the fs registered.
	memFS.WithID("test-id")
	require.NotEqual(t, origPrefix, memFS.Prefix())
	require.True(t, strings.HasSuffix(memFS.Prefix(), "test-id"))
	require.True(t, IsRegistered(memFS))

	// WithVolume(sameVolume) is identity; WithVolume(new) updates prefix.
	require.Same(t, memFS, memFS.WithVolume(""))
	memFS.WithVolume("V:")
	require.True(t, strings.HasSuffix(memFS.Prefix(), "V:"), "prefix gets volume suffix")
	require.Equal(t, "V:", memFS.Volume())
}

func TestMemFileSystem_AddMemFile_EdgeCases(t *testing.T) {
	t.Run("EmptyFilename", func(t *testing.T) {
		memFS := newTestMemFS(t)
		_, err := memFS.AddMemFile(MemFile{FileName: ""}, time.Now())
		require.Error(t, err)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t)
		memFS.SetReadOnly(true)
		_, err := memFS.AddMemFile(NewMemFile("x.txt", []byte("x")), time.Now())
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})

	t.Run("PathCreatesParents", func(t *testing.T) {
		memFS := newTestMemFS(t)
		f, err := memFS.AddMemFile(NewMemFile("a/b/c.txt", []byte("X")), time.Now())
		require.NoError(t, err)
		require.True(t, f.Exists())
		require.True(t, memFS.Exists("/a"))
		require.True(t, memFS.Exists("/a/b"))
	})
}

func TestMemFileSystem_MakeDir_EdgeCases(t *testing.T) {
	t.Run("EmptyPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.MakeDir("", nil), ErrEmptyPath)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t)
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.MakeDir("/d", nil), ErrReadOnlyFileSystem)
	})

	t.Run("AlreadyExists", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.NoError(t, memFS.MakeDir("/d", nil))
		err := memFS.MakeDir("/d", nil)
		require.Error(t, err)
		var aexists ErrAlreadyExists
		require.ErrorAs(t, err, &aexists, "MakeDir on existing dir returns ErrAlreadyExists")
	})

	t.Run("ParentMissing", func(t *testing.T) {
		memFS := newTestMemFS(t)
		err := memFS.MakeDir("/no/such/parent/d", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("f.txt", []byte("x")))
		err := memFS.MakeDir("/f.txt/sub", nil)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

func TestMemFileSystem_MakeAllDirs_EdgeCases(t *testing.T) {
	t.Run("EmptyPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.MakeAllDirs("", nil), ErrEmptyPath)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t)
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.MakeAllDirs("/a/b/c", nil), ErrReadOnlyFileSystem)
	})

	t.Run("IdempotentOnExistingDirs", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.NoError(t, memFS.MakeAllDirs("/a/b/c", nil))
		// Second call must not error — already-existing components are
		// silently skipped, matching the os.MkdirAll contract.
		require.NoError(t, memFS.MakeAllDirs("/a/b/c", nil))
	})

	t.Run("PathComponentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a/f.txt", []byte("x")))
		err := memFS.MakeAllDirs("/a/f.txt/d", nil)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

func TestMemFileSystem_Stat_And_Exists(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", nil))
	require.NoError(t, memFS.CreateSymbolicLink("/a.txt", "/link"))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.Stat("")
		require.ErrorIs(t, err, ErrEmptyPath)
		require.False(t, memFS.Exists(""))
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.Stat("/missing")
		require.Error(t, err)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.False(t, memFS.Exists("/missing"))
	})

	t.Run("FileMode", func(t *testing.T) {
		info, err := memFS.Stat("/a.txt")
		require.NoError(t, err)
		require.False(t, info.IsDir())
		require.Equal(t, int64(3), info.Size())
		require.Zero(t, info.Mode()&iofs.ModeDir)
		require.Zero(t, info.Mode()&iofs.ModeSymlink)
	})

	t.Run("DirMode", func(t *testing.T) {
		info, err := memFS.Stat("/d")
		require.NoError(t, err)
		require.True(t, info.IsDir())
		require.NotZero(t, info.Mode()&iofs.ModeDir)
	})

	t.Run("SymlinkMode", func(t *testing.T) {
		info, err := memFS.Stat("/link")
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&iofs.ModeSymlink, "Mode must carry ModeSymlink")
		require.Nil(t, info.Sys())
	})
}

func TestMemFileSystem_ReadAll_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", nil))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.ReadAll(t.Context(), "")
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.ReadAll(t.Context(), "/missing")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := memFS.ReadAll(ctx, "/a.txt")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("OnDirectoryReturnsNilData", func(t *testing.T) {
		// ReadAll on a directory returns its (always-nil) FileData rather
		// than erroring. Documenting the current behavior so a future
		// change is intentional.
		data, err := memFS.ReadAll(t.Context(), "/d")
		require.NoError(t, err)
		require.Empty(t, data)
	})
}

func TestMemFileSystem_WriteAll_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t)

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.WriteAll(t.Context(), "", nil, nil), ErrEmptyPath)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.WriteAll(ctx, "/x.txt", []byte("x"), nil), context.Canceled)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.WriteAll(t.Context(), "/no/such/x.txt", []byte("x"), nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("OverwriteExisting", func(t *testing.T) {
		require.NoError(t, memFS.WriteAll(t.Context(), "/over.txt", []byte("first"), nil))
		require.NoError(t, memFS.WriteAll(t.Context(), "/over.txt", []byte("second"), nil))
		data, err := memFS.ReadAll(t.Context(), "/over.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("second"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.WriteAll(t.Context(), "/ro.txt", []byte("x"), nil), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Append_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t)

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Append(t.Context(), "", []byte("x"), nil), ErrEmptyPath)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.Append(ctx, "/x.txt", []byte("x"), nil), context.Canceled)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.Append(t.Context(), "/no/dir/x.txt", []byte("x"), nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("CreateThenAppend", func(t *testing.T) {
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("a"), nil))
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("b"), nil))
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("c"), nil))
		data, err := memFS.ReadAll(t.Context(), "/log.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("abc"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Append(t.Context(), "/log.txt", []byte("z"), nil), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Touch_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Touch("", nil), ErrEmptyPath)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.Touch("/no/dir/x.txt", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("UpdatesModTime", func(t *testing.T) {
		info, err := memFS.Stat("/a.txt")
		require.NoError(t, err)
		original := info.ModTime()

		time.Sleep(2 * time.Millisecond)
		require.NoError(t, memFS.Touch("/a.txt", nil))

		info, err = memFS.Stat("/a.txt")
		require.NoError(t, err)
		require.True(t, info.ModTime().After(original), "Touch must advance ModTime")
	})

	t.Run("CreatesIfMissing", func(t *testing.T) {
		require.False(t, memFS.Exists("/new.txt"))
		require.NoError(t, memFS.Touch("/new.txt", nil))
		require.True(t, memFS.Exists("/new.txt"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Touch("/another.txt", nil), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Truncate_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")))

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Truncate("", 0), ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		require.ErrorIs(t, memFS.Truncate("/missing", 0), os.ErrNotExist)
	})

	t.Run("SameSizeNoOp", func(t *testing.T) {
		require.NoError(t, memFS.Truncate("/a.txt", 5))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello"), data)
	})

	t.Run("ZeroPadsWhenExtending", func(t *testing.T) {
		require.NoError(t, memFS.Truncate("/a.txt", 8))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello\x00\x00\x00"), data)
	})

	t.Run("ShrinkPreservesPrefix", func(t *testing.T) {
		require.NoError(t, memFS.Truncate("/a.txt", 3))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hel"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Truncate("/a.txt", 1), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenReader_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", nil))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenReader("")
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.OpenReader("/missing")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenReader("/d")
		require.Error(t, err)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ReadCloseTwiceOK", func(t *testing.T) {
		r, err := memFS.OpenReader("/a.txt")
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, []byte("abc"), data)
		require.NoError(t, r.Close())
		// Re-closing must not panic; many wrappers reuse a closed reader.
		_ = r.Close()
	})
}

func TestMemFileSystem_OpenWriter_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("existing")), NewMemFile("f.txt", []byte("file")))
	require.NoError(t, memFS.MakeDir("/d", nil))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenWriter("", nil)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenWriter("/d", nil)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenWriter("/no/dir/x.txt", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenWriter("/f.txt/x.txt", nil)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("TruncatesExisting", func(t *testing.T) {
		w, err := memFS.OpenWriter("/a.txt", nil)
		require.NoError(t, err)
		_, err = w.Write([]byte("X"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		// Re-close is a no-op.
		require.NoError(t, w.Close())

		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("X"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.OpenWriter("/new.txt", nil)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenAppendWriter_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")), NewMemFile("f.txt", []byte("f")))
	require.NoError(t, memFS.MakeDir("/d", nil))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("", nil)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/d", nil)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/no/dir/x.txt", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/f.txt/x.txt", nil)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("AppendsToExisting", func(t *testing.T) {
		w, err := memFS.OpenAppendWriter("/a.txt", nil)
		require.NoError(t, err)
		_, err = w.Write([]byte(" world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello world"), data)
	})

	t.Run("CreatesNewFile", func(t *testing.T) {
		w, err := memFS.OpenAppendWriter("/new.txt", nil)
		require.NoError(t, err)
		_, err = w.Write([]byte("z"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		require.True(t, memFS.Exists("/new.txt"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.OpenAppendWriter("/x.txt", nil)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenReadWriter_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abcdef")), NewMemFile("f.txt", []byte("f")))
	require.NoError(t, memFS.MakeDir("/d", nil))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("", nil)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/d", nil)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/no/dir/x.txt", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/f.txt/x.txt", nil)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("SeekReadWriteAt", func(t *testing.T) {
		rw, err := memFS.OpenReadWriter("/a.txt", nil)
		require.NoError(t, err)

		// Read whole file.
		buf := make([]byte, 6)
		n, err := rw.Read(buf)
		require.NoError(t, err)
		require.Equal(t, 6, n)
		require.Equal(t, []byte("abcdef"), buf)

		// Seek to start and overwrite middle byte via WriteAt.
		_, err = rw.Seek(0, io.SeekStart)
		require.NoError(t, err)
		n, err = rw.WriteAt([]byte("Z"), 2)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		// ReadAt
		got := make([]byte, 1)
		n, err = rw.ReadAt(got, 2)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, byte('Z'), got[0])

		require.NoError(t, rw.Close())
		// Re-close is harmless.
		_ = rw.Close()

		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("abZdef"), data)
	})

	t.Run("CreatesNewFile", func(t *testing.T) {
		rw, err := memFS.OpenReadWriter("/new-rw.txt", nil)
		require.NoError(t, err)
		_, err = rw.Write([]byte("hi"))
		require.NoError(t, err)
		require.NoError(t, rw.Close())

		data, err := memFS.ReadAll(t.Context(), "/new-rw.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hi"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.OpenReadWriter("/a.txt", nil)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Remove_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
	require.NoError(t, memFS.MakeAllDirs("/d/sub", nil))
	require.NoError(t, memFS.WriteAll(t.Context(), "/d/sub/inner.txt", []byte("x"), nil))

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Remove(""), ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		require.ErrorIs(t, memFS.Remove("/missing"), os.ErrNotExist)
	})

	t.Run("Root", func(t *testing.T) {
		err := memFS.Remove("/")
		require.Error(t, err, "removing root must fail")
	})

	t.Run("RemovingDirDropsSubtree", func(t *testing.T) {
		// Current semantics: Remove on a directory drops it (and the
		// entire subtree) atomically. Documented here so a future change
		// to e.g. require ENOTEMPTY is intentional.
		require.NoError(t, memFS.Remove("/d"))
		require.False(t, memFS.Exists("/d"))
		require.False(t, memFS.Exists("/d/sub/inner.txt"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Remove("/a.txt"), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Rename_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))

	t.Run("EmptyInputs", func(t *testing.T) {
		_, err := memFS.Rename("", "x")
		require.ErrorIs(t, err, ErrEmptyPath)
		_, err = memFS.Rename("/a.txt", "")
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("NewNameWithSeparator", func(t *testing.T) {
		_, err := memFS.Rename("/a.txt", "sub/b.txt")
		require.Error(t, err)
	})

	t.Run("Root", func(t *testing.T) {
		_, err := memFS.Rename("/", "new-root")
		require.Error(t, err, "renaming root must fail")
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.Rename("/missing", "x.txt")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.Rename("/a.txt", "b.txt")
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Move_EdgeCases(t *testing.T) {
	t.Run("EmptyInputs", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		require.ErrorIs(t, memFS.Move("", "/x"), ErrEmptyPath)
		require.ErrorIs(t, memFS.Move("/a.txt", ""), ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.Move("/missing", "/x"), os.ErrNotExist)
	})

	t.Run("Root", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.Error(t, memFS.Move("/", "/x"), "moving root must fail")
	})

	t.Run("ParentMissing", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		err := memFS.Move("/a.txt", "/no/dir/b.txt")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t,
			NewMemFile("a.txt", []byte("a")),
			NewMemFile("blocker.txt", []byte("b")),
		)
		err := memFS.Move("/a.txt", "/blocker.txt/x")
		require.Error(t, err)
		// Either ErrIsNotDirectory or ErrAlreadyExists is acceptable; both
		// signal that the move can't succeed. Verify that at least one
		// rejection error type is present in the chain.
		var notDir ErrIsNotDirectory
		var aexists ErrAlreadyExists
		require.True(t, errors.As(err, &notDir) || errors.As(err, &aexists),
			"unexpected error: %v", err)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.Move("/a.txt", "/b.txt"), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_CopyFile_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")))

	t.Run("EmptyInputs", func(t *testing.T) {
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "", "/x", nil), ErrEmptyPath)
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "/a.txt", "", nil), ErrEmptyPath)
	})

	t.Run("MissingSource", func(t *testing.T) {
		err := memFS.CopyFile(t.Context(), "/missing", "/x", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.CopyFile(ctx, "/a.txt", "/dst.txt", nil), context.Canceled)
	})

	t.Run("Success", func(t *testing.T) {
		require.NoError(t, memFS.CopyFile(t.Context(), "/a.txt", "/copy.txt", nil))
		data, err := memFS.ReadAll(t.Context(), "/copy.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "/a.txt", "/new.txt", nil), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Symlinks_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("real.txt", []byte("R")))

	t.Run("CreateEmptyInputs", func(t *testing.T) {
		require.ErrorIs(t, memFS.CreateSymbolicLink("", "/link"), ErrEmptyPath)
		require.ErrorIs(t, memFS.CreateSymbolicLink("/real.txt", ""), ErrEmptyPath)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		require.Error(t, memFS.CreateSymbolicLink("/real.txt", "/no/dir/link"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		err := memFS.CreateSymbolicLink("/real.txt", "/link")
		memFS.SetReadOnly(false)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})

	t.Run("ReadLinkEmptyOrMissing", func(t *testing.T) {
		_, err := memFS.ReadSymbolicLink("")
		require.ErrorIs(t, err, ErrEmptyPath)
		_, err = memFS.ReadSymbolicLink("/missing")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("IsSymbolicLinkEmptyMissing", func(t *testing.T) {
		require.False(t, memFS.IsSymbolicLink(""))
		require.False(t, memFS.IsSymbolicLink("/missing"))
	})
}

func TestMemFileSystem_XAttr_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))

	t.Run("EmptyPaths", func(t *testing.T) {
		_, err := memFS.GetXAttr("", "n", true)
		require.ErrorIs(t, err, ErrEmptyPath)
		_, err = memFS.ListXAttr("", true)
		require.ErrorIs(t, err, ErrEmptyPath)
		require.ErrorIs(t, memFS.SetXAttr("", "n", []byte("v"), 0, true), ErrEmptyPath)
		require.ErrorIs(t, memFS.RemoveXAttr("", "n", true), ErrEmptyPath)
	})

	t.Run("MissingNode", func(t *testing.T) {
		_, err := memFS.GetXAttr("/missing", "n", true)
		require.ErrorIs(t, err, os.ErrNotExist)
		_, err = memFS.ListXAttr("/missing", true)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.ErrorIs(t, memFS.SetXAttr("/missing", "n", []byte("v"), 0, true), os.ErrNotExist)
		require.ErrorIs(t, memFS.RemoveXAttr("/missing", "n", true), os.ErrNotExist)
	})

	t.Run("ListEmpty", func(t *testing.T) {
		names, err := memFS.ListXAttr("/a.txt", true)
		require.NoError(t, err)
		require.Empty(t, names)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.SetXAttr("/a.txt", "n", []byte("v"), 0, true), ErrReadOnlyFileSystem)
		require.ErrorIs(t, memFS.RemoveXAttr("/a.txt", "n", true), ErrReadOnlyFileSystem)
		memFS.SetReadOnly(false)
	})
}

func TestMemFileSystem_ListDir_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))

	t.Run("ListDirInfo_EmptyPath", func(t *testing.T) {
		err := memFS.ListDirInfo(t.Context(), "", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("ListDirInfo_Missing", func(t *testing.T) {
		err := memFS.ListDirInfo(t.Context(), "/missing", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ListDirInfo_OnFile", func(t *testing.T) {
		err := memFS.ListDirInfo(t.Context(), "/a.txt", func(*FileInfo) error { return nil }, nil)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("ListDirInfo_CanceledCtx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := memFS.ListDirInfo(ctx, "/", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ListDirInfo_CallbackError", func(t *testing.T) {
		sentinel := errors.New("stop")
		err := memFS.ListDirInfo(t.Context(), "/", func(*FileInfo) error { return sentinel }, nil)
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("ListDirMax_Errors", func(t *testing.T) {
		_, err := memFS.ListDirMax(t.Context(), "", -1, nil)
		require.ErrorIs(t, err, ErrEmptyPath)

		_, err = memFS.ListDirMax(t.Context(), "/missing", -1, nil)
		require.ErrorIs(t, err, os.ErrNotExist)

		_, err = memFS.ListDirMax(t.Context(), "/a.txt", -1, nil)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = memFS.ListDirMax(ctx, "/", -1, nil)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ListDirInfoRecursive_Errors", func(t *testing.T) {
		err := memFS.ListDirInfoRecursive(t.Context(), "", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, ErrEmptyPath)

		err = memFS.ListDirInfoRecursive(t.Context(), "/missing", func(*FileInfo) error { return nil }, nil)
		require.ErrorIs(t, err, os.ErrNotExist)

		err = memFS.ListDirInfoRecursive(t.Context(), "/a.txt", func(*FileInfo) error { return nil }, nil)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

func TestMemFileSystem_Watch_EdgeCases(t *testing.T) {
	t.Run("EmptyPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		_, err := memFS.Watch("", func(File, Event) {})
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("NilCallback", func(t *testing.T) {
		memFS := newTestMemFS(t)
		_, err := memFS.Watch("/", nil)
		require.Error(t, err)
	})

	t.Run("MissingPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		_, err := memFS.Watch("/missing", func(File, Event) {})
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("FileWatchReceivesOwnEvents", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		events := make(chan Event, 8)
		cancel, err := memFS.Watch("/a.txt", func(_ File, e Event) { events <- e })
		require.NoError(t, err)
		t.Cleanup(func() { _ = cancel() })

		require.NoError(t, memFS.WriteAll(t.Context(), "/a.txt", []byte("Z"), nil))

		select {
		case e := <-events:
			require.True(t, e.HasWrite())
		case <-time.After(2 * time.Second):
			t.Fatal("expected write event on file watch")
		}
	})

	t.Run("MultipleSubscribers", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		var wg sync.WaitGroup
		wg.Add(2)
		c1, err := memFS.Watch("/", func(File, Event) { wg.Done() })
		require.NoError(t, err)
		t.Cleanup(func() { _ = c1() })
		c2, err := memFS.Watch("/", func(File, Event) { wg.Done() })
		require.NoError(t, err)
		t.Cleanup(func() { _ = c2() })

		require.NoError(t, memFS.Touch("/a.txt", nil))

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("not all subscribers were called")
		}
	})

	t.Run("CancelTwiceIsSafe", func(t *testing.T) {
		memFS := newTestMemFS(t)
		cancel, err := memFS.Watch("/", func(File, Event) {})
		require.NoError(t, err)
		require.NoError(t, cancel())
		require.NoError(t, cancel(), "second cancel must be a no-op")
	})

	t.Run("RootEventDispatchedOnce", func(t *testing.T) {
		// Bug regression: a directory watch on "/" used to receive every
		// root-level event twice — once because the path matched the
		// watch directly, once because root is its own parent. Verify
		// exactly one delivery.
		memFS := newTestMemFS(t)
		var n int
		var mu sync.Mutex
		cancel, err := memFS.Watch("/", func(File, Event) {
			mu.Lock()
			n++
			mu.Unlock()
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cancel() })

		require.NoError(t, memFS.WriteAll(t.Context(), "/x.txt", []byte("x"), nil))

		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		got := n
		mu.Unlock()
		require.Equal(t, 1, got, "root watch must fire exactly once per root-child event")
	})
}

func TestMemFileSystem_Close_Idempotent(t *testing.T) {
	memFS, err := NewMemFileSystem("/")
	require.NoError(t, err)

	require.True(t, IsRegistered(memFS))
	require.NoError(t, memFS.Close())
	require.False(t, IsRegistered(memFS), "Close unregisters the fs")
	require.NoError(t, memFS.Close(), "second Close is a no-op")

	// Stat on a path after Close must not panic; just returns some error
	// (the implementation reports parent-is-not-a-directory today; see TODO).
	_, err = memFS.Stat("/anything")
	require.Error(t, err)
}

func TestMemFileSystem_Clear(t *testing.T) {
	memFS := newTestMemFS(t,
		NewMemFile("a.txt", []byte("a")),
		NewMemFile("sub/b.txt", []byte("b")),
	)
	require.True(t, memFS.Exists("/a.txt"))
	require.True(t, memFS.Exists("/sub/b.txt"))

	memFS.Clear()

	require.False(t, memFS.Exists("/a.txt"), "Clear removes top-level files")
	require.False(t, memFS.Exists("/sub"), "Clear removes top-level dirs")

	// Filesystem is still usable.
	require.NoError(t, memFS.WriteAll(t.Context(), "/after.txt", []byte("ok"), nil))
	require.True(t, memFS.Exists("/after.txt"))
}

func TestMemFileSystem_Concurrent(t *testing.T) {
	// Hammer the filesystem from many goroutines to exercise the rwmutex
	// under -race. The test only asserts that operations don't error;
	// the race detector catches actual concurrency bugs.
	memFS := newTestMemFS(t)
	require.NoError(t, memFS.MakeDir("/shared", nil))

	const workers = 16
	const opsPerWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				path := "/shared/" + string(rune('a'+(id%26))) + "-" + string(rune('a'+(i%26))) + ".txt"
				_ = memFS.WriteAll(t.Context(), path, []byte("x"), nil)
				_, _ = memFS.ReadAll(t.Context(), path)
				_, _ = memFS.Stat(path)
				_ = memFS.Touch(path, nil)
				_ = memFS.Remove(path)
			}
		}(w)
	}
	wg.Wait()
}

func TestMemFileSystem_FileMode_Permissions(t *testing.T) {
	// FileMode() composes the stored Permissions with the dir/symlink bits.
	memFS := newTestMemFS(t)

	require.NoError(t, memFS.WriteAll(t.Context(), "/f.txt", []byte("x"), []Permissions{UserRead}))
	info, err := memFS.Stat("/f.txt")
	require.NoError(t, err)
	require.Equal(t, iofs.FileMode(UserRead), info.Mode()&iofs.ModePerm)

	require.NoError(t, memFS.SetPermissions("/f.txt", AllRead))
	info, err = memFS.Stat("/f.txt")
	require.NoError(t, err)
	require.Equal(t, iofs.FileMode(AllRead), info.Mode()&iofs.ModePerm)
}
