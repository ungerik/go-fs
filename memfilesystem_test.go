package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	stdfstest "testing/fstest"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RegisteredTestMemFileSystem creates a MemFileSystem with NewMemFileSystem
// and registers it, like NewMemFileSystem did before v1.0.0-beta.2.
func RegisteredTestMemFileSystem(opts *MemFileSystemOptions, initialFiles ...MemFile) (*MemFileSystem, error) {
	memFS, err := NewMemFileSystem(opts, initialFiles...)
	if err != nil {
		return nil, err
	}
	return memFS, Register(memFS)
}

func TestNewMemFileSystem(t *testing.T) {
	for _, sep := range []string{`/`, `\`} {
		fs, err := NewMemFileSystem(&MemFileSystemOptions{Separator: sep})
		require.NoError(t, err)
		require.False(t, IsRegistered(fs), "NewMemFileSystem must not register")
		require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
		require.NoError(t, Register(fs))
		require.True(t, fs.RootDir().Exists(), "root dir exists")
		require.True(t, fs.RootDir().IsDir(), "root is dir")

		err = fs.Close()
		require.NoError(t, err)
		require.False(t, IsRegistered(fs), "Close unregisters")
		require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
		require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")

		volFS, err := RegisteredTestMemFileSystem(&MemFileSystemOptions{Separator: sep, Volume: "C:"})
		require.NoError(t, err)
		require.Equal(t, "C:", volFS.Volume())
		require.True(t, volFS.RootDir().IsDir(), "root of a volume file system is a dir")
		require.NoError(t, volFS.Close())
	}
}

func TestNewMemFileSystemAndRegister(t *testing.T) {
	require.Error(t, NewMemFileSystemAndRegister(nil), "an ID is required")
	err := NewMemFileSystemAndRegister(&MemFileSystemOptions{ID: "and-register-test"}, NewMemFile("a.txt", []byte("a")))
	require.NoError(t, err)
	memFS := GetFileSystemByPrefixOrNil("mem://and-register-test")
	require.NotNil(t, memFS, "registered under its ID")
	t.Cleanup(func() { _ = memFS.Close() })
	data, err := File("mem://and-register-test/a.txt").ReadAll(t.Context())
	require.NoError(t, err)
	require.Equal(t, "a", string(data))

	err = NewMemFileSystemAndRegister(&MemFileSystemOptions{ID: "and-register-test"})
	require.ErrorIs(t, err, ErrAlreadyRegistered, "another file system with the same ID")
}

func TestMemFileSystem_SingleFile(t *testing.T) {
	fs, err := RegisteredTestMemFileSystem(nil, NewMemFile("test.txt", []byte("Hello, World!")))
	require.NoError(t, err, "NewMemFileSystem")
	f := fs.RootDir().Join("test.txt")

	t.Cleanup(func() { _ = fs.Close() })

	// Check fs
	require.True(t, strings.HasPrefix(fs.Prefix(), "mem://"))
	require.True(t, fs.RootDir().Exists(), "root directory exists")
	require.True(t, fs.RootDir().IsDir(), "root is a directory")
	files, err := fs.RootDir().ListDirMax(t.Context(), -1)
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
	content, err := f.ReadAllString(t.Context())
	require.NoError(t, err, "ReadAllString")
	require.Equal(t, "Hello, World!", content)

	err = fs.Close()
	require.NoError(t, err, "Close")
	require.False(t, f.Exists(), "test.txt does not exist after close")
	require.False(t, fs.RootDir().Exists(), "root dir does not exist after close")
	require.False(t, fs.RootDir().IsDir(), "root dir does not exist after close")
}

func TestMemFileSystem_FullFeatures(t *testing.T) {
	t.Run("Rename", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("hello")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		src := memFS.RootDir().Join("a.txt")
		require.True(t, src.Exists())

		renamed, err := src.Rename(t.Context(), "b.txt")
		require.NoError(t, err)
		require.True(t, renamed.Exists(), "new path exists")
		require.False(t, src.Exists(), "old path gone")
		require.Equal(t, "b.txt", renamed.Name())
		content, err := renamed.ReadAllString(t.Context())
		require.NoError(t, err)
		require.Equal(t, "hello", content, "content preserved across rename")

		// Renaming a directory works the same way.
		require.NoError(t, memFS.MakeDir("/d1", 0))
		dir := memFS.RootDir().Join("d1")
		dir2, err := dir.Rename(t.Context(), "d2")
		require.NoError(t, err)
		require.True(t, dir2.IsDir())
		require.False(t, dir.Exists())

		// Rejects separator in newName.
		_, err = renamed.Rename(t.Context(), "with/slash.txt")
		require.Error(t, err)

		// A missing source is an error.
		_, err = memFS.RootDir().Join("c.txt").Rename(t.Context(), "b.txt")
		require.ErrorIs(t, err, os.ErrNotExist)

		// An existing file with the new name is replaced like by os.Rename.
		require.NoError(t, memFS.WriteAll(t.Context(), "/c.txt", []byte("c"), 0))
		_, err = memFS.RootDir().Join("c.txt").Rename(t.Context(), "b.txt")
		require.NoError(t, err, "an existing file with the new name is replaced")
		require.Equal(t, "c", mustReadString(t, renamed), "content of the replacing file")
		require.False(t, memExists(memFS, "/c.txt"))
	})

	t.Run("Move", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.MakeAllDirs("/sub/nested", 0))

		// The destination is always the final path, an existing directory
		// is a collision (File.MoveTo resolves "into directory" before dispatch).
		require.ErrorIs(t, memFS.Move(t.Context(), "/a.txt", "/sub"), os.ErrExist)
		require.NoError(t, memFS.Move(t.Context(), "/a.txt", "/sub/a.txt"))
		require.True(t, memFS.RootDir().Join("sub", "a.txt").Exists())
		require.False(t, memFS.RootDir().Join("a.txt").Exists())

		// Move to explicit new path.
		require.NoError(t, memFS.Move(t.Context(), "/sub/a.txt", "/sub/nested/renamed.txt"))
		require.True(t, memFS.RootDir().Join("sub", "nested", "renamed.txt").Exists())
		require.False(t, memFS.RootDir().Join("sub", "a.txt").Exists())

		// An existing destination file is replaced like by os.Rename.
		require.NoError(t, memFS.WriteAll(t.Context(), "/sub/blocker.txt", []byte("x"), 0))
		require.NoError(t, memFS.Move(t.Context(), "/sub/nested/renamed.txt", "/sub/blocker.txt"))
		require.Equal(t, "a", mustReadString(t, memFS.RootDir().Join("sub", "blocker.txt")))
		require.False(t, memExists(memFS, "/sub/nested/renamed.txt"))

		// Missing source.
		require.Error(t, memFS.Move(t.Context(), "/nope.txt", "/sub"))

		// Moving a directory into a descendant is rejected and leaves the
		// source intact (regression for an orphaning bug).
		require.NoError(t, memFS.MakeAllDirs("/cyc/inner", 0))
		require.Error(t, memFS.Move(t.Context(), "/cyc", "/cyc/inner/sub"), "into descendant")
		require.True(t, memFS.RootDir().Join("cyc").Exists(), "source survives rejected move")
		require.True(t, memFS.RootDir().Join("cyc", "inner").Exists(), "subtree survives rejected move")

		// Move(src, src) on a regular file is a no-op, matching
		// LocalFileSystem and the MoveFileSystem contract.
		require.NoError(t, memFS.WriteAll(t.Context(), "/same.txt", []byte("z"), 0))
		require.NoError(t, memFS.Move(t.Context(), "/same.txt", "/same.txt"), "Move(file, file) must be a no-op")
		got, err := memFS.RootDir().Join("same.txt").ReadAllString(t.Context())
		require.NoError(t, err)
		require.Equal(t, "z", got, "file content preserved")

		// Move(src, src) on a directory is also a no-op (the directory-
		// append rule does not apply when src and dest are already equal).
		require.NoError(t, memFS.Move(t.Context(), "/cyc", "/cyc"), "Move(dir, dir) must be a no-op")
		require.True(t, memFS.RootDir().Join("cyc", "inner").Exists(), "subtree preserved")

		// Move(/a, /) targets the existing root directory, which is a
		// collision under the final-destination contract (File.MoveTo
		// resolves "into directory" before calling Move).
		require.NoError(t, memFS.MakeDir("/movable", 0))
		require.ErrorIs(t, memFS.Move(t.Context(), "/movable", "/"), os.ErrExist, "Move(/a, /) collides with the root")
		require.True(t, memFS.RootDir().Join("movable").Exists(), "subject survives the rejected move")
	})

	t.Run("Permissions_User_Group", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("a")))
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
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("a")))
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
		require.Error(t, memFS.SetXAttr("/a.txt", "user.tag", []byte("new"), XAttrCreate, true))
		// XATTR_REPLACE rejects missing
		require.Error(t, memFS.SetXAttr("/a.txt", "user.absent", []byte("z"), XAttrReplace, true))

		// Remove and verify
		require.NoError(t, memFS.RemoveXAttr("/a.txt", "user.tag", true))
		_, err = memFS.GetXAttr("/a.txt", "user.tag", true)
		require.Error(t, err)
		require.Error(t, memFS.RemoveXAttr("/a.txt", "user.tag", true), "removing missing attr errors")
	})

	t.Run("ListDirMax", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil,
			NewMemFile("a.txt", []byte("a")),
			NewMemFile("b.txt", []byte("b")),
			NewMemFile("c.log", []byte("c")),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		all, err := memFS.JoinCleanFile("/").ListDirMax(t.Context(), -1)
		require.NoError(t, err)
		require.Len(t, all, 3)

		two, err := memFS.JoinCleanFile("/").ListDirMax(t.Context(), 2)
		require.NoError(t, err)
		require.Len(t, two, 2)

		zero, err := memFS.JoinCleanFile("/").ListDirMax(t.Context(), 0)
		require.NoError(t, err)
		require.Empty(t, zero)

		txt, err := memFS.JoinCleanFile("/").ListDirMax(t.Context(), -1, "*.txt")
		require.NoError(t, err)
		require.Len(t, txt, 2)
	})

	t.Run("ListDirInfoRecursive", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.MakeAllDirs("/a/b", 0))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/b/c.txt", []byte("c"), 0))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/b/d.txt", []byte("d"), 0))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/e.txt", []byte("e"), 0))
		require.NoError(t, memFS.WriteAll(t.Context(), "/a/skip.bin", []byte("x"), 0))

		var collected []string
		err = memFS.ListDirRecursive(t.Context(), "/a", []string{"*.txt"}, func(info *FileInfo) error {
			collected = append(collected, info.Name)
			return nil
		})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"c.txt", "d.txt", "e.txt"}, collected)

		// Context cancel
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err = memFS.ListDirRecursive(ctx, "/a", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("Symlinks", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("real.txt", []byte("R")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.NoError(t, memFS.CreateSymbolicLink("/real.txt", "/link"))
		require.True(t, isMemSymlink(memFS, "/link"))
		require.False(t, isMemSymlink(memFS, "/real.txt"))

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
		require.False(t, isMemSymlink(memFS, "/link"))
		require.True(t, memFS.RootDir().Join("real.txt").Exists())
	})

	t.Run("Watch", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("a")))
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		events := make(chan Event, 16)
		cancelDir, err := memFS.Watch("/", func(_ File, e Event) { events <- e })
		require.NoError(t, err)

		// Touch existing -> Chmod
		require.NoError(t, memFS.Touch("/a.txt", 0))

		// WriteAll on new file -> Create|Write
		require.NoError(t, memFS.WriteAll(t.Context(), "/b.txt", []byte("b"), 0))

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
		require.NoError(t, memFS.WriteAll(t.Context(), "/c.txt", []byte("c"), 0))
		select {
		case e := <-events:
			t.Fatalf("unexpected event after cancel: %s", e)
		case <-time.After(100 * time.Millisecond):
		}

		// Close should not crash on subsequent mutations
		require.NoError(t, memFS.Close())
	})
}

// isMemSymlink reports if filePath is a symbolic link in memFS.
func isMemSymlink(memFS *MemFileSystem, filePath string) bool {
	info, err := memFS.Lstat(filePath)
	return err == nil && info.IsSymlink
}

// newTestMemFS returns a registered MemFileSystem that is closed at the end
// of the test. Closing twice (here + manual Close inside the test) is a no-op.
func newTestMemFS(t *testing.T, initial ...MemFile) *MemFileSystem {
	t.Helper()
	memFS, err := RegisteredTestMemFileSystem(nil, initial...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	return memFS
}

func TestMemFileSystem_Constructor_EdgeCases(t *testing.T) {
	t.Run("InvalidSeparator", func(t *testing.T) {
		for _, sep := range []string{":", "//", "|"} {
			_, err := NewMemFileSystem(&MemFileSystemOptions{Separator: sep})
			require.Error(t, err, "separator %q must be rejected", sep)
		}
	})

	t.Run("EmptyInitialFileName", func(t *testing.T) {
		_, err := RegisteredTestMemFileSystem(nil, MemFile{FileName: ""})
		require.Error(t, err, "empty initial filename must be rejected")
	})

	t.Run("InitialFilesWithPath", func(t *testing.T) {
		memFS, err := RegisteredTestMemFileSystem(
			nil,
			NewMemFile("docs/readme.txt", []byte("R")),
			NewMemFile("docs/sub/deep.txt", []byte("D")),
			NewMemFile("top.txt", []byte("T")),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = memFS.Close() })

		require.True(t, memExists(memFS, "/docs"))
		require.True(t, memExists(memFS, "/docs/sub"))
		require.True(t, memExists(memFS, "/docs/readme.txt"))
		require.True(t, memExists(memFS, "/docs/sub/deep.txt"))
		require.True(t, memExists(memFS, "/top.txt"))

		data, err := memFS.ReadAll(t.Context(), "/docs/sub/deep.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("D"), data)
	})

	t.Run("Registered", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.True(t, IsRegistered(memFS), "fresh fs is registered")
	})
}

func TestMemFileSystem_IDVolume(t *testing.T) {
	memFS, err := NewMemFileSystem(&MemFileSystemOptions{ID: "test-id", Volume: "V:"})
	require.NoError(t, err)
	require.Equal(t, "test-id", memFS.ID())
	require.True(t, strings.HasPrefix(memFS.Prefix(), "mem://test-id"), "prefix contains the ID")
	require.Equal(t, "V:", memFS.Volume())

	_, err = NewMemFileSystem(&MemFileSystemOptions{ID: "a/b"})
	require.Error(t, err, "an ID with a separator must be rejected")
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
		require.True(t, memExists(memFS, "/a"))
		require.True(t, memExists(memFS, "/a/b"))
	})
}

func TestMemFileSystem_MakeDir_EdgeCases(t *testing.T) {
	t.Run("EmptyPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.MakeDir("", 0), ErrEmptyPath)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t)
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.MakeDir("/d", 0), ErrReadOnlyFileSystem)
	})

	t.Run("AlreadyExists", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.NoError(t, memFS.MakeDir("/d", 0))
		err := memFS.MakeDir("/d", 0)
		require.Error(t, err)
		var aexists ErrAlreadyExists
		require.ErrorAs(t, err, &aexists, "MakeDir on existing dir returns ErrAlreadyExists")
	})

	t.Run("ParentMissing", func(t *testing.T) {
		memFS := newTestMemFS(t)
		err := memFS.MakeDir("/no/such/parent/d", 0)
		require.Error(t, err)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("f.txt", []byte("x")))
		err := memFS.MakeDir("/f.txt/sub", 0)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

func TestMemFileSystem_MakeAllDirs_EdgeCases(t *testing.T) {
	t.Run("EmptyPath", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.MakeAllDirs("", 0), ErrEmptyPath)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS := newTestMemFS(t)
		memFS.SetReadOnly(true)
		require.ErrorIs(t, memFS.MakeAllDirs("/a/b/c", 0), ErrReadOnlyFileSystem)
	})

	t.Run("IdempotentOnExistingDirs", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.NoError(t, memFS.MakeAllDirs("/a/b/c", 0))
		// Second call must not error — already-existing components are
		// silently skipped, matching the os.MkdirAll contract.
		require.NoError(t, memFS.MakeAllDirs("/a/b/c", 0))
	})

	t.Run("PathComponentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a/f.txt", []byte("x")))
		err := memFS.MakeAllDirs("/a/f.txt/d", 0)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

func TestMemFileSystem_Stat_And_Exists(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", 0))
	require.NoError(t, memFS.CreateSymbolicLink("/a.txt", "/link"))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.Stat("")
		require.ErrorIs(t, err, ErrEmptyPath)
		require.False(t, memExists(memFS, ""))
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.Stat("/missing")
		require.Error(t, err)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.False(t, memExists(memFS, "/missing"))
	})

	t.Run("FileMode", func(t *testing.T) {
		info, err := memFS.Stat("/a.txt")
		require.NoError(t, err)
		require.False(t, info.IsDir)
		require.True(t, info.IsRegular)
		require.Equal(t, int64(3), info.Size)
		require.False(t, info.IsSymlink)
	})

	t.Run("DirMode", func(t *testing.T) {
		info, err := memFS.Stat("/d")
		require.NoError(t, err)
		require.True(t, info.IsDir)
		require.False(t, info.IsRegular)
	})

	t.Run("SymlinkFollowed", func(t *testing.T) {
		// Like os.Stat, Stat follows symbolic links and reports the target,
		// while IsSymbolicLink reports the link itself.
		info, err := memFS.Stat("/link")
		require.NoError(t, err)
		require.True(t, info.IsSymlink, "Stat must report that the path is a link")
		require.True(t, info.IsRegular, "Stat must follow the link to the regular target")
		require.Equal(t, int64(3), info.Size, "Stat must report the target size")
		require.True(t, isMemSymlink(memFS, "/link"))
		require.Nil(t, info.Sys)
	})
}

func TestMemFileSystem_ReadAll_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", 0))

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

	t.Run("OnDirectoryErrors", func(t *testing.T) {
		// Like os.ReadFile, reading a directory is an error
		_, err := memFS.ReadAll(t.Context(), "/d")
		require.ErrorAs(t, err, new(ErrIsDirectory))
	})
}

func TestMemFileSystem_WriteAll_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t)

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.WriteAll(t.Context(), "", nil, 0), ErrEmptyPath)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.WriteAll(ctx, "/x.txt", []byte("x"), 0), context.Canceled)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.WriteAll(t.Context(), "/no/such/x.txt", []byte("x"), 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("OverwriteExisting", func(t *testing.T) {
		require.NoError(t, memFS.WriteAll(t.Context(), "/over.txt", []byte("first"), 0))
		require.NoError(t, memFS.WriteAll(t.Context(), "/over.txt", []byte("second"), 0))
		data, err := memFS.ReadAll(t.Context(), "/over.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("second"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.WriteAll(t.Context(), "/ro.txt", []byte("x"), 0), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Append_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t)

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Append(t.Context(), "", []byte("x"), 0), ErrEmptyPath)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.Append(ctx, "/x.txt", []byte("x"), 0), context.Canceled)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.Append(t.Context(), "/no/dir/x.txt", []byte("x"), 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("CreateThenAppend", func(t *testing.T) {
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("a"), 0))
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("b"), 0))
		require.NoError(t, memFS.Append(t.Context(), "/log.txt", []byte("c"), 0))
		data, err := memFS.ReadAll(t.Context(), "/log.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("abc"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Append(t.Context(), "/log.txt", []byte("z"), 0), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Touch_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Touch("", 0), ErrEmptyPath)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		err := memFS.Touch("/no/dir/x.txt", 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("UpdatesModTime", func(t *testing.T) {
		info, err := memFS.Stat("/a.txt")
		require.NoError(t, err)
		original := info.Modified

		time.Sleep(2 * time.Millisecond)
		require.NoError(t, memFS.Touch("/a.txt", 0))

		info, err = memFS.Stat("/a.txt")
		require.NoError(t, err)
		require.True(t, info.Modified.After(original), "Touch must advance ModTime")
	})

	t.Run("CreatesIfMissing", func(t *testing.T) {
		require.False(t, memExists(memFS, "/new.txt"))
		require.NoError(t, memFS.Touch("/new.txt", 0))
		require.True(t, memExists(memFS, "/new.txt"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Touch("/another.txt", 0), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Truncate_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")))

	t.Run("EmptyPath", func(t *testing.T) {
		require.ErrorIs(t, memFS.Truncate(t.Context(), "", 0), ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		require.ErrorIs(t, memFS.Truncate(t.Context(), "/missing", 0), os.ErrNotExist)
	})

	t.Run("SameSizeNoOp", func(t *testing.T) {
		require.NoError(t, memFS.Truncate(t.Context(), "/a.txt", 5))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello"), data)
	})

	t.Run("ZeroPadsWhenExtending", func(t *testing.T) {
		require.NoError(t, memFS.Truncate(t.Context(), "/a.txt", 8))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello\x00\x00\x00"), data)
	})

	t.Run("ShrinkPreservesPrefix", func(t *testing.T) {
		require.NoError(t, memFS.Truncate(t.Context(), "/a.txt", 3))
		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hel"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.Truncate(t.Context(), "/a.txt", 1), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenReader_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abc")))
	require.NoError(t, memFS.MakeDir("/d", 0))

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
	require.NoError(t, memFS.MakeDir("/d", 0))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenWriter("", 0)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenWriter("/d", 0)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenWriter("/no/dir/x.txt", 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenWriter("/f.txt/x.txt", 0)
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("TruncatesExisting", func(t *testing.T) {
		w, err := memFS.OpenWriter("/a.txt", 0)
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
		_, err := memFS.OpenWriter("/new.txt", 0)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenAppendWriter_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")), NewMemFile("f.txt", []byte("f")))
	require.NoError(t, memFS.MakeDir("/d", 0))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("", 0)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/d", 0)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/no/dir/x.txt", 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenAppendWriter("/f.txt/x.txt", 0)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("AppendsToExisting", func(t *testing.T) {
		w, err := memFS.OpenAppendWriter("/a.txt", 0)
		require.NoError(t, err)
		_, err = w.Write([]byte(" world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		data, err := memFS.ReadAll(t.Context(), "/a.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello world"), data)
	})

	t.Run("CreatesNewFile", func(t *testing.T) {
		w, err := memFS.OpenAppendWriter("/new.txt", 0)
		require.NoError(t, err)
		_, err = w.Write([]byte("z"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		require.True(t, memExists(memFS, "/new.txt"))
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.OpenAppendWriter("/x.txt", 0)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_OpenReadWriter_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("abcdef")), NewMemFile("f.txt", []byte("f")))
	require.NoError(t, memFS.MakeDir("/d", 0))

	t.Run("EmptyPath", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("", 0)
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("OnDirectory", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/d", 0)
		var isDir ErrIsDirectory
		require.ErrorAs(t, err, &isDir)
	})

	t.Run("ParentMissing", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/no/dir/x.txt", 0)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		_, err := memFS.OpenReadWriter("/f.txt/x.txt", 0)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("SeekReadWriteAt", func(t *testing.T) {
		rw, err := memFS.OpenReadWriter("/a.txt", 0)
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
		rw, err := memFS.OpenReadWriter("/new-rw.txt", 0)
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
		_, err := memFS.OpenReadWriter("/a.txt", 0)
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Remove_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
	require.NoError(t, memFS.MakeAllDirs("/d/sub", 0))
	require.NoError(t, memFS.WriteAll(t.Context(), "/d/sub/inner.txt", []byte("x"), 0))

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

	t.Run("NonEmptyDirRefused", func(t *testing.T) {
		// Like os.Remove, a non-empty directory can't be removed;
		// File.RemoveRecursive is the way to drop a subtree.
		require.Error(t, memFS.Remove("/d"))
		require.True(t, memExists(memFS, "/d/sub/inner.txt"), "content must survive a refused Remove")
		require.NoError(t, memFS.RootDir().Join("d").RemoveRecursive(t.Context()))
		require.False(t, memExists(memFS, "/d"))
		require.False(t, memExists(memFS, "/d/sub/inner.txt"))
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
		_, err := File("").Rename(t.Context(), "x")
		require.ErrorIs(t, err, ErrEmptyPath)
		for _, newName := range []string{"", ".", ".."} {
			_, err = memFS.JoinCleanFile("/a.txt").Rename(t.Context(), newName)
			require.ErrorIs(t, err, os.ErrInvalid, "new name %q", newName)
		}
		require.True(t, memFS.JoinCleanFile("/a.txt").Exists(), "a failed Rename must keep the file")
	})

	t.Run("NewNameWithSeparator", func(t *testing.T) {
		_, err := memFS.JoinCleanFile("/a.txt").Rename(t.Context(), "sub/b.txt")
		require.Error(t, err)
	})

	t.Run("Root", func(t *testing.T) {
		_, err := memFS.RootDir().Rename(t.Context(), "new-root")
		require.Error(t, err, "renaming root must fail")
	})

	t.Run("Missing", func(t *testing.T) {
		_, err := memFS.JoinCleanFile("/missing").Rename(t.Context(), "x.txt")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		_, err := memFS.JoinCleanFile("/a.txt").Rename(t.Context(), "b.txt")
		require.ErrorIs(t, err, ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_Move_EdgeCases(t *testing.T) {
	t.Run("EmptyInputs", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		require.ErrorIs(t, memFS.Move(t.Context(), "", "/x"), ErrEmptyPath)
		require.ErrorIs(t, memFS.Move(t.Context(), "/a.txt", ""), ErrEmptyPath)
	})

	t.Run("Missing", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.ErrorIs(t, memFS.Move(t.Context(), "/missing", "/x"), os.ErrNotExist)
	})

	t.Run("Root", func(t *testing.T) {
		memFS := newTestMemFS(t)
		require.Error(t, memFS.Move(t.Context(), "/", "/x"), "moving root must fail")
	})

	t.Run("ParentMissing", func(t *testing.T) {
		memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("a")))
		err := memFS.Move(t.Context(), "/a.txt", "/no/dir/b.txt")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ParentIsFile", func(t *testing.T) {
		memFS := newTestMemFS(t,
			NewMemFile("a.txt", []byte("a")),
			NewMemFile("blocker.txt", []byte("b")),
		)
		err := memFS.Move(t.Context(), "/a.txt", "/blocker.txt/x")
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
		require.ErrorIs(t, memFS.Move(t.Context(), "/a.txt", "/b.txt"), ErrReadOnlyFileSystem)
	})
}

func TestMemFileSystem_CopyFile_EdgeCases(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello")))

	t.Run("EmptyInputs", func(t *testing.T) {
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "", "/x"), ErrEmptyPath)
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "/a.txt", ""), ErrEmptyPath)
	})

	t.Run("MissingSource", func(t *testing.T) {
		err := memFS.CopyFile(t.Context(), "/missing", "/x")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, memFS.CopyFile(ctx, "/a.txt", "/dst.txt"), context.Canceled)
	})

	t.Run("Success", func(t *testing.T) {
		require.NoError(t, memFS.CopyFile(t.Context(), "/a.txt", "/copy.txt"))
		data, err := memFS.ReadAll(t.Context(), "/copy.txt")
		require.NoError(t, err)
		require.Equal(t, []byte("hello"), data)
	})

	t.Run("ReadOnly", func(t *testing.T) {
		memFS.SetReadOnly(true)
		defer memFS.SetReadOnly(false)
		require.ErrorIs(t, memFS.CopyFile(t.Context(), "/a.txt", "/new.txt"), ErrReadOnlyFileSystem)
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
		require.False(t, isMemSymlink(memFS, ""))
		require.False(t, isMemSymlink(memFS, "/missing"))
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
		err := memFS.ListDir(t.Context(), "", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, ErrEmptyPath)
	})

	t.Run("ListDirInfo_Missing", func(t *testing.T) {
		err := memFS.ListDir(t.Context(), "/missing", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("ListDirInfo_OnFile", func(t *testing.T) {
		err := memFS.ListDir(t.Context(), "/a.txt", nil, func(*FileInfo) error { return nil })
		require.Error(t, err)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})

	t.Run("ListDirInfo_CanceledCtx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := memFS.ListDir(ctx, "/", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ListDirInfo_CallbackError", func(t *testing.T) {
		sentinel := errors.New("stop")
		err := memFS.ListDir(t.Context(), "/", nil, func(*FileInfo) error { return sentinel })
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("ListDirMax_Errors", func(t *testing.T) {
		_, err := File("").ListDirMax(t.Context(), -1)
		require.ErrorIs(t, err, ErrEmptyPath)

		_, err = memFS.JoinCleanFile("/missing").ListDirMax(t.Context(), -1)
		require.ErrorIs(t, err, os.ErrNotExist)

		_, err = memFS.JoinCleanFile("/a.txt").ListDirMax(t.Context(), -1)
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = memFS.JoinCleanFile("/").ListDirMax(ctx, -1)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("ListDirInfoRecursive_Errors", func(t *testing.T) {
		err := memFS.ListDirRecursive(t.Context(), "", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, ErrEmptyPath)

		err = memFS.ListDirRecursive(t.Context(), "/missing", nil, func(*FileInfo) error { return nil })
		require.ErrorIs(t, err, os.ErrNotExist)

		err = memFS.ListDirRecursive(t.Context(), "/a.txt", nil, func(*FileInfo) error { return nil })
		var notDir ErrIsNotDirectory
		require.ErrorAs(t, err, &notDir)
	})
}

// TestMemFileSystem_ListDir_PopulatesFileAndDoesNotDeadlock guards two
// regressions in the directory-listing methods:
//   - FileInfo.File was left empty, so File.ListDir / CopyRecursive yielded
//     invalid empty-path files instead of real ones.
//   - the read lock was held across the callback, so a callback that wrote
//     back into the same MemFileSystem (e.g. CopyRecursive copying within one
//     file system) deadlocked.
func TestMemFileSystem_ListDir_PopulatesFileAndDoesNotDeadlock(t *testing.T) {
	t.Run("ListDir populates FileInfo.File", func(t *testing.T) {
		memFS := newTestMemFS(t,
			NewMemFile("dir/a.txt", []byte("a")),
			NewMemFile("dir/sub/b.txt", []byte("b")),
		)

		var names []string
		err := memFS.RootDir().Join("dir").ListDir(t.Context(), func(f File) error {
			require.NotEqual(t, File(""), f, "listed file must not be the empty/invalid file")
			require.True(t, f.Exists(), "listed file %s must resolve to an existing file", f)
			names = append(names, f.Name())
			return nil
		})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a.txt", "sub"}, names)
	})

	t.Run("ListDirRecursive populates FileInfo.File", func(t *testing.T) {
		memFS := newTestMemFS(t,
			NewMemFile("dir/a.txt", []byte("a")),
			NewMemFile("dir/sub/b.txt", []byte("b")),
		)

		var paths []string
		err := memFS.RootDir().Join("dir").ListDirRecursive(t.Context(), func(f File) error {
			require.True(t, f.Exists(), "listed file %s must exist", f)
			paths = append(paths, f.Name())
			return nil
		})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a.txt", "b.txt"}, paths)
	})

	t.Run("CopyRecursive within the same file system does not deadlock", func(t *testing.T) {
		memFS := newTestMemFS(t,
			NewMemFile("src/a.txt", []byte("aaa")),
			NewMemFile("src/sub/b.txt", []byte("bbb")),
		)

		// Copying a directory into a sibling of the same MemFileSystem makes
		// the listing callback write back into this file system. A read lock
		// held across the callback would deadlock here; the 10s test timeout
		// would turn that into a failure.
		src := memFS.RootDir().Join("src")
		dst := memFS.RootDir().Join("dst")
		err := CopyRecursive(t.Context(), src, dst)
		require.NoError(t, err)

		require.Equal(t, "aaa", mustReadString(t, dst.Join("a.txt")))
		require.Equal(t, "bbb", mustReadString(t, dst.Join("sub", "b.txt")))
		// Source is untouched by a copy.
		require.True(t, src.Join("a.txt").Exists())
	})
}

func mustReadString(t *testing.T, file File) string {
	t.Helper()
	s, err := file.ReadAllString(t.Context())
	require.NoError(t, err, "ReadAllString %s", file)
	return s
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

		require.NoError(t, memFS.WriteAll(t.Context(), "/a.txt", []byte("Z"), 0))

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

		require.NoError(t, memFS.Touch("/a.txt", 0))

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

		require.NoError(t, memFS.WriteAll(t.Context(), "/x.txt", []byte("x"), 0))

		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		got := n
		mu.Unlock()
		require.Equal(t, 1, got, "root watch must fire exactly once per root-child event")
	})
}

func TestMemFileSystem_Close_Idempotent(t *testing.T) {
	memFS, err := RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)

	require.True(t, IsRegistered(memFS))
	require.NoError(t, memFS.Close())
	require.False(t, IsRegistered(memFS), "Close unregisters the fs")
	require.NoError(t, memFS.Close(), "second Close is a no-op")

	// Stat on a path after Close must not panic
	// and reports that the file system is closed.
	_, err = memFS.Stat("/anything")
	require.ErrorIs(t, err, ErrFileSystemClosed)
}

func TestMemFileSystem_Clear(t *testing.T) {
	memFS := newTestMemFS(t,
		NewMemFile("a.txt", []byte("a")),
		NewMemFile("sub/b.txt", []byte("b")),
	)
	require.True(t, memExists(memFS, "/a.txt"))
	require.True(t, memExists(memFS, "/sub/b.txt"))

	memFS.Clear()

	require.False(t, memExists(memFS, "/a.txt"), "Clear removes top-level files")
	require.False(t, memExists(memFS, "/sub"), "Clear removes top-level dirs")

	// Filesystem is still usable.
	require.NoError(t, memFS.WriteAll(t.Context(), "/after.txt", []byte("ok"), 0))
	require.True(t, memExists(memFS, "/after.txt"))
}

func TestMemFileSystem_Concurrent(t *testing.T) {
	// Hammer the filesystem from many goroutines to exercise the rwmutex
	// under -race. The test only asserts that operations don't error;
	// the race detector catches actual concurrency bugs.
	memFS := newTestMemFS(t)
	require.NoError(t, memFS.MakeDir("/shared", 0))

	const workers = 16
	const opsPerWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func(id int) {
			defer wg.Done()
			for i := range opsPerWorker {
				path := "/shared/" + string(rune('a'+(id%26))) + "-" + string(rune('a'+(i%26))) + ".txt"
				_ = memFS.WriteAll(t.Context(), path, []byte("x"), 0)
				_, _ = memFS.ReadAll(t.Context(), path)
				_, _ = memFS.Stat(path)
				_ = memFS.Touch(path, 0)
				_ = memFS.Remove(path)
			}
		}(w)
	}
	wg.Wait()
}

func TestMemFileSystem_FileMode_Permissions(t *testing.T) {
	// FileMode() composes the stored Permissions with the dir/symlink bits.
	memFS := newTestMemFS(t)

	require.NoError(t, memFS.WriteAll(t.Context(), "/f.txt", []byte("x"), UserRead))
	info, err := memFS.Stat("/f.txt")
	require.NoError(t, err)
	require.Equal(t, UserRead, info.Permissions)

	require.NoError(t, memFS.SetPermissions("/f.txt", AllRead))
	info, err = memFS.Stat("/f.txt")
	require.NoError(t, err)
	require.Equal(t, AllRead, info.Permissions)
}

// memExists returns the existence of a path ignoring errors
func memExists(fs *MemFileSystem, path string) bool {
	exists, _ := fs.Exists(path)
	return exists
}

// newUnregisteredMemFS returns a MemFileSystem created with opts
// that is not registered and closed at the end of the test.
func newUnregisteredMemFS(t *testing.T, opts *MemFileSystemOptions, initial ...MemFile) *MemFileSystem {
	t.Helper()
	memFS, err := NewMemFileSystem(opts, initial...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	return memFS
}

// callMemOp calls op and returns its error, or its panic as error.
// It fails the test if op does not return within 5 seconds,
// for example because an earlier operation leaked a lock.
func callMemOp(t *testing.T, name string, op func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("panic: %v", p)
			}
		}()
		done <- op()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return, a lock is probably held", name)
		return nil
	}
}

func TestMemFileSystem_AfterClose(t *testing.T) {
	ctx := t.Context()
	memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("a.txt", []byte("a")))
	require.NoError(t, err)
	require.NoError(t, memFS.MakeDir("/dir", 0))
	writer, err := memFS.OpenWriter("/open-writer.txt", 0)
	require.NoError(t, err)
	readWriter, err := memFS.OpenReadWriter("/open-read-writer.txt", 0)
	require.NoError(t, err)
	reader, err := memFS.OpenReader("/a.txt")
	require.NoError(t, err)
	require.NoError(t, memFS.Close())
	require.False(t, IsRegistered(memFS), "Close unregisters")

	nop := func(*FileInfo) error { return nil }
	ops := []struct {
		name string
		call func() error
	}{
		// MakeAllDirs is first: it panicked while holding the lock,
		// so that every following operation blocked
		{"MakeAllDirs", func() error { return memFS.MakeAllDirs("/x/y", 0) }},
		{"Stat", func() error { _, err := memFS.Stat("/a.txt"); return err }},
		{"Lstat", func() error { _, err := memFS.Lstat("/a.txt"); return err }},
		{"Exists", func() error { _, err := memFS.Exists("/a.txt"); return err }},
		{"ListDir", func() error { return memFS.ListDir(ctx, "/", nil, nop) }},
		{"ListDirRecursive", func() error { return memFS.ListDirRecursive(ctx, "/", nil, nop) }},
		{"ReadAll", func() error { _, err := memFS.ReadAll(ctx, "/a.txt"); return err }},
		{"OpenReader", func() error { _, err := memFS.OpenReader("/a.txt"); return err }},
		{"Watch", func() error { _, err := memFS.Watch("/a.txt", func(File, Event) {}); return err }},
		{"User", func() error { _, err := memFS.User("/a.txt"); return err }},
		{"Group", func() error { _, err := memFS.Group("/a.txt"); return err }},
		{"ReadSymbolicLink", func() error { _, err := memFS.ReadSymbolicLink("/a.txt"); return err }},
		{"ListXAttr", func() error { _, err := memFS.ListXAttr("/a.txt", true); return err }},
		{"GetXAttr", func() error { _, err := memFS.GetXAttr("/a.txt", "user.x", true); return err }},
		{"OpenWriter", func() error { _, err := memFS.OpenWriter("/b.txt", 0); return err }},
		{"OpenAppendWriter", func() error { _, err := memFS.OpenAppendWriter("/b.txt", 0); return err }},
		{"OpenReadWriter", func() error { _, err := memFS.OpenReadWriter("/b.txt", 0); return err }},
		{"MakeDir", func() error { return memFS.MakeDir("/new-dir", 0) }},
		{"Remove", func() error { return memFS.Remove("/a.txt") }},
		{"RemoveAll", func() error { return memFS.RemoveAll(ctx, "/a.txt") }},
		{"WriteAll", func() error { return memFS.WriteAll(ctx, "/b.txt", []byte("b"), 0) }},
		{"Append", func() error { return memFS.Append(ctx, "/b.txt", []byte("b"), 0) }},
		{"Truncate", func() error { return memFS.Truncate(ctx, "/a.txt", 0) }},
		{"Touch", func() error { return memFS.Touch("/b.txt", 0) }},
		{"CopyFile", func() error { return memFS.CopyFile(ctx, "/a.txt", "/b.txt") }},
		{"Move", func() error { return memFS.Move(ctx, "/a.txt", "/b.txt") }},
		{"SetPermissions", func() error { return memFS.SetPermissions("/a.txt", AllRead) }},
		{"SetUser", func() error { return memFS.SetUser("/a.txt", "nobody") }},
		{"SetGroup", func() error { return memFS.SetGroup("/a.txt", "nobody") }},
		{"CreateSymbolicLink", func() error { return memFS.CreateSymbolicLink("/a.txt", "/link") }},
		{"SetXAttr", func() error { return memFS.SetXAttr("/a.txt", "user.x", []byte("x"), 0, true) }},
		{"RemoveXAttr", func() error { return memFS.RemoveXAttr("/a.txt", "user.x", true) }},
		{"AddMemFile", func() error { _, err := memFS.AddMemFile(NewMemFile("c.txt", nil), time.Now()); return err }},
		{"Writer.Write", func() error { _, err := writer.Write([]byte("x")); return err }},
		{"ReadWriter.Close after Write", func() error {
			_, err := readWriter.Write([]byte("x"))
			if err != nil {
				return fmt.Errorf("the read writer works on its own copy: %w", err)
			}
			return readWriter.Close()
		}},
	}
	for _, op := range ops {
		err := callMemOp(t, op.name, op.call)
		require.ErrorIs(t, err, ErrFileSystemClosed, "%s after Close", op.name)
	}

	// Readers opened before Close keep their snapshot
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "a", string(data))
	require.NoError(t, memFS.Close(), "Close is idempotent")
	memFS.Clear() // A no-op after Close
}

func TestMemFileSystem_ReadAllAndWriteAllCopy(t *testing.T) {
	memFS := newTestMemFS(t)

	data := []byte("hello")
	require.NoError(t, memFS.WriteAll(t.Context(), "/a.txt", data, 0))
	data[0] = 'X'
	got, err := memFS.ReadAll(t.Context(), "/a.txt")
	require.NoError(t, err)
	require.Equal(t, "hello", string(got), "WriteAll must store a copy of the data")

	got[0] = 'Y'
	again, err := memFS.ReadAll(t.Context(), "/a.txt")
	require.NoError(t, err)
	require.Equal(t, "hello", string(again), "ReadAll must return a copy of the content")

	initial := []byte("initial")
	added := newTestMemFS(t, NewMemFile("init.txt", initial))
	_, err = added.AddMemFile(NewMemFile("added.txt", initial), time.Now())
	require.NoError(t, err)
	initial[0] = 'X'
	require.Equal(t, "initial", mustReadString(t, added.RootDir().Join("init.txt")), "NewMemFileSystem must copy the initial files")
	require.Equal(t, "initial", mustReadString(t, added.RootDir().Join("added.txt")), "AddMemFile must copy the file")

	value := []byte("value")
	require.NoError(t, memFS.SetXAttr("/a.txt", "user.x", value, 0, true))
	value[0] = 'X'
	gotValue, err := memFS.GetXAttr("/a.txt", "user.x", true)
	require.NoError(t, err)
	require.Equal(t, "value", string(gotValue), "SetXAttr must store a copy")
	gotValue[0] = 'Y'
	gotValue, err = memFS.GetXAttr("/a.txt", "user.x", true)
	require.NoError(t, err)
	require.Equal(t, "value", string(gotValue), "GetXAttr must return a copy")
}

func TestMemFileSystem_CopyFileDoesNotShareContent(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello world")))
	a, b := memFS.RootDir().Join("a.txt"), memFS.RootDir().Join("b.txt")
	require.NoError(t, CopyFile(t.Context(), a, b))

	rw, err := a.OpenReadWriter()
	require.NoError(t, err)
	_, err = rw.WriteAt([]byte("JELLO"), 0)
	require.NoError(t, err)
	require.NoError(t, rw.Close())
	require.NoError(t, a.Append(t.Context(), []byte("!")))

	require.Equal(t, "JELLO world!", mustReadString(t, a))
	require.Equal(t, "hello world", mustReadString(t, b), "modifying the source must not change the copy")
}

func TestMemFileSystem_ReaderSnapshot(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte("hello world")))
	f := memFS.RootDir().Join("a.txt")
	ctx := t.Context()

	beforeTruncate, err := f.OpenReader()
	require.NoError(t, err)
	defer beforeTruncate.Close()
	require.NoError(t, f.Truncate(ctx, 5))
	afterTruncate, err := f.OpenReader()
	require.NoError(t, err)
	defer afterTruncate.Close()
	require.NoError(t, f.AppendString(ctx, " XXXXX"))

	beforeReadWriter, err := f.OpenReader()
	require.NoError(t, err)
	defer beforeReadWriter.Close()
	rw, err := f.OpenReadWriter()
	require.NoError(t, err)
	_, err = rw.WriteAt([]byte("JELLO"), 0)
	require.NoError(t, err)
	require.Equal(t, "hello XXXXX", mustReadString(t, f), "the writes of a read writer are stored on Close")
	require.NoError(t, rw.Close())

	beforeWriter, err := f.OpenReader()
	require.NoError(t, err)
	defer beforeWriter.Close()
	w, err := f.OpenWriter()
	require.NoError(t, err)
	_, err = w.Write([]byte("new"))
	require.NoError(t, err)
	require.Equal(t, "new", mustReadString(t, f), "the writes of a writer are visible to later readers")
	require.NoError(t, w.Close())

	for _, check := range []struct {
		reader io.Reader
		want   string
	}{
		{beforeTruncate, "hello world"},
		{afterTruncate, "hello"},
		{beforeReadWriter, "hello XXXXX"},
		{beforeWriter, "JELLO XXXXX"},
	} {
		data, err := io.ReadAll(check.reader)
		require.NoError(t, err)
		require.Equal(t, check.want, string(data), "a reader must read the content at the time it was opened")
	}

	info, err := beforeTruncate.Stat()
	require.NoError(t, err)
	require.Equal(t, int64(len("hello world")), info.Size(), "the FileInfo of a reader is a snapshot")
	require.Equal(t, "a.txt", info.Name())
}

func TestMemFileSystem_ConcurrentReadersAndWriters(t *testing.T) {
	// Run with -race: readers read their snapshot while
	// the file is changed by writers of all kinds.
	const content = "hello world hello world hello world"
	memFS := newTestMemFS(t, NewMemFile("a.txt", []byte(content)))
	ctx := t.Context()
	var wg sync.WaitGroup
	for range 4 {
		r, err := memFS.OpenReader("/a.txt")
		require.NoError(t, err)
		wg.Go(func() {
			defer r.Close()
			data, err := io.ReadAll(r)
			assert.NoError(t, err)
			assert.Equal(t, content, string(data))
		})
	}
	rw, err := memFS.OpenReadWriter("/a.txt", 0)
	require.NoError(t, err)
	w, err := memFS.OpenAppendWriter("/a.txt", 0)
	require.NoError(t, err)
	wg.Go(func() {
		_, err := rw.WriteAt([]byte("JELLO"), 0)
		assert.NoError(t, err)
		assert.NoError(t, rw.Close())
	})
	wg.Go(func() {
		_, err := w.Write([]byte(" appended"))
		assert.NoError(t, err)
		assert.NoError(t, w.Close())
	})
	wg.Go(func() { assert.NoError(t, memFS.Truncate(ctx, "/a.txt", 3)) })
	wg.Go(func() { assert.NoError(t, memFS.Append(ctx, "/a.txt", []byte("more"), 0)) })
	wg.Wait()
}

func TestMemFileSystem_StdFS(t *testing.T) {
	// testing/fstest.TestFS requires among others
	// that ReadFile returns a slice the caller may modify
	memFS := newTestMemFS(t,
		NewMemFile("a.txt", []byte("a")),
		NewMemFile("dir/b.txt", []byte("b")),
		NewMemFile("dir/sub/c.txt", []byte("c")),
	)
	require.NoError(t, stdfstest.TestFS(memFS.RootDir().StdFS(), "a.txt", "dir/b.txt", "dir/sub/c.txt"))
}

func TestMemFileSystem_WriteToDirectory(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("dir/file.txt", []byte("keep")), NewMemFile("src.txt", []byte("src")))
	ctx := t.Context()
	isDir := func(name string, err error) {
		t.Helper()
		require.ErrorIs(t, err, ErrIsDirectory{}, "%s on a directory", name)
		require.True(t, memFS.RootDir().Join("dir").IsDir(), "%s must keep the directory", name)
		require.Equal(t, "keep", mustReadString(t, memFS.RootDir().Join("dir", "file.txt")), "%s must keep the directory content", name)
	}
	isDir("WriteAll", memFS.WriteAll(ctx, "/dir", []byte("data"), 0))
	isDir("Append", memFS.Append(ctx, "/dir", []byte("data"), 0))
	isDir("Truncate", memFS.Truncate(ctx, "/dir", 10))
	isDir("CopyFile", memFS.CopyFile(ctx, "/src.txt", "/dir"))
	_, err := memFS.OpenWriter("/dir", 0)
	isDir("OpenWriter", err)
	_, err = memFS.OpenAppendWriter("/dir", 0)
	isDir("OpenAppendWriter", err)
	_, err = memFS.OpenReadWriter("/dir", 0)
	isDir("OpenReadWriter", err)
	isDir("File.WriteAll", memFS.RootDir().Join("dir").WriteAllString(ctx, "data"))

	// Also through a symbolic link to the directory
	require.NoError(t, memFS.CreateSymbolicLink("/dir", "/dir-link"))
	isDir("WriteAll through a link", memFS.WriteAll(ctx, "/dir-link", []byte("data"), 0))
	require.True(t, isMemSymlink(memFS, "/dir-link"), "the link must stay a link")
}

func TestMemFileSystem_WriteThroughSymlink(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("target.txt", []byte("orig")))
	ctx := t.Context()
	require.NoError(t, memFS.CreateSymbolicLink("/target.txt", "/link"))
	target, link := memFS.RootDir().Join("target.txt"), memFS.RootDir().Join("link")
	checkLink := func(what, want string) {
		t.Helper()
		require.Equal(t, want, mustReadString(t, target), "%s must write the target", what)
		require.Equal(t, want, mustReadString(t, link), "reading through the link after %s", what)
		linkTarget, err := memFS.ReadSymbolicLink("/link")
		require.NoError(t, err, "%s must keep the link", what)
		require.Equal(t, "/target.txt", linkTarget)
	}

	require.NoError(t, link.WriteAllString(ctx, "new"))
	checkLink("WriteAll", "new")
	require.NoError(t, link.AppendString(ctx, "+append"))
	checkLink("Append", "new+append")
	require.NoError(t, link.Truncate(ctx, 3))
	checkLink("Truncate", "new")
	w, err := link.OpenWriter()
	require.NoError(t, err)
	_, err = w.Write([]byte("writer"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	checkLink("OpenWriter", "writer")
	rw, err := link.OpenReadWriter()
	require.NoError(t, err)
	_, err = rw.WriteAt([]byte("W"), 0)
	require.NoError(t, err)
	require.NoError(t, rw.Close())
	checkLink("OpenReadWriter", "Writer")
	require.NoError(t, memFS.SetPermissions("/link", UserRead))
	info, err := memFS.Stat("/target.txt")
	require.NoError(t, err)
	require.Equal(t, UserRead, info.Permissions, "SetPermissions follows the link")

	// Writing through a dangling link creates its target
	require.NoError(t, memFS.CreateSymbolicLink("created.txt", "/dangling"))
	require.NoError(t, memFS.WriteAll(ctx, "/dangling", []byte("created"), 0))
	require.Equal(t, "created", mustReadString(t, memFS.RootDir().Join("created.txt")))
	require.True(t, isMemSymlink(memFS, "/dangling"))
}

func TestMemFileSystem_SymlinkResolution(t *testing.T) {
	memFS := newTestMemFS(t, NewMemFile("d/f.txt", []byte("x")), NewMemFile("d/sub/g.txt", []byte("yy")))
	ctx := t.Context()
	require.NoError(t, memFS.CreateSymbolicLink("/d", "/dlink"))
	require.NoError(t, memFS.CreateSymbolicLink("d/f.txt", "/flink"))

	t.Run("DanglingExistsAgreesWithStat", func(t *testing.T) {
		require.NoError(t, memFS.CreateSymbolicLink("/missing", "/dangling"))
		exists, err := memFS.Exists("/dangling")
		require.NoError(t, err)
		require.False(t, exists, "a dangling link does not exist, like for Stat")
		_, err = memFS.Stat("/dangling")
		require.ErrorIs(t, err, os.ErrNotExist)
		info, err := memFS.Lstat("/dangling")
		require.NoError(t, err, "Lstat describes the link itself")
		require.True(t, info.IsSymlink)
		require.False(t, info.IsDir)
		require.False(t, info.IsRegular)
		require.NoError(t, memFS.Remove("/dangling"))
	})

	t.Run("ListDirThroughLink", func(t *testing.T) {
		var names []string
		err := memFS.RootDir().Join("dlink").ListDir(ctx, func(f File) error {
			names = append(names, f.Name())
			require.True(t, f.Exists(), "%s must exist", f)
			return nil
		})
		require.NoError(t, err, "a link to a directory must be listable")
		require.Equal(t, []string{"f.txt", "sub"}, names)
	})

	t.Run("StatThroughLink", func(t *testing.T) {
		info, err := memFS.Stat("/dlink/f.txt")
		require.NoError(t, err)
		require.Equal(t, int64(1), info.Size)
		require.Equal(t, memFS.RootDir().Join("dlink", "f.txt"), info.File)

		info, err = memFS.Stat("/flink")
		require.NoError(t, err)
		require.Equal(t, "flink", info.Name, "Stat of a link has the name of the link")
		require.True(t, info.IsSymlink)
		require.True(t, info.IsRegular)
	})

	t.Run("ListDirDescribesLinks", func(t *testing.T) {
		listed := map[string]*FileInfo{}
		require.NoError(t, memFS.ListDir(ctx, "/", nil, func(info *FileInfo) error {
			listed[info.Name] = info
			return nil
		}))
		require.True(t, listed["dlink"].IsSymlink)
		require.True(t, listed["dlink"].IsDir, "IsDir describes the target of a link")
		require.True(t, listed["flink"].IsSymlink)
		require.True(t, listed["flink"].IsRegular, "IsRegular describes the target of a link")
		require.False(t, listed["d"].IsSymlink)
	})

	t.Run("WalkThroughLink", func(t *testing.T) {
		var paths []string
		err := memFS.RootDir().Join("dlink").ListDirRecursive(ctx, func(f File) error {
			paths = append(paths, f.Path())
			return nil
		})
		require.NoError(t, err, "a link to a directory must be walkable")
		require.Equal(t, []string{"/dlink/f.txt", "/dlink/sub/g.txt"}, paths)

		// A link to a directory inside the walked tree is followed,
		// a link back to a directory of the walked path is not
		require.NoError(t, memFS.CreateSymbolicLink("..", "/d/sub/up"))
		paths = nil
		err = memFS.ListDirRecursive(ctx, "/", nil, func(info *FileInfo) error {
			paths = append(paths, info.File.Path())
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, []string{"/d/f.txt", "/d/sub/g.txt", "/dlink/f.txt", "/dlink/sub/g.txt", "/flink"}, paths)
		require.NoError(t, memFS.Remove("/d/sub/up"))
	})

	t.Run("LinkLoop", func(t *testing.T) {
		require.NoError(t, memFS.CreateSymbolicLink("/loop2", "/loop1"))
		require.NoError(t, memFS.CreateSymbolicLink("/loop1", "/loop2"))
		_, err := memFS.Stat("/loop1")
		require.Error(t, err, "a link loop must not hang")
		_, err = memFS.ReadAll(ctx, "/loop1/x")
		require.Error(t, err)
		require.NoError(t, memFS.Remove("/loop1"), "Remove doesn't follow the link")
		require.NoError(t, memFS.Remove("/loop2"))
	})

	t.Run("XAttrFollowSymlinks", func(t *testing.T) {
		require.NoError(t, memFS.SetXAttr("/flink", "user.target", []byte("t"), 0, true))
		require.NoError(t, memFS.SetXAttr("/flink", "user.link", []byte("l"), 0, false))
		names, err := memFS.ListXAttr("/d/f.txt", true)
		require.NoError(t, err)
		require.Equal(t, []string{"user.target"}, names, "followSymlinks sets the attribute of the target")
		names, err = memFS.ListXAttr("/flink", false)
		require.NoError(t, err)
		require.Equal(t, []string{"user.link"}, names, "without followSymlinks the link has its own attributes")
	})

	t.Run("RemoveAndMoveDontFollow", func(t *testing.T) {
		require.NoError(t, memFS.Move(ctx, "/flink", "/flink2"))
		require.True(t, isMemSymlink(memFS, "/flink2"), "Move moves the link")
		require.True(t, memExists(memFS, "/d/f.txt"), "Move must not move the target")
		require.NoError(t, memFS.Remove("/flink2"))
		require.True(t, memExists(memFS, "/d/f.txt"), "Remove must not remove the target")
	})
}

func TestMemFileSystem_MoveAndCopySemantics(t *testing.T) {
	ctx := t.Context()
	setup := func(t *testing.T) *MemFileSystem {
		t.Helper()
		return newTestMemFS(t,
			NewMemFile("src.txt", []byte("source")),
			NewMemFile("dest.txt", []byte("destination")),
			NewMemFile("srcdir/child.txt", []byte("child")),
			NewMemFile("destdir/keep.txt", []byte("keep")),
		)
	}
	destIntact := func(t *testing.T, memFS *MemFileSystem) {
		t.Helper()
		require.Equal(t, "destination", mustReadString(t, memFS.RootDir().Join("dest.txt")))
		require.Equal(t, "keep", mustReadString(t, memFS.RootDir().Join("destdir", "keep.txt")))
		require.False(t, memExists(memFS, "/destdir/child.txt"), "never merged")
		require.Equal(t, "child", mustReadString(t, memFS.RootDir().Join("srcdir", "child.txt")))
	}

	t.Run("FileOntoFileIsReplaced", func(t *testing.T) {
		memFS := setup(t)
		require.NoError(t, memFS.Move(ctx, "/src.txt", "/dest.txt"))
		require.Equal(t, "source", mustReadString(t, memFS.RootDir().Join("dest.txt")))
		require.False(t, memExists(memFS, "/src.txt"))
	})

	t.Run("OntoDirectory", func(t *testing.T) {
		memFS := setup(t)
		require.NoError(t, memFS.MakeDir("/emptydir", 0))
		for _, m := range [][2]string{{"/src.txt", "/destdir"}, {"/srcdir", "/destdir"}, {"/src.txt", "/emptydir"}, {"/srcdir", "/emptydir"}} {
			err := memFS.Move(ctx, m[0], m[1])
			require.ErrorIs(t, err, os.ErrExist, "Move(%s, %s)", m[0], m[1])
			require.ErrorIs(t, err, ErrAlreadyExists{}, "Move(%s, %s)", m[0], m[1])
		}
		destIntact(t, memFS)
		require.True(t, memFS.RootDir().Join("emptydir").IsEmptyDir())
	})

	t.Run("DirectoryOntoFile", func(t *testing.T) {
		memFS := setup(t)
		require.ErrorIs(t, memFS.Move(ctx, "/srcdir", "/dest.txt"), ErrIsNotDirectory{})
		destIntact(t, memFS)
	})

	t.Run("IntoItself", func(t *testing.T) {
		memFS := setup(t)
		require.NoError(t, memFS.MakeDir("/srcdir/sub", 0))
		require.ErrorIs(t, memFS.Move(ctx, "/srcdir", "/srcdir/new"), os.ErrInvalid)
		require.ErrorIs(t, memFS.Move(ctx, "/srcdir", "/srcdir/sub/new"), os.ErrInvalid)
		require.ErrorIs(t, memFS.Move(ctx, "/", "/x"), os.ErrInvalid, "the root")
		// A link into the directory must not hide the cycle
		require.NoError(t, memFS.CreateSymbolicLink("/srcdir/sub", "/sublink"))
		require.ErrorIs(t, memFS.Move(ctx, "/srcdir", "/sublink/new"), os.ErrInvalid, "through a link")
		require.True(t, memExists(memFS, "/srcdir/sub"))
		require.Equal(t, "child", mustReadString(t, memFS.RootDir().Join("srcdir", "child.txt")))
		require.False(t, memExists(memFS, "/srcdir/sub/new"))
	})

	t.Run("SameFileThroughLink", func(t *testing.T) {
		memFS := setup(t)
		require.NoError(t, memFS.CreateSymbolicLink("/srcdir", "/srclink"))
		require.NoError(t, memFS.Move(ctx, "/srclink/child.txt", "/srcdir/child.txt"), "a no-op")
		require.NoError(t, memFS.CopyFile(ctx, "/srclink/child.txt", "/srcdir/child.txt"), "a no-op")
		require.Equal(t, "child", mustReadString(t, memFS.RootDir().Join("srcdir", "child.txt")))
	})

	t.Run("RemoveNonEmptyDirectory", func(t *testing.T) {
		memFS := setup(t)
		err := memFS.Remove("/destdir")
		require.ErrorIs(t, err, ErrDirectoryNotEmpty{})
		require.ErrorIs(t, memFS.RootDir().Join("destdir").Remove(), ErrDirectoryNotEmpty{})
		destIntact(t, memFS)
		require.ErrorIs(t, memFS.Remove("/"), os.ErrInvalid, "the root")
	})

	t.Run("CopyFile", func(t *testing.T) {
		memFS := setup(t)
		require.NoError(t, memFS.SetPermissions("/src.txt", UserRead))
		require.NoError(t, memFS.SetPermissions("/dest.txt", AllReadWrite))
		require.ErrorIs(t, memFS.CopyFile(ctx, "/src.txt", "/destdir"), ErrIsDirectory{})
		require.ErrorIs(t, memFS.CopyFile(ctx, "/srcdir", "/new.txt"), ErrIsDirectory{})
		destIntact(t, memFS)

		require.NoError(t, memFS.CopyFile(ctx, "/src.txt", "/new.txt"))
		info, err := memFS.Stat("/new.txt")
		require.NoError(t, err)
		require.Equal(t, UserRead, info.Permissions, "a new copy gets the permissions of the source")
		require.NoError(t, memFS.CopyFile(ctx, "/src.txt", "/dest.txt"))
		info, err = memFS.Stat("/dest.txt")
		require.NoError(t, err)
		require.Equal(t, AllReadWrite, info.Permissions, "a replaced file keeps its permissions")
		require.Equal(t, "source", mustReadString(t, memFS.RootDir().Join("dest.txt")))
	})
}

func TestMemFileSystem_VolumePaths(t *testing.T) {
	for _, sep := range []string{`/`, `\`} {
		t.Run("separator "+sep, func(t *testing.T) {
			memFS, err := RegisteredTestMemFileSystem(&MemFileSystemOptions{Separator: sep, Volume: "C:"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = memFS.Close() })
			ctx := t.Context()
			path := func(elems ...string) string { return sep + strings.Join(elems, sep) }

			require.Equal(t, "", memFS.VolumeName(path("dir")), "a path without the volume has none")
			require.Equal(t, "", memFS.VolumeName(path("ab", "c")))
			require.Equal(t, "C:", memFS.VolumeName("C:"+path("dir")))

			// Like on Windows the clean paths start with the volume,
			// and a slash separates it from the ID in the URIs
			prefix := "mem://" + memFS.ID() + "/"
			require.Equal(t, prefix, memFS.Prefix())
			require.Equal(t, "C:"+path("dir", "a.txt"), memFS.CleanPath(path("dir", "a.txt")))
			require.Equal(t, "C:"+path("dir", "a.txt"), memFS.CleanPath("C:"+path("dir", "a.txt")))
			require.Equal(t, "C:"+path("dir", "a.txt"), memFS.CleanPath(prefix+"C:"+path("dir", "a.txt")))
			require.Equal(t, File(prefix+"C:"+sep), memFS.RootDir())
			require.Equal(t, "C:"+sep, memFS.RootDir().Path())
			require.Equal(t, memFS.RootDir(), memFS.RootDir().Join("dir").Dir())

			// Every way to the same path gives the same URI, which round trips
			wantURI := prefix + "C:" + path("dir", "a.txt")
			for _, file := range []File{
				memFS.RootDir().Join("dir", "a.txt"),
				memFS.RootDir().Join("dir").Join("a.txt"),
				memFS.JoinCleanFile(path("dir", "a.txt")),
				memFS.JoinCleanFile("C:" + path("dir", "a.txt")),
				memFS.RootDir().Join("dir", "a.txt", "child").Dir(),
				File(wantURI).Dir().Join("a.txt"),
			} {
				require.Equal(t, wantURI, string(file))
				parsedFS, parsedPath := ParseRawURI(string(file))
				require.Same(t, memFS, parsedFS)
				require.Equal(t, "C:"+path("dir", "a.txt"), parsedPath)
				require.Equal(t, "C:", file.VolumeName())
			}

			// The FileInfo.File of Stat and ListDir is the same
			// for paths with and without the volume
			require.NoError(t, memFS.MakeDir(path("dir"), 0))
			require.NoError(t, memFS.WriteAll(ctx, path("dir", "a.txt"), []byte("a"), 0))
			for _, p := range []string{path("dir", "a.txt"), "C:" + path("dir", "a.txt")} {
				info, err := memFS.Stat(p)
				require.NoError(t, err)
				require.Equal(t, wantURI, string(info.File), "Stat(%q).File", p)
				require.Equal(t, "a.txt", info.Name, "Stat(%q).Name", p)
			}
			for _, dir := range []string{path("dir"), "C:" + path("dir")} {
				require.NoError(t, memFS.ListDir(ctx, dir, nil, func(info *FileInfo) error {
					require.Equal(t, wantURI, string(info.File), "ListDir(%q) FileInfo.File", dir)
					return nil
				}))
			}

			// Paths with and without the volume are the same files
			require.NoError(t, memFS.MakeDir(path("a"), 0))
			require.NoError(t, memFS.WriteAll(ctx, "C:"+path("a", "f.txt"), []byte("f"), 0))
			require.Equal(t, "f", mustReadString(t, memFS.RootDir().Join("a", "f.txt")))
			require.ErrorIs(t, memFS.Move(ctx, "C:"+path("a"), path("a", "b")), os.ErrInvalid, "a directory moved into itself")
			require.ErrorIs(t, memFS.Move(ctx, path("a"), "C:"+path("a", "b")), os.ErrInvalid, "a directory moved into itself")
			require.True(t, memExists(memFS, path("a", "f.txt")), "the subtree must survive")
			require.NoError(t, memFS.Move(ctx, "C:"+path("a"), path("a")), "the same directory")

			// The package splits paths with VolumeName
			rel, err := memFS.RootDir().Join("a").RelPathOf(memFS.RootDir().Join("b"))
			require.NoError(t, err)
			require.Equal(t, ".."+sep+"b", rel)
			moved := memFS.RootDir().Join("moved")
			require.ErrorIs(t, memFS.RootDir().Join("a").MoveTo(ctx, memFS.RootDir().Join("a", "x")), os.ErrInvalid)
			require.NoError(t, memFS.RootDir().Join("a").MoveTo(ctx, moved))
			require.Equal(t, "f", mustReadString(t, moved.Join("f.txt")))
		})
	}

	_, err := NewMemFileSystem(&MemFileSystemOptions{Volume: `C:\`})
	require.Error(t, err, "a volume with a separator must be rejected")
}

func TestMemFileSystem_MakeDirPermissions(t *testing.T) {
	memFS := newTestMemFS(t)
	require.NoError(t, memFS.MakeDir("/d", UserReadWrite))
	info, err := memFS.Stat("/d")
	require.NoError(t, err)
	require.Equal(t, UserReadWrite, info.Permissions, "MakeDir must use perm")
	require.NoError(t, memFS.MakeDir("/default", 0))
	info, err = memFS.Stat("/default")
	require.NoError(t, err)
	require.Equal(t, memFileSystemDefaultPermissions, info.Permissions, "zero perm means the default permissions")
}

func TestMemFileSystem_ErrorFilesOfUnregisteredFileSystem(t *testing.T) {
	// The Files of errors are built from the prefix of the file system,
	// not via the registry, so they are meaningful without registration
	memFS := newUnregisteredMemFS(t, &MemFileSystemOptions{ID: "unregistered-errors"})
	_, err := memFS.Stat("/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
	var notExist ErrDoesNotExist
	require.ErrorAs(t, err, &notExist)
	file, ok := notExist.File()
	require.True(t, ok)
	require.Equal(t, File("mem://unregistered-errors/missing.txt"), file)

	err = memFS.WriteAll(t.Context(), "/no/such/dir.txt", nil, 0)
	require.ErrorContains(t, err, "mem://unregistered-errors/no/such")
}

// TestMemFileSystem_CloseWithActiveWatch checks that Close doesn't wait for
// a blocked watch callback and drops the queued events of the watch, so the
// callback is not called again after it returns. The synctest bubble makes
// sure that the callback blocks and the second event is queued before Close.
func TestMemFileSystem_CloseWithActiveWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		memFS, err := NewMemFileSystem(nil)
		require.NoError(t, err)
		delivered := make(chan string, 2)
		release := make(chan struct{})
		_, err = memFS.Watch("/", func(file File, _ Event) {
			delivered <- file.Name()
			<-release
		})
		require.NoError(t, err)
		require.NoError(t, memFS.Touch("/first.txt", 0))
		require.NoError(t, memFS.Touch("/second.txt", 0))
		synctest.Wait() // The first callback blocks, the second event is queued
		require.Equal(t, "first.txt", <-delivered)

		require.NoError(t, memFS.Close(), "Close must not wait for the blocked callback")
		close(release)
		synctest.Wait() // The delivering goroutine exits
		assert.Empty(t, delivered, "the queued event must be dropped by Close")
	})
}

func TestMemFileSystem_WatchSerialOrder(t *testing.T) {
	memFS := newTestMemFS(t)
	const numEvents = 30
	var (
		mtx      sync.Mutex
		got      []string
		inFlight atomic.Int32
		parallel atomic.Bool
		release  = make(chan struct{})
		done     = make(chan struct{})
	)
	cancel, err := memFS.Watch("/", func(f File, _ Event) {
		if inFlight.Add(1) > 1 {
			parallel.Store(true)
		}
		defer inFlight.Add(-1)
		<-release // The callbacks block until all events are produced
		time.Sleep(time.Millisecond)
		mtx.Lock()
		defer mtx.Unlock()
		got = append(got, f.Name())
		if len(got) == numEvents {
			close(done)
		}
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cancel() })

	otherEvents := make(chan Event, numEvents)
	cancelOther, err := memFS.Watch("/", func(_ File, e Event) { otherEvents <- e })
	require.NoError(t, err)
	t.Cleanup(func() { _ = cancelOther() })

	var want []string
	for i := range numEvents {
		name := fmt.Sprintf("file-%02d.txt", i)
		want = append(want, name)
		// A blocked callback must not block the change
		require.NoError(t, callMemOp(t, "WriteAll", func() error {
			return memFS.WriteAll(t.Context(), "/"+name, []byte("x"), 0)
		}))
	}
	// A blocked callback must not block the callbacks of other watches
	for range numEvents {
		select {
		case <-otherEvents:
		case <-time.After(5 * time.Second):
			t.Fatal("the events of another watch were blocked")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("not all events were delivered")
	}
	require.False(t, parallel.Load(), "the callbacks of a watch must be called serially")
	mtx.Lock()
	defer mtx.Unlock()
	require.Equal(t, want, got, "the events must be delivered in the order of the changes")
}
