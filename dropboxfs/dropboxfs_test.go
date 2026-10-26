package dropboxfs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// Small upload limits for the tests, so that the conformance suite's
// large content uses upload sessions with several chunks
const (
	testChunkSize = 256 << 10
	testMaxUpload = 512 << 10
)

// newTestFS returns a registered file system on a fakeClient
// with small upload limits, closed by t.Cleanup.
func newTestFS(t *testing.T, opts *Options) (*fileSystem, *fakeClient) {
	t.Helper()
	client := newFakeClient()
	client.maxUpload = testMaxUpload
	dbfs := newFileSystem("dbid:test-"+strings.ReplaceAll(t.Name(), "/", "-"), client, opts)
	dbfs.maxUpload = testMaxUpload
	dbfs.chunkSize = testChunkSize
	require.NoError(t, fs.Register(dbfs))
	t.Cleanup(func() { _ = dbfs.Close() })
	return dbfs, client
}

// TestConformance runs the conformance suite offline against the fake
// Dropbox API, with and without the metadata cache.
func TestConformance(t *testing.T) {
	for _, cacheTimeout := range []time.Duration{0, time.Minute} {
		t.Run("CacheTimeout="+cacheTimeout.String(), func(t *testing.T) {
			dbfs, client := newTestFS(t, &Options{CacheTimeout: cacheTimeout})
			require.NoError(t, dbfs.MakeDir("/conformance", 0))
			fstest.RunConformance(t, dbfs, fstest.Config{
				Name:    "Dropbox",
				Prefix:  dbfs.Prefix(),
				TestDir: "/conformance",
			})
			assert.Positive(t, client.callCount("UploadSessionFinish"), "the large content must be uploaded with an upload session")
			assert.Positive(t, client.callCount("ListFolderContinue"), "listings must be paginated")
		})
	}
}

// TestConformanceOnline runs the conformance suite against a real
// Dropbox account if DROPBOX_ACCESS_TOKEN is set, see README.md.
func TestConformanceOnline(t *testing.T) {
	accessToken := os.Getenv("DROPBOX_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("Skipping Dropbox filesystem test: DROPBOX_ACCESS_TOKEN environment variable not set")
	}
	testDir := os.Getenv("DROPBOX_TEST_DIR")
	if testDir == "" {
		testDir = "/go-fs-test"
	}
	mute := os.Getenv("DROPBOX_MUTE") == "true" || os.Getenv("DROPBOX_MUTE") == "1"

	dbfs, err := New(t.Context(), accessToken, &Options{CacheTimeout: 5 * time.Minute, Mute: mute})
	require.NoError(t, err, "New")
	assert.True(t, strings.HasPrefix(dbfs.ID(), "dbid:"), "ID must be the Dropbox account id")
	assert.Equal(t, Prefix+strings.TrimPrefix(dbfs.ID(), "dbid:"), dbfs.Prefix())
	t.Cleanup(func() {
		if err := dbfs.Close(); err != nil {
			t.Logf("Error closing filesystem: %v", err)
		}
	})

	fstest.RunConformance(t, dbfs, fstest.Config{
		Name:    "Dropbox",
		Prefix:  dbfs.Prefix(),
		TestDir: testDir,
	})
}

