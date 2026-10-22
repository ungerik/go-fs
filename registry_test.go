package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	stdfstest "testing/fstest"
	"testing/synctest"
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

	// A canceled context fails without creating
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := EnsureRegistered(ctx, prefix, create)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestEnsureRegistered_CreatorCanceled checks that a waiting call with a
// live ctx doesn't fail with the cancellation of the ctx of the call that
// runs create, but runs create itself. The synctest bubble makes sure
// that the waiter is waiting for the in-flight call when it fails.
func TestEnsureRegistered_CreatorCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const prefix = "mem://ensure-registered-creator-canceled"
		var created atomic.Int32
		create := func(ctx context.Context) (FileSystem, error) {
			if created.Add(1) == 1 {
				// The first call fails with the end of its ctx
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return NewMemFileSystem(&MemFileSystemOptions{ID: "ensure-registered-creator-canceled"})
		}

		creatorCtx, cancelCreator := context.WithCancel(t.Context())
		creatorErr := make(chan error, 1)
		go func() {
			_, _, err := EnsureRegistered(creatorCtx, prefix, create)
			creatorErr <- err
		}()
		synctest.Wait() // The creator blocks in create

		type result struct {
			fileSystem FileSystem
			free       func() error
			err        error
		}
		waiterResult := make(chan result, 1)
		go func() {
			fileSystem, free, err := EnsureRegistered(t.Context(), prefix, create)
			waiterResult <- result{fileSystem, free, err}
		}()
		synctest.Wait() // The waiter waits for the in-flight call

		cancelCreator()
		assert.ErrorIs(t, <-creatorErr, context.Canceled, "the creator gets the end of its own ctx")
		waiter := <-waiterResult
		require.NoError(t, waiter.err, "the waiter's ctx is still alive")
		assert.Equal(t, prefix, waiter.fileSystem.Prefix())
		assert.Equal(t, int32(2), created.Load(), "the waiter must run create itself")
		assert.True(t, IsRegistered(waiter.fileSystem))
		require.NoError(t, waiter.free())
		assert.False(t, IsRegistered(waiter.fileSystem), "the last free unregisters")
	})
}

// TestEnsureRegistered_CreateError checks that all calls waiting for a
// create call that fails with an error of its own get that error
// without calling create again, and that the failure isn't remembered.
func TestEnsureRegistered_CreateError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const prefix = "mem://ensure-registered-create-error"
		errCreate := errors.New("create failed")
		release := make(chan struct{})
		var created atomic.Int32
		failingCreate := func(context.Context) (FileSystem, error) {
			created.Add(1)
			<-release
			return nil, errCreate
		}

		const n = 10
		errs := make(chan error, n)
		for range n {
			go func() {
				_, _, err := EnsureRegistered(t.Context(), prefix, failingCreate)
				errs <- err
			}()
		}
		synctest.Wait() // One call runs create, the others wait for it
		close(release)
		for range n {
			assert.ErrorIs(t, <-errs, errCreate)
		}
		assert.Equal(t, int32(1), created.Load(), "the waiters must not run create again")
		assert.Nil(t, GetFileSystemByPrefixOrNil(prefix))

		// A later call runs create again
		fileSystem, free, err := EnsureRegistered(t.Context(), prefix, func(context.Context) (FileSystem, error) {
			return NewMemFileSystem(&MemFileSystemOptions{ID: "ensure-registered-create-error"})
		})
		require.NoError(t, err)
		assert.Equal(t, prefix, fileSystem.Prefix())
		require.NoError(t, free())
	})
}

