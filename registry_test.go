package fs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRawURI(t *testing.T) {
	fs, fsPath := ParseRawURI("")
	assert.Equal(t, InvalidFileSystem(""), fs)
	assert.Equal(t, "", fsPath)

	fs, fsPath = ParseRawURI("/")
	assert.Equal(t, Local, fs)
	assert.Equal(t, string(filepath.Separator), fsPath)

	fs, fsPath = ParseRawURI(filepath.Join(string(filepath.Separator), "a", "b"))
	assert.Equal(t, Local, fs)
	assert.Equal(t, filepath.Join(string(filepath.Separator), "a", "b"), fsPath)

	testFS := InvalidFileSystem("test")
	longerTestFS := InvalidFileSystem("longerTest")
	Register(testFS)
	Register(longerTestFS)
	t.Cleanup(func() {
		Unregister(testFS)
		Unregister(longerTestFS)
	})
	assert.Equal(t, Invalid, GetFileSystem("invalid://"))
	assert.Equal(t, testFS, GetFileSystem("invalid://test/"))
	assert.Equal(t, longerTestFS, GetFileSystem("invalid://longerTest/"))

	fs, fsPath = ParseRawURI("invalid://file")
	assert.Equal(t, Invalid, fs)
	assert.Equal(t, "file", fsPath)

	fs, fsPath = ParseRawURI("invalid://test/file")
	assert.Equal(t, testFS, fs)
	assert.Equal(t, "file", fsPath)

	fs, fsPath = ParseRawURI("invalid://longerTest/file")
	assert.Equal(t, longerTestFS, fs)
	assert.Equal(t, "file", fsPath)
}

// TestParseRawURI_SchemeNotLocal verifies that a URI carrying a scheme
// ("://") which matches no registered file system does NOT silently resolve
// to the local file system, except for the local file:// scheme itself.
func TestParseRawURI_SchemeNotLocal(t *testing.T) {
	// Unregistered remote schemes must not become local paths.
	for _, uri := range []string{
		"s3://bucket/key",
		"http://example.com/file.txt",
		"https://example.com/file.txt",
		"ftp://user@host/path",
		"sftp://user@host/path",
		"dropbox://something/file",
	} {
		fs, _ := ParseRawURI(uri)
		assert.Equalf(t, Invalid, fs, "unregistered scheme %q must resolve to the invalid file system, not local", uri)
	}

	// The local file:// scheme resolves to the local file system.
	fs, fsPath := ParseRawURI(LocalPrefix + "/home/user/file.txt")
	assert.Equal(t, Local, fs, "file:// must resolve to the local file system")
	assert.Equal(t, Local.CleanPath(LocalPrefix+"/home/user/file.txt"), fsPath)

	// Plain paths without a scheme resolve to the local file system.
	for _, uri := range []string{
		"/home/user/file.txt",
		"relative/path.txt",
		"file.txt",
	} {
		fs, fsPath := ParseRawURI(uri)
		assert.Equalf(t, Local, fs, "scheme-less path %q must resolve to the local file system", uri)
		assert.Equal(t, filepath.FromSlash(uri), fsPath)
	}
}

func TestParseRawURI_escapes(t *testing.T) {
	// The prefix can contain escapes itself, like the user name of an
	// smbfs prefix, so only the path after the prefix is decoded.
	escapedFS := InvalidFileSystem("user%40example.com")
	Register(escapedFS)
	t.Cleanup(func() { Unregister(escapedFS) })

	fs, fsPath := ParseRawURI(escapedFS.Prefix() + "a%20b/100%2525.txt")
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "a b/100%25.txt", fsPath, "URL escapes after the prefix are decoded once")

	fs, fsPath = ParseRawURI(escapedFS.Prefix() + "50% off.txt")
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "50% off.txt", fsPath, "a path with an invalid escape is used as it is")

	// The URI of a path is its inverse, so a name that looks
	// URL escaped keeps its name through File methods.
	file := fsFile(escapedFS, "a b/100%25.txt")
	assert.Equal(t, escapedFS.Prefix()+"a b/100%2525.txt", string(file))
	fs, fsPath = file.ParseRawURI()
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "a b/100%25.txt", fsPath)
	assert.Equal(t, "100%25.txt", file.Name())
	assert.Equal(t, fsFile(escapedFS, "a b"), file.Dir())
}

// TestParseRawURI_PrefixBoundary checks that a prefix only matches
// at a boundary, so "s3://logs" never serves "s3://logs-archive".
func TestParseRawURI_PrefixBoundary(t *testing.T) {
	logs, err := NewMemFileSystem(&MemFileSystemOptions{ID: "logs"})
	require.NoError(t, err)
	require.NoError(t, Register(logs))
	t.Cleanup(func() { _ = logs.Close() })

	for _, uri := range []string{
		"mem://logs-archive/key",
		"mem://logs.example.com/key",
		"mem://logs:2121/key",
		"mem://logs2/key",
	} {
		fs, _ := ParseRawURI(uri)
		assert.Equal(t, Invalid, fs, "%q must not match the prefix mem://logs", uri)
	}
	for _, uri := range []string{"mem://logs", "mem://logs/", "mem://logs/key"} {
		fs, _ := ParseRawURI(uri)
		assert.Equal(t, FileSystem(logs), fs, "%q must match the prefix mem://logs", uri)
	}
}