// newTestAPIServer starts a server for the users/get_current_account
// endpoint that requires the Authorization header auth,
// and returns the SDK configuration for it.
func newTestAPIServer(t *testing.T, accessToken string, opts *Options, auth string) (*httptest.Server, func() (*fileSystem, error)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != auth {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error_summary": "invalid_access_token/", "error": {".tag": "invalid_access_token"}}`))
			return
		}
		if r.URL.Path != "/2/users/get_current_account" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account_id":   "dbid:AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc",
			"email":        "alice@example.com",
			"account_type": map[string]any{".tag": "basic"},
			"root_info": map[string]any{
				".tag":              "user",
				"root_namespace_id": "3235641",
				"home_namespace_id": "3235641",
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() (*fileSystem, error) {
		config, err := newConfig(accessToken, opts)
		if err != nil {
			return nil, err
		}
		config.URLGenerator = func(hostType, namespace, route string) string {
			return srv.URL + "/2/" + namespace + "/" + route
		}
		return newFromConfig(t.Context(), config, opts)
	}
}

// TestNew checks the account request with the access token
// or Options.HTTPClient, and the identity of the file system.
func TestNew(t *testing.T) {
	_, newFS := newTestAPIServer(t, "my-token", nil, "Bearer my-token")
	dbfs, err := newFS()
	require.NoError(t, err)
	assert.Equal(t, "dbid:AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc", dbfs.ID())
	assert.Equal(t, "dropbox://AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc", dbfs.Prefix())
	assert.Equal(t, "Dropbox", dbfs.Name())
	assert.Equal(t, "Dropbox dropbox://AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc", dbfs.String())
	assert.NotContains(t, dbfs.String(), "my-token")

	_, newFS = newTestAPIServer(t, "wrong-token", nil, "Bearer my-token")
	_, err = newFS()
	assert.ErrorContains(t, err, "invalid_access_token")
	assert.NotContains(t, err.Error(), "wrong-token", "the token must not be part of the error")

	// Options.HTTPClient replaces the token, like an oauth2 client that refreshes it
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer refreshed")
		return http.DefaultTransport.RoundTrip(r)
	})}
	_, newFS = newTestAPIServer(t, "", &Options{HTTPClient: client}, "Bearer refreshed")
	dbfs, err = newFS()
	require.NoError(t, err)
	assert.Equal(t, "dbid:AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc", dbfs.ID())

	_, err = New(t.Context(), "", nil)
	assert.ErrorContains(t, err, "empty access token")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = New(ctx, "token", nil)
	assert.ErrorIs(t, err, context.Canceled)
	err = NewAndRegister(ctx, "token", nil)
	assert.ErrorIs(t, err, context.Canceled)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestOpenReadWriter_Closed is the regression test for OpenReadWriter,