// TestEnsureRegistered_WrongPrefix checks that a file system that create
// returns with another prefix than the requested one is closed
// and not registered.
func TestEnsureRegistered_WrongPrefix(t *testing.T) {
	var memFS *MemFileSystem
	_, _, err := EnsureRegistered(t.Context(), "mem://ensure-registered-requested", func(context.Context) (FileSystem, error) {
		var err error
		memFS, err = NewMemFileSystem(&MemFileSystemOptions{ID: "ensure-registered-other"})
		return memFS, err
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mem://ensure-registered-other")
	require.NotNil(t, memFS)
	assert.False(t, IsRegistered(memFS), "the rejected file system must not be registered")
	assert.Nil(t, GetFileSystemByPrefixOrNil("mem://ensure-registered-requested"))
	assert.Nil(t, GetFileSystemByPrefixOrNil("mem://ensure-registered-other"))
	_, err = memFS.Stat("/")
	assert.ErrorIs(t, err, ErrFileSystemClosed, "the rejected file system must be closed")
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

// TestAndRegisterConstructors checks the ...AndRegister constructors:
// the registered file system resolves the URIs of its prefix, and a
// second call with the same prefix fails with ErrAlreadyRegistered and
// discards the new instance, so the first one stays registered.
func TestAndRegisterConstructors(t *testing.T) {
	// newMemFS returns a MemFileSystem with the file /dir/a.txt
	newMemFS := func(t *testing.T, content string) *MemFileSystem {
		t.Helper()
		memFS, err := NewMemFileSystem(nil, NewMemFile("/dir/a.txt", []byte(content)))
		require.NoError(t, err)
		require.NoError(t, Register(memFS))
		t.Cleanup(func() { _ = memFS.Close() })
		return memFS
	}
	tests := []struct {
		name     string
		uri      string // of the file with the registered content
		register func(t *testing.T, content string) error
	}{
		{
			name: "NewStdFileSystemAndRegister",
			uri:  StdFileSystemPrefix + "and-register-std/a.txt",
			register: func(t *testing.T, content string) error {
				fsys := stdfstest.MapFS{"a.txt": {Data: []byte(content)}}
				return NewStdFileSystemAndRegister(fsys, "and-register-std")
			},
		},
		{
			name: "NewSubFileSystemAndRegister",
			uri:  SubFileSystemPrefix + "and-register-sub/a.txt",
			register: func(t *testing.T, content string) error {
				return NewSubFileSystemAndRegister(newMemFS(t, content), "/dir", "and-register-sub")
			},
		},
		{
			name: "NewOverlayFileSystemAndRegister",
			uri:  OverlayFileSystemPrefix + "and-register-overlay/dir/a.txt",
			register: func(t *testing.T, content string) error {
				base := newMemFS(t, content)
				upper := newMemFS(t, content)
				return NewOverlayFileSystemAndRegister(base, upper, "and-register-overlay")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, tt.register(t, "first"))
			file := File(tt.uri)
			registered := file.FileSystem()
			require.True(t, IsRegistered(registered), "the URI %s resolves to the registered file system", file)
			t.Cleanup(func() { _ = registered.Close() })
			data, err := file.ReadAllString(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "first", data)

			err = tt.register(t, "second")
			require.ErrorIs(t, err, ErrAlreadyRegistered, "a second file system with the same prefix")
			assert.Equal(t, registered, file.FileSystem(), "the first file system stays registered")
			data, err = file.ReadAllString(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "first", data, "the second file system is discarded")
		})
	}
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

// prefixTestFS is an InvalidFileSystem with any prefix.
type prefixTestFS struct {
	InvalidFileSystem
	prefix string
}

func (f *prefixTestFS) Prefix() string { return f.prefix }

// TestRedactURI_Errors is a regression test: the error messages
// that name a URI contained the password if url.Parse rejected the URI,
// or if url.Parse took a password of digits or an empty one with an
// unencoded '/' or '?' for a port, see fsimpl.RedactURI.
func TestRedactURI_Errors(t *testing.T) {
	for _, uri := range []string{
		"ftp-unregistered://user:hunter2@host/dir/100%.txt",
		"ftp-unregistered://user:hunter2@host:port/dir/file.txt",
		"ftp-unregistered://user:hunter2@host/dir/\x7f.txt",
		"ftp-unregistered://user:hunter2/x@host/dir/file.txt",
		"ftp-unregistered://user:/hunter2@host/dir/file.txt",
		"ftp-unregistered://user:123?hunter2@host/dir/file.txt",
	} {
		assert.NotContains(t, NewErrDoesNotExist(File(uri)).Error(), "hunter2", "ErrDoesNotExist for %q", uri)

		_, err := File(uri).Stat()
		require.ErrorIs(t, err, ErrInvalidFileSystem)
		assert.NotContains(t, err.Error(), "hunter2", "ErrInvalidFileSystem for %q", uri)

		registered := &prefixTestFS{prefix: uri}
		require.NoError(t, Register(registered))
		err = Register(&prefixTestFS{prefix: uri})
		Unregister(registered)
		require.ErrorIs(t, err, ErrAlreadyRegistered)
		assert.NotContains(t, err.Error(), "hunter2", "ErrAlreadyRegistered for %q", uri)

		_, _, err = EnsureRegistered(t.Context(), uri, func(context.Context) (FileSystem, error) {
			return &prefixTestFS{prefix: uri + "/other"}, nil
		})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "hunter2", "EnsureRegistered error for %q", uri)
	}
}