// TestRegister_Identity checks that Register rejects a different file system
// with the same prefix and that Unregister only removes the passed instance.
func TestRegister_Identity(t *testing.T) {
	a, err := NewMemFileSystem(&MemFileSystemOptions{ID: "register-identity"})
	require.NoError(t, err)
	b, err := NewMemFileSystem(&MemFileSystemOptions{ID: "register-identity"})
	require.NoError(t, err)

	require.NoError(t, Register(a))
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, Register(a), "registering the same file system again is a no-op")
	require.ErrorIs(t, Register(b), ErrAlreadyRegistered, "another file system with the same prefix")

	assert.True(t, IsRegistered(a))
	assert.False(t, IsRegistered(b), "b has the same prefix but is not registered")
	assert.False(t, Unregister(b), "b was not registered")
	assert.True(t, IsRegistered(a), "unregistering b must not unregister a")

	assert.True(t, Unregister(a))
	assert.False(t, IsRegistered(a), "Register is not reference counted")
	assert.False(t, Unregister(a))

	assert.Error(t, Register(nil))
	assert.NotPanics(t, func() { Unregister(uncomparableFS{InvalidFileSystem: InvalidFileSystem("x")}) })
}

// uncomparableFS is a FileSystem value that can't be compared with ==.
type uncomparableFS struct {
	InvalidFileSystem
	_ []byte
}

// TestEnsureRegistered checks that concurrent calls for the same prefix
// share one created file system that is closed after the last free.
func TestEnsureRegistered(t *testing.T) {
	const prefix = "mem://ensure-registered"
	var created atomic.Int32
	create := func(ctx context.Context) (FileSystem, error) {
		created.Add(1)
		time.Sleep(10 * time.Millisecond) // let the other goroutines wait for this call
		return NewMemFileSystem(&MemFileSystemOptions{ID: "ensure-registered"})
	}

	const n = 20
	frees := make([]func() error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			fileSystem, free, err := EnsureRegistered(t.Context(), prefix, create)
			assert.NoError(t, err)
			assert.Equal(t, prefix, fileSystem.Prefix())
			frees[i] = free
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), created.Load(), "concurrent calls must share one create call")

	memFS := GetFileSystemByPrefixOrNil(prefix)
	require.NotNil(t, memFS)
	for i, free := range frees {
		require.NoError(t, free())
		require.NoError(t, free(), "free is idempotent")
		if i < n-1 {
			assert.True(t, IsRegistered(memFS), "registered while referenced")
		}
	}
	assert.False(t, IsRegistered(memFS), "the last free unregisters")
	assert.False(t, memFS.RootDir().Exists(), "the last free closes")

	// A file system created with the wrong prefix is closed and rejected
	_, _, err := EnsureRegistered(t.Context(), "mem://other-prefix", create)
	assert.Error(t, err)

	// A canceled context fails without creating
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = EnsureRegistered(ctx, prefix, create)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestEnsureRegistered_Registered checks that a file system registered
// with Register is shared but never closed by free.
func TestEnsureRegistered_Registered(t *testing.T) {
	memFS, err := NewMemFileSystem(&MemFileSystemOptions{ID: "ensure-registered-plain"})
	require.NoError(t, err)
	require.NoError(t, Register(memFS))
	t.Cleanup(func() { _ = memFS.Close() })

	got, free, err := EnsureRegistered(t.Context(), memFS.Prefix(), func(context.Context) (FileSystem, error) {
		t.Fatal("must not create a registered file system")
		return nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, FileSystem(memFS), got)
	require.NoError(t, free())
	assert.True(t, IsRegistered(memFS), "free must not unregister a file system registered with Register")
	assert.True(t, memFS.RootDir().Exists(), "free must not close a file system registered with Register")
}

// TestInvalidFileSystem_Errors checks that files with an unregistered
// prefix report ErrInvalidFileSystem with the URI and a redacted password.
func TestInvalidFileSystem_Errors(t *testing.T) {
	file := File("s3unregistered://user:secret@bucket/key.txt")
	_, err := file.Stat()
	require.ErrorIs(t, err, ErrInvalidFileSystem)
	assert.NotErrorIs(t, err, os.ErrNotExist)
	assert.NotContains(t, err.Error(), "secret", "the password must be redacted")
	assert.Contains(t, err.Error(), "bucket/key.txt")

	assert.ErrorIs(t, file.WriteAllString(t.Context(), "x"), ErrInvalidFileSystem)
	assert.ErrorIs(t, file.Remove(), ErrInvalidFileSystem)
	assert.ErrorIs(t, RemoveErrDoesNotExist(file.Remove()), ErrInvalidFileSystem, "must not be swallowed as not existing")
}