// which went through the registry and returned "invalid file system"
// instead of fs.ErrFileSystemClosed after Close.
func TestOpenReadWriter_Closed(t *testing.T) {
	dbfs, _ := newTestFS(t, nil)
	_, err := dbfs.OpenReadWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	require.NoError(t, dbfs.Close())
	_, err = dbfs.OpenReadWriter("/x.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// TestOpenReadWriter checks that an unregistered file system
// reads and writes through its own client.
func TestOpenReadWriter(t *testing.T) {
	client := newFakeClient()
	dbfs := newFileSystem("dbid:unregistered", client, nil)
	ctx := t.Context()
	require.NoError(t, dbfs.WriteAll(ctx, "/rw.txt", []byte("Initial"), 0))
	rw, err := dbfs.OpenReadWriter("/rw.txt", 0)
	require.NoError(t, err)
	_, err = rw.Write([]byte("Updated"))
	require.NoError(t, err)
	require.NoError(t, rw.Close())
	assert.Equal(t, "Updated", string(client.entries["/rw.txt"].data))

	require.NoError(t, dbfs.MakeDir("/dir", 0))
	_, err = dbfs.OpenReadWriter("/dir", 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
}

// TestMove_Overwrite is the regression test for Move and CopyFile
// onto an existing destination, which failed with to/conflict.
func TestMove_Overwrite(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, &Options{CacheTimeout: time.Minute})
	require.NoError(t, dbfs.WriteAll(ctx, "/src.txt", []byte("src"), 0))
	require.NoError(t, dbfs.WriteAll(ctx, "/dest.txt", []byte("dest"), 0))
	require.NoError(t, dbfs.WriteAll(ctx, "/dir/keep.txt", []byte("keep"), 0))

	require.NoError(t, dbfs.CopyFile(ctx, "/src.txt", "/dest.txt"))
	assert.Equal(t, "src", string(client.entries["/dest.txt"].data))
	require.NoError(t, dbfs.WriteAll(ctx, "/dest.txt", []byte("dest"), 0))
	require.NoError(t, dbfs.Move(ctx, "/src.txt", "/dest.txt"))
	assert.Equal(t, "src", string(client.entries["/dest.txt"].data))
	assert.NotContains(t, client.entries, "/src.txt")
	_, err := dbfs.Stat("/src.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "the cache must forget the moved source")

	assert.ErrorIs(t, dbfs.Move(ctx, "/dest.txt", "/dir"), os.ErrExist)
	assert.ErrorIs(t, dbfs.CopyFile(ctx, "/dest.txt", "/dir"), fs.ErrIsDirectory{})
	assert.ErrorIs(t, dbfs.Move(ctx, "/dir", "/dest.txt"), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, dbfs.Move(ctx, "/dir", "/dir/sub"), os.ErrInvalid)
	assert.ErrorIs(t, dbfs.Move(ctx, "/dest.txt", "/dest.txt/sub"), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, dbfs.Move(ctx, "/dest.txt", "/"), os.ErrExist)
	assert.ErrorIs(t, dbfs.CopyFile(ctx, "/dir", "/new"), fs.ErrIsDirectory{})
	assert.ErrorIs(t, dbfs.CopyFile(ctx, "/dest.txt", "/"), fs.ErrIsDirectory{})
	assert.ErrorIs(t, dbfs.Move(ctx, "/missing.txt", "/new.txt"), os.ErrNotExist)
	assert.Equal(t, "keep", string(client.entries["/dir/keep.txt"].data))
	assert.Equal(t, "src", string(client.entries["/dest.txt"].data))

	// A file in the way of a parent of the destination
	err = dbfs.CopyFile(ctx, "/dest.txt", "/dest.txt-copy/sub/x.txt")
	require.NoError(t, err, "Dropbox creates missing parents")
	err = dbfs.CopyFile(ctx, "/dest.txt", "/dest.txt/x.txt")
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{})
}

// TestMove_ReplaceOnlyCheckedVersion checks that Move and CopyFile replace
// only the version of the destination file that relocate checked: another
// client uploads a newer version between the check and the delete,
// which must survive the failed Move or CopyFile.
func TestMove_ReplaceOnlyCheckedVersion(t *testing.T) {
	for name, relocate := range map[string]func(dbfs *fileSystem, ctx context.Context, src, dest string) error{
		"Move":     (*fileSystem).Move,
		"CopyFile": (*fileSystem).CopyFile,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			dbfs, client := newTestFS(t, nil)
			require.NoError(t, dbfs.WriteAll(ctx, "/src.txt", []byte("src"), 0))
			require.NoError(t, dbfs.WriteAll(ctx, "/dest.txt", []byte("dest"), 0))
			client.onCall("DeleteV2", func() {
				dest := client.entries["/dest.txt"]
				dest.data, dest.rev = []byte("newer"), "rev-newer"
			})

			err := relocate(dbfs, ctx, "/src.txt", "/dest.txt")
			assert.ErrorIs(t, err, os.ErrExist, "the newer destination is in the way")
			assert.Equal(t, 1, client.callCount("DeleteV2"), "the hook must have replaced the destination")
			assert.Equal(t, "newer", string(client.entries["/dest.txt"].data), "the newer version must not be deleted")
			assert.Equal(t, "src", string(client.entries["/src.txt"].data), "the source must be kept")
		})
	}
}

// TestCopyFile_SourceBecameFolder is the regression test for a CopyFile
// whose cached source file became a folder on the server: relocate deleted
// the destination file before the copy found the folder, so the failed
// copy lost the destination.
func TestCopyFile_SourceBecameFolder(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, &Options{CacheTimeout: time.Hour})
	require.NoError(t, dbfs.WriteAll(ctx, "/src", []byte("src"), 0))
	require.NoError(t, dbfs.WriteAll(ctx, "/dest.txt", []byte("dest"), 0))

	// Another client replaces the cached source file with a folder
	delete(client.entries, "/src")
	client.entries["/src"] = &fakeEntry{display: "/src", isDir: true, id: "folder"}
	client.entries["/src/a.txt"] = &fakeEntry{display: "/src/a.txt", data: []byte("a"), id: "a", rev: "1"}

	err := dbfs.CopyFile(ctx, "/src", "/dest.txt")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	require.Contains(t, client.entries, "/dest.txt", "the destination must not be deleted")
	assert.Equal(t, "dest", string(client.entries["/dest.txt"].data))
	assert.NotContains(t, client.entries, "/dest.txt/a.txt", "the folder must not be copied")
	assert.Zero(t, client.callCount("DeleteV2"), "nothing must be deleted")
}

