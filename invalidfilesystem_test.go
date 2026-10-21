package fs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvalidFileSystem_Prefix(t *testing.T) {
	assert.Equal(t, "invalid://", InvalidFileSystem("").Prefix())
	assert.Equal(t, "invalid://test/", InvalidFileSystem("test").Prefix())
	assert.Equal(t, "invalid://test/", InvalidFileSystem("/test/").Prefix())
}

// checkInvalidErr checks that err of the operation op wraps
// ErrInvalidFileSystem, not os.ErrNotExist, and names the URI
// "nosuch://user:xxxxx@host/dir/file.txt" with the password redacted.
func checkInvalidErr(t *testing.T, err error, op string) {
	t.Helper()
	assert.ErrorIs(t, err, ErrInvalidFileSystem, op)
	assert.NotErrorIs(t, err, os.ErrNotExist, "%s must not report a missing file", op)
	if assert.Error(t, err, op) {
		assert.Contains(t, err.Error(), `"nosuch://user:xxxxx@host/dir/file.txt"`, "%s must name the URI", op)
		assert.NotContains(t, err.Error(), "secret", "%s must redact the password", op)
	}
}

// TestInvalidFileSystem_Methods calls every method of InvalidFileSystem.
func TestInvalidFileSystem_Methods(t *testing.T) {
	ctx := t.Context()
	for _, invalid := range []InvalidFileSystem{"", "test"} {
		t.Run("name "+string(invalid), func(t *testing.T) {
			if invalid == "" {
				assert.Equal(t, "invalid file system", invalid.Name())
				assert.Equal(t, "invalid file system", invalid.String())
			} else {
				assert.Equal(t, "test", invalid.Name())
				assert.Equal(t, "invalid file system test", invalid.String())
			}
			assert.Equal(t, invalid.String(), invalid.ID())
			assert.Equal(t, "/", invalid.Separator())
			assert.Equal(t, InvalidFile, invalid.RootDir())
			readable, writable := invalid.ReadableWritable()
			assert.True(t, readable, "readable, so that the operations reach the methods")
			assert.True(t, writable, "writable, so that the operations reach the methods")
			assert.NoError(t, invalid.Close())
			assert.NoError(t, invalid.Close())

			// Paths of the prefix are cleaned without leading separator
			assert.Equal(t, "a/b", invalid.CleanPath(invalid.Prefix()+"a//b/"))
			assert.Equal(t, "a/b", invalid.PathFromURI(invalid.Prefix()+"a/b"))
			assert.Equal(t, "a b", invalid.PathFromURI(invalid.Prefix()+"a%20b"), "URL escapes are decoded")
			assert.Equal(t, invalid.Prefix()+"a/b", invalid.URIFromPath("a/b"))
			dir, name := invalid.SplitDirAndName("a/b")
			assert.Equal(t, "a", dir)
			assert.Equal(t, "b", name)

			const filePath = "nosuch://user:secret@host/dir/file.txt"
			info, err := invalid.Stat(filePath)
			assert.Nil(t, info)
			checkInvalidErr(t, err, "Stat")
			called := false
			err = invalid.ListDir(ctx, filePath, nil, func(*FileInfo) error { called = true; return nil })
			checkInvalidErr(t, err, "ListDir")
			assert.False(t, called, "ListDir must not call the callback")
			r, err := invalid.OpenReader(filePath)
			assert.Nil(t, r)
			checkInvalidErr(t, err, "OpenReader")
			w, err := invalid.OpenWriter(filePath, AllRead)
			assert.Nil(t, w)
			checkInvalidErr(t, err, "OpenWriter")
			rw, err := invalid.OpenReadWriter(filePath, AllRead)
			assert.Nil(t, rw)
			checkInvalidErr(t, err, "OpenReadWriter")
			checkInvalidErr(t, invalid.MakeDir(filePath, AllRead), "MakeDir")
			checkInvalidErr(t, invalid.MakeAllDirs(filePath, AllRead), "MakeAllDirs")
			checkInvalidErr(t, invalid.Remove(filePath), "Remove")

			// Without a path there is no URI to report
			_, err = invalid.Stat("")
			assert.Equal(t, ErrInvalidFileSystem, err)
		})
	}
}