// TestMove_CaseOnly checks a rename that only changes the case of the
// name, which Dropbox refuses as a move onto the file itself. The
// replacing logic of Move must not delete the file then.
func TestMove_CaseOnly(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(ctx, "/name.txt", []byte("content"), 0))

	require.NoError(t, dbfs.Move(ctx, "/name.txt", "/Name.txt"))
	e := client.entries["/name.txt"]
	require.NotNil(t, e, "the file must not be deleted")
	assert.Equal(t, "/Name.txt", e.display)
	assert.Equal(t, "content", string(e.data))
	assert.Len(t, client.entries, 1, "no temporary file is left behind")

	assert.NoError(t, dbfs.CopyFile(ctx, "/name.txt", "/NAME.TXT"), "a copy onto itself is a no-op")
	assert.Len(t, client.entries, 1)

	// The original name is restored if the move from the temporary name fails
	client.setFailNext("MoveV2", nil, nil, os.ErrDeadlineExceeded)
	err := dbfs.Move(ctx, "/Name.txt", "/name.txt")
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.Equal(t, "/Name.txt", client.entries["/name.txt"].display)
	assert.Len(t, client.entries, 1, "no temporary file is left behind")

	require.NoError(t, dbfs.Move(ctx, "/Name.txt", "/name.txt"))
	assert.Equal(t, "/name.txt", client.entries["/name.txt"].display)
}

// TestRemove_StaleCache is the regression test for Remove trusting
// a cached FileInfo of a file, while DeleteV2 deletes folders recursively.
func TestRemove_StaleCache(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, &Options{CacheTimeout: time.Hour})
	require.NoError(t, dbfs.WriteAll(ctx, "/x", []byte("file"), 0))
	_, err := dbfs.Stat("/x")
	require.NoError(t, err)

	// Another client replaces the file with a folder with content
	delete(client.entries, "/x")
	client.entries["/x"] = &fakeEntry{display: "/x", isDir: true, id: "folder"}
	client.entries["/x/important.txt"] = &fakeEntry{display: "/x/important.txt", data: []byte("important"), id: "file", rev: "1"}

	err = dbfs.Remove("/x")
	assert.ErrorIs(t, err, fs.ErrDirectoryNotEmpty{})
	assert.Contains(t, client.entries, "/x/important.txt", "the folder content must not be deleted")
}

// TestCache checks that the cache is used, case-insensitive,
// and invalidated by writes.
func TestCache(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, &Options{CacheTimeout: time.Hour})
	require.NoError(t, dbfs.WriteAll(ctx, "/Dir/File.txt", []byte("x"), 0))
	calls := client.callCount("GetMetadata")
	info, err := dbfs.Stat("/dir/file.TXT")
	require.NoError(t, err)
	assert.Equal(t, "File.txt", info.Name)
	assert.Equal(t, calls, client.callCount("GetMetadata"), "the upload result must be cached under a case-insensitive key")

	info.Name = "modified"
	info, err = dbfs.Stat("/Dir/File.txt")
	require.NoError(t, err)
	assert.Equal(t, "File.txt", info.Name, "callers get a copy of the cached FileInfo")

	require.NoError(t, dbfs.RemoveAll(ctx, "/DIR"))
	_, err = dbfs.Stat("/Dir/File.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "RemoveAll must invalidate the entries below the folder")
}