// TestInvalidFileSystem_URIs verifies that a URI without registered
// file system keeps its scheme and authority when it is cleaned, split
// and joined. CleanPath used to clean the whole URI as path, so the
// directory of "nosuch://host/dir/file.txt" was "invalid://nosuch:/host/dir",
// which also defeated the redaction of the password in error messages.
func TestInvalidFileSystem_URIs(t *testing.T) {
	for _, tc := range []struct{ uri, dir, name string }{
		{"nosuch://host/dir/file.txt", "nosuch://host/dir", "file.txt"},
		{"nosuch://host/file.txt", "nosuch://host", "file.txt"},
		{"nosuch://user:secret@host/dir/file.txt", "nosuch://user:secret@host/dir", "file.txt"},
		{"nosuch:///abs/file.txt", "nosuch:///abs", "file.txt"},
	} {
		file := File(tc.uri)
		require.Equal(t, FileSystem(Invalid), file.FileSystem(), "%s has no registered file system", tc.uri)
		dir, name := file.DirAndName()
		assert.Equal(t, File(tc.dir), dir, "directory of %s", tc.uri)
		assert.Equal(t, tc.name, name, "name of %s", tc.uri)
		assert.Equal(t, file, dir.Join(name), "joining the name to the directory of %s", tc.uri)
		assert.Equal(t, tc.name, file.Name())
	}
	assert.Equal(t, "nosuch://host", Invalid.CleanPath("nosuch://host/"))
	assert.Equal(t, "nosuch://host/b", Invalid.CleanPath("nosuch://host//a/../b/"))
	assert.Equal(t, "nosuch://host/a/b", Invalid.CleanPath("nosuch://host", "a", "b"))
	assert.Equal(t, "nosuch://host/x", Invalid.URIFromPath("nosuch://host/x"))
	dir, name := Invalid.SplitDirAndName("nosuch://host")
	assert.Equal(t, "nosuch://host", dir, "the authority is never split")
	assert.Empty(t, name)
}

// TestInvalidFileSystem_FileAPI verifies that every operation of the
// File API on a URI without registered file system reports
// ErrInvalidFileSystem with the URI, instead of a missing file or
// a write to a bogus local path. The emulations of OpenReadWriter
// and MakeAllDirs used to succeed or to report the path ".".
func TestInvalidFileSystem_FileAPI(t *testing.T) {
	ctx := t.Context()
	file := File("nosuch://user:secret@host/dir/file.txt")

	_, err := file.Stat()
	checkInvalidErr(t, err, "File.Stat")
	_, err = file.ReadAll(ctx)
	checkInvalidErr(t, err, "File.ReadAll")
	_, err = file.OpenReader()
	checkInvalidErr(t, err, "File.OpenReader")
	_, err = file.OpenReadSeeker()
	checkInvalidErr(t, err, "File.OpenReadSeeker")
	checkInvalidErr(t, file.ListDir(ctx, func(File) error { return nil }), "File.ListDir")
	checkInvalidErr(t, file.ListDirRecursive(ctx, func(File) error { return nil }), "File.ListDirRecursive")
	checkInvalidErr(t, file.WriteAllString(ctx, "x"), "File.WriteAll")
	checkInvalidErr(t, file.AppendString(ctx, "x"), "File.Append")
	_, err = file.OpenWriter()
	checkInvalidErr(t, err, "File.OpenWriter")
	_, err = file.OpenAppendWriter()
	checkInvalidErr(t, err, "File.OpenAppendWriter")
	_, err = file.OpenReadWriter()
	checkInvalidErr(t, err, "File.OpenReadWriter")
	checkInvalidErr(t, file.MakeDir(), "File.MakeDir")
	checkInvalidErr(t, file.MakeAllDirs(), "File.MakeAllDirs")
	checkInvalidErr(t, file.Touch(), "File.Touch")
	checkInvalidErr(t, file.Truncate(ctx, 0), "File.Truncate")
	checkInvalidErr(t, file.Remove(), "File.Remove")
	checkInvalidErr(t, file.RemoveRecursive(ctx), "File.RemoveRecursive")
	checkInvalidErr(t, CopyFile(ctx, file, File(t.TempDir()).Join("dest.txt")), "CopyFile from the file")
	assert.False(t, file.Exists())
	assert.False(t, file.IsDir())

	// The operations on InvalidFile, which has no path, report the empty path
	_, err = InvalidFile.ReadAll(ctx)
	assert.ErrorIs(t, err, ErrEmptyPath)
	assert.ErrorIs(t, InvalidFile.WriteAllString(ctx, "x"), ErrEmptyPath)
	assert.False(t, InvalidFile.Exists())
}