// TestRemove checks the typed errors of Remove and RemoveAll.
func TestRemove(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(ctx, "/dir/a.txt", []byte("a"), 0))
	assert.ErrorIs(t, dbfs.Remove("/dir"), fs.ErrDirectoryNotEmpty{})
	assert.ErrorIs(t, dbfs.Remove("/missing"), os.ErrNotExist)
	assert.ErrorContains(t, dbfs.Remove("/"), "root")
	assert.ErrorContains(t, dbfs.RemoveAll(ctx, "/"), "root")
	assert.NoError(t, dbfs.RemoveAll(ctx, "/missing"))
	require.NoError(t, dbfs.Remove("/dir/a.txt"))
	require.NoError(t, dbfs.Remove("/dir"))
	assert.Empty(t, client.entries)
}

// TestErrors checks that transport errors are never mistaken
// for missing files and that API errors are mapped.
func TestErrors(t *testing.T) {
	ctx := t.Context()
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(ctx, "/file.txt", []byte("x"), 0))

	client.setFailNext("GetMetadata", os.ErrDeadlineExceeded)
	_, err := dbfs.Stat("/file.txt")
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.NotErrorIs(t, err, os.ErrNotExist)

	client.setFailNext("DeleteV2", os.ErrDeadlineExceeded)
	assert.ErrorIs(t, dbfs.RemoveAll(ctx, "/file.txt"), os.ErrDeadlineExceeded)

	client.setFailNext("ListFolderContinue", os.ErrDeadlineExceeded)
	for _, name := range []string{"a", "b", "c", "d"} {
		require.NoError(t, dbfs.WriteAll(ctx, "/list/"+name, nil, 0))
	}
	err = dbfs.ListDir(ctx, "/list", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)

	err = dbfs.ListDir(ctx, "/file.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{})
	_, err = dbfs.OpenReader("/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.ErrorIs(t, dbfs.MakeDir("/file.txt", 0), os.ErrExist)
	assert.ErrorIs(t, dbfs.MakeDir("/", 0), os.ErrExist)
	assert.ErrorIs(t, dbfs.MakeDir("/file.txt/sub", 0), fs.ErrIsNotDirectory{})
}

// TestClosedFileSystem verifies that after Close every method that uses the
// Dropbox API returns fs.ErrFileSystemClosed instead of dereferencing a
// closed client.
func TestClosedFileSystem(t *testing.T) {
	// A real (offline) client is fine: closed methods short-circuit before any
	// network call, so no token or connectivity is required.
	config, err := newConfig("offline-token", nil)
	require.NoError(t, err)
	dbfs := newFileSystem("dbid:test", files.NewContext(config), &Options{CacheTimeout: time.Minute})
	require.NoError(t, fs.Register(dbfs))
	require.True(t, fs.IsRegistered(dbfs), "filesystem should be registered before Close")

	require.NoError(t, dbfs.Close())
	assert.False(t, fs.IsRegistered(dbfs), "Close must unregister the filesystem")
	assert.NoError(t, dbfs.Close(), "Close is idempotent")

	ctx := t.Context()
	_, err = dbfs.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.WriteAll(ctx, "/file", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.Remove("/file"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.Move(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.CopyFile(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	_, err = dbfs.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = dbfs.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = dbfs.OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil }), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, dbfs.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
}

// TestRemoveRoot verifies that the root folder of the account is never
// removed and that the error wraps os.ErrInvalid like in the other backends.
func TestRemoveRoot(t *testing.T) {
	dbfs, client := newTestFS(t, nil)
	require.NoError(t, dbfs.WriteAll(t.Context(), "/file.txt", []byte("x"), 0))
	assert.ErrorIs(t, dbfs.Remove("/"), os.ErrInvalid, "Remove of the root")
	assert.ErrorIs(t, dbfs.RemoveAll(t.Context(), "/"), os.ErrInvalid, "RemoveAll of the root")
	assert.Contains(t, client.entries, "/file.txt", "nothing must be removed")
}

// TestValidateName verifies the names that a copy or move into the file
// system rejects with ValidateName before it writes anything.
func TestValidateName(t *testing.T) {
	f := &fileSystem{}
	for _, name := range []string{"a.txt", "Größe: what?.txt"} {
		assert.NoError(t, f.ValidateName(name), "%q", name)
	}
	assert.ErrorIs(t, f.ValidateName("\xff.txt"), os.ErrInvalid)
}
