package webdavfs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
)

// newTestFS returns a registered file system for the server srv below
// prefix with the credentials of the test server, closed by t.Cleanup.
func newTestFS(t *testing.T, srv *httptest.Server, prefix string) fs.FileSystem {
	t.Helper()
	davFS, err := New(t.Context(), srv.URL+prefix, &Options{Username: "alice", Password: "secret"})
	require.NoError(t, err)
	require.NoError(t, fs.Register(davFS))
	t.Cleanup(func() { _ = davFS.Close() })
	return davFS
}

func TestConformance(t *testing.T) {
	for _, style := range []struct {
		name  string
		style serverStyle
	}{
		{"x/net/webdav", styleXNet},
		{"nginx", styleNginx},
		{"Apache", styleApache},
	} {
		t.Run(style.name, func(t *testing.T) {
			srv := newStyledServer(t, style.style, "/dav", true)
			davFS := newTestFS(t, srv.Server, "/dav")
			require.Equal(t, "webdav://alice@"+srv.Listener.Addr().String()+"/dav", davFS.Prefix())

			require.NoError(t, davFS.(fs.WriteFileSystem).MakeDir("/conformance", 0))
			fstest.RunConformance(t, davFS, fstest.Config{
				Name:    "WebDAV",
				Prefix:  davFS.Prefix(),
				TestDir: "/conformance",
			})
		})
	}
}

func TestNew_Errors(t *testing.T) {
	srv := newTestServer(t, "/dav")
	_, err := New(t.Context(), "ftp://example.com/", nil)
	assert.Error(t, err, "scheme must be http or https")
	_, err = New(t.Context(), srv.URL+"/dav", nil)
	assert.ErrorIs(t, err, os.ErrPermission, "missing credentials")
	_, err = New(t.Context(), srv.URL+"/nowhere", &Options{Username: "alice", Password: "secret"})
	assert.ErrorIs(t, err, os.ErrNotExist, "missing collection")
}

// TestNew_PasswordNotInErrors checks that no error of New
// contains the password of the base URL.
func TestNew_PasswordNotInErrors(t *testing.T) {
	for _, baseURL := range []string{
		"http://alice:topsecret@127.0.0.1:1/dav", // connection refused
		"ftp://alice:topsecret@example.com/dav",  // wrong scheme
		"http://alice:topsecret@[::1/dav",        // parse error
		"http://alice:topsecret@/dav",            // no host
	} {
		_, err := New(t.Context(), baseURL, nil)
		require.Error(t, err, baseURL)
		assert.NotContains(t, err.Error(), "topsecret", "password leaked in the error for %s", baseURL)
	}
}

// TestPrefix checks that a username is part of the prefix
// and that the prefix and String never contain the password.
func TestPrefix(t *testing.T) {
	srv := newStyledServer(t, styleXNet, "", false)
	host := srv.Listener.Addr().String()

	davFS, err := New(t.Context(), "http://bob%40example.com:topsecret@"+host+"/", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = davFS.Close() })
	assert.Equal(t, "webdav://bob%40example.com@"+host, davFS.Prefix())
	assert.Equal(t, "bob%40example.com@"+host, davFS.ID())
	assert.Equal(t, "WebDAV", davFS.Name())
	assert.Equal(t, "WebDAV webdav://bob%40example.com@"+host, davFS.String())

	// The same server for another user gets another prefix,
	// both can be registered at the same time
	require.NoError(t, NewAndRegister(t.Context(), srv.URL, &Options{Username: "carol", Password: "x"}))
	require.NoError(t, NewAndRegister(t.Context(), srv.URL, nil))
	carolFS := fs.GetFileSystemByPrefixOrNil("webdav://carol@" + host)
	anonFS := fs.GetFileSystemByPrefixOrNil("webdav://" + host)
	require.NotNil(t, carolFS)
	require.NotNil(t, anonFS)
	t.Cleanup(func() { _ = carolFS.Close(); _ = anonFS.Close() })

	err = NewAndRegister(t.Context(), srv.URL, &Options{Username: "carol"})
	assert.ErrorIs(t, err, fs.ErrAlreadyRegistered, "a second file system for the same prefix")
}

// TestAuthorizationHeader checks that an Authorization header
// of Options.Header is not overwritten by basic authentication.
func TestAuthorizationHeader(t *testing.T) {
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	davFS, err := New(t.Context(), srv.URL, &Options{
		Username: "alice",
		Password: "secret",
		Header:   http.Header{"Authorization": {"Bearer token"}},
	})
	require.NoError(t, err, "the bearer token must be sent instead of basic auth")
	t.Cleanup(func() { _ = davFS.Close() })
	assert.Equal(t, "webdav://alice@"+srv.Listener.Addr().String(), davFS.Prefix())
}

// TestCheckRedirect checks that a redirect that would turn a WebDAV
// method into GET is not followed and that the CheckRedirect of
// Options.Client is used for the other redirects.
func TestCheckRedirect(t *testing.T) {
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	var gets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			gets = append(gets, r.URL.Path)
		case r.URL.Path == "/moved":
			http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
			return
		case r.URL.Path == "/moved-keeping-method":
			http.Redirect(w, r, "/", http.StatusPermanentRedirect)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	var userRedirects int
	davFS, err := New(t.Context(), srv.URL, &Options{Client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			userRedirects++
			return nil
		},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = davFS.Close() })

	_, err = davFS.Stat("/moved")
	var statusErr *StatusError
	require.ErrorAs(t, err, &statusErr, "PROPFIND must not become a GET")
	assert.Equal(t, http.StatusMovedPermanently, statusErr.StatusCode)
	assert.Contains(t, err.Error(), "/elsewhere")
	assert.Empty(t, gets, "no GET request")
	assert.Zero(t, userRedirects, "the CheckRedirect of the client is not asked for refused redirects")

	// A 308 keeps the method and is passed to the CheckRedirect of the client
	info, err := davFS.Stat("/moved-keeping-method")
	require.NoError(t, err)
	assert.True(t, info.IsDir, "the redirect target is the root collection")
	assert.Equal(t, 1, userRedirects)
}

// TestMove_OntoDirectoryKeepsTree is the regression test for MOVE with
// "Overwrite: T", which deleted the destination directory tree.
func TestMove_OntoDirectoryKeepsTree(t *testing.T) {
	ctx := t.Context()
	srv := newTestServer(t, "")
	davFS := newTestFS(t, srv, "")
	root := davFS.RootDir()
	require.NoError(t, root.Join("x.txt").WriteAllString(ctx, "x"))
	require.NoError(t, root.Join("photos").MakeDir())
	require.NoError(t, root.Join("photos", "img1.jpg").WriteAllString(ctx, "img"))

	err := davFS.(fs.MoveFileSystem).Move(ctx, "/x.txt", "/photos")
	assert.ErrorIs(t, err, os.ErrExist)
	assert.True(t, root.Join("photos", "img1.jpg").Exists(), "the directory tree must be kept")
	assert.True(t, root.Join("x.txt").Exists(), "the source must be kept")

	err = davFS.(fs.CopyFileSystem).CopyFile(ctx, "/x.txt", "/photos")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.True(t, root.Join("photos", "img1.jpg").Exists(), "the directory tree must be kept")

	// A file destination is replaced
	require.NoError(t, root.Join("y.txt").WriteAllString(ctx, "y"))
	require.NoError(t, davFS.(fs.MoveFileSystem).Move(ctx, "/x.txt", "/y.txt"))
	content, err := root.Join("y.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "x", content)
}

// TestMove_IntoItself checks the pre-check, the server is never asked.
func TestMove_IntoItself(t *testing.T) {
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	require.NoError(t, davFS.RootDir().Join("dir", "sub").MakeAllDirs())

	err := davFS.(fs.MoveFileSystem).Move(t.Context(), "/dir", "/dir/sub/dir")
	assert.ErrorIs(t, err, os.ErrInvalid)
	assert.NotContains(t, srv.requests(), "MOVE /dir/")
}

// TestMove_MissingParent checks that 409 Conflict of MOVE and COPY
// reports the missing parent of the destination.
func TestMove_MissingParent(t *testing.T) {
	ctx := t.Context()
	davFS := newTestFS(t, newTestServer(t, ""), "")
	require.NoError(t, davFS.RootDir().Join("a.txt").WriteAllString(ctx, "a"))

	err := davFS.(fs.MoveFileSystem).Move(ctx, "/a.txt", "/missing/a.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.ErrorContains(t, err, "/missing")
	err = davFS.(fs.CopyFileSystem).CopyFile(ctx, "/a.txt", "/missing/a.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestCanceled checks that Move and CopyFile honour the context.
func TestCanceled(t *testing.T) {
	davFS := newTestFS(t, newTestServer(t, ""), "")
	require.NoError(t, davFS.RootDir().Join("a.txt").WriteAllString(t.Context(), "a"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, davFS.(fs.MoveFileSystem).Move(ctx, "/a.txt", "/b.txt"), context.Canceled)
	assert.ErrorIs(t, davFS.(fs.CopyFileSystem).CopyFile(ctx, "/a.txt", "/b.txt"), context.Canceled)
	assert.ErrorIs(t, davFS.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/a.txt"), context.Canceled)
	assert.True(t, davFS.RootDir().Join("a.txt").Exists())
	assert.False(t, davFS.RootDir().Join("b.txt").Exists())
}

// TestRemoveAll_Nginx is the regression test for DELETE of a collection
// without trailing slash, which nginx answers with 409. That was
// mapped to os.ErrNotExist and swallowed, so nothing was deleted.
func TestRemoveAll_Nginx(t *testing.T) {
	ctx := t.Context()
	srv := newStyledServer(t, styleNginx, "", true)
	davFS := newTestFS(t, srv.Server, "")
	require.NoError(t, davFS.RootDir().Join("dir").MakeDir())
	require.NoError(t, davFS.RootDir().Join("dir", "a.txt").WriteAllString(ctx, "a"))

	require.NoError(t, davFS.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/dir"))
	assert.False(t, davFS.RootDir().Join("dir").Exists(), "the directory must be deleted")
	assert.Contains(t, srv.requests(), "DELETE /dir/")

	// A 409 is an error, not a missing file
	srv.writeMemFile(t, "/b.txt", "b")
	err := davFS.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/b.txt/")
	require.NoError(t, err, "the path is cleaned, no trailing slash is sent for a file")
	assert.False(t, davFS.RootDir().Join("b.txt").Exists())
}

// TestRemove_FileWithoutTrailingSlash is the regression test for
// PROPFIND of a file with trailing slash, which nginx and Apache
// answer with 404, so Remove of an existing file failed.
func TestRemove_FileWithoutTrailingSlash(t *testing.T) {
	for _, style := range []serverStyle{styleNginx, styleApache} {
		srv := newStyledServer(t, style, "", true)
		davFS := newTestFS(t, srv.Server, "")
		require.NoError(t, davFS.RootDir().Join("f.txt").WriteAllString(t.Context(), "x"))
		require.NoError(t, davFS.RootDir().Join("f.txt").Remove())
		assert.False(t, davFS.RootDir().Join("f.txt").Exists())
		for _, request := range srv.requests() {
			assert.NotEqual(t, "PROPFIND /f.txt/", request)
		}
	}
}

// TestRemove_NonEmptyDir checks the typed error.
func TestRemove_NonEmptyDir(t *testing.T) {
	davFS := newTestFS(t, newTestServer(t, ""), "")
	require.NoError(t, davFS.RootDir().Join("dir", "a.txt").MakeAllDirs())
	err := davFS.(fs.WriteFileSystem).Remove("/dir")
	assert.ErrorIs(t, err, fs.ErrDirectoryNotEmpty{})
	assert.True(t, davFS.RootDir().Join("dir", "a.txt").IsDir())
}

// TestStat_ApacheRedirect is the regression test for the 301 redirect
// of a collection URL without trailing slash, which Go's client followed
// as GET, so Stat of a directory failed.
func TestStat_ApacheRedirect(t *testing.T) {
	srv := newStyledServer(t, styleApache, "", true)
	davFS := newTestFS(t, srv.Server, "")
	require.NoError(t, davFS.RootDir().Join("dir").MakeDir())

	info, err := davFS.Stat("/dir")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	assert.Equal(t, davFS.RootDir().Join("dir"), info.File)
	assert.NotContains(t, srv.requests(), "GET /dir/", "the redirect must not be followed as GET")

	n := 0
	require.NoError(t, davFS.ListDir(t.Context(), "/dir", nil, func(*fs.FileInfo) error { n++; return nil }))
	assert.Zero(t, n)
}

// TestReadCollection checks that reading a collection is an
// fs.ErrIsDirectory, whether the server answers with a 405 status
// (x/net/webdav), a redirect to an HTML index (Apache) or 403 (nginx).
func TestReadCollection(t *testing.T) {
	for _, style := range []serverStyle{styleXNet, styleNginx, styleApache} {
		srv := newStyledServer(t, style, "", true)
		davFS := newTestFS(t, srv.Server, "")
		require.NoError(t, davFS.RootDir().Join("dir").MakeDir())

		_, err := davFS.(fs.ReadAllFileSystem).ReadAll(t.Context(), "/dir")
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "style %d", style)
		_, err = davFS.OpenReader("/dir")
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "style %d", style)
		err = davFS.(fs.WriteAllFileSystem).WriteAll(t.Context(), "/dir", []byte("x"), 0)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "style %d", style)
		assert.True(t, davFS.RootDir().Join("dir").IsDir())
	}
}

// TestDelete207 checks that a 207 Multi-Status answer of DELETE,
// which reports members that could not be deleted, is an error.
func TestDelete207(t *testing.T) {
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusMultiStatus)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	davFS, err := New(t.Context(), srv.URL, nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(davFS))
	t.Cleanup(func() { _ = davFS.Close() })
	require.NoError(t, davFS.RootDir().Join("dir", "locked.txt").MakeAllDirs())

	err = davFS.(fs.RemoveAllFileSystem).RemoveAll(t.Context(), "/dir")
	var statusErr *StatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusMultiStatus, statusErr.StatusCode)
}

// TestOpenWriter_Abort checks that the writer implements fs.WriteAborter
// and that Abort keeps an existing file.
func TestOpenWriter_Abort(t *testing.T) {
	ctx := t.Context()
	davFS := newTestFS(t, newTestServer(t, ""), "")
	file := davFS.RootDir().Join("keep.txt")
	require.NoError(t, file.WriteAllString(ctx, "keep"))

	w, err := davFS.(fs.WriteFileSystem).OpenWriter("/keep.txt", 0)
	require.NoError(t, err)
	aborter, ok := w.(fs.WriteAborter)
	require.True(t, ok, "the writer implements fs.WriteAborter")
	_, err = w.Write([]byte("partial"))
	require.NoError(t, err)
	require.NoError(t, aborter.Abort())
	require.NoError(t, w.Close())
	content, err := file.ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "keep", content)
}

// TestRangedRead verifies that seeking issues Range requests
// and that the reader implements fs.ReadAtSeekCloser.
func TestRangedRead(t *testing.T) {
	ctx := t.Context()
	srv := newTestServer(t, "")
	require.NoError(t, NewAndRegister(ctx, "http://alice:secret@"+srv.Listener.Addr().String(), nil))
	davFS := fs.GetFileSystemByPrefixOrNil("webdav://alice@" + srv.Listener.Addr().String())
	require.NotNil(t, davFS, "NewAndRegister must register the file system")
	t.Cleanup(func() { _ = davFS.Close() })

	file := davFS.RootDir().Join("ranged.txt")
	require.NoError(t, file.WriteAllString(ctx, "0123456789"))

	r, err := file.OpenReadSeeker()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, isRangeReader := r.(*fsimpl.RangeReader)
	assert.True(t, isRangeReader, "the native range reader must be used")

	pos, err := r.Seek(-3, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(7), pos)
	tail, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "789", string(tail))

	_, err = r.Seek(2, io.SeekStart)
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(r, buf)
	require.NoError(t, err)
	assert.Equal(t, "2345", string(buf))

	// Native MOVE and COPY
	require.NoError(t, file.MoveTo(ctx, davFS.RootDir().Join("moved.txt")))
	assert.False(t, file.Exists())
	require.NoError(t, fs.CopyFile(ctx, davFS.RootDir().Join("moved.txt"), davFS.RootDir().Join("copied.txt")))
	data, err := davFS.RootDir().Join("copied.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", data)
}

// TestRequests_TrailingSlash checks the URLs the file system sends
// for a directory and a file: collection URLs with trailing slash
// for DELETE, MKCOL, MOVE and COPY, file URLs without.
func TestRequests_TrailingSlash(t *testing.T) {
	ctx := t.Context()
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	require.NoError(t, davFS.(fs.WriteFileSystem).MakeDir("/d", 0))
	require.NoError(t, davFS.(fs.WriteAllFileSystem).WriteAll(ctx, "/f.txt", []byte("f"), 0))
	require.NoError(t, davFS.(fs.MoveFileSystem).Move(ctx, "/d", "/e"))
	require.NoError(t, davFS.(fs.MoveFileSystem).Move(ctx, "/f.txt", "/g.txt"))
	require.NoError(t, davFS.(fs.WriteFileSystem).Remove("/e"))
	require.NoError(t, davFS.(fs.WriteFileSystem).Remove("/g.txt"))

	requests := srv.requests()
	for _, want := range []string{"MKCOL /d/", "PUT /f.txt", "MOVE /d/", "MOVE /f.txt", "DELETE /e/", "DELETE /g.txt"} {
		assert.Contains(t, requests, want)
	}
	assert.False(t, slices.ContainsFunc(requests, func(r string) bool {
		return strings.HasSuffix(r, ".txt/")
	}), "no file URL with trailing slash: %v", requests)
}

// TestClosed checks that methods with an early return
// still report fs.ErrFileSystemClosed.
func TestClosed(t *testing.T) {
	davFS := newTestFS(t, newTestServer(t, ""), "")
	require.NoError(t, davFS.Close())
	ctx := t.Context()
	assert.ErrorIs(t, davFS.(fs.MoveFileSystem).Move(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, davFS.(fs.CopyFileSystem).CopyFile(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, davFS.(fs.WriteFileSystem).MakeDir("/", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, davFS.(fs.RemoveAllFileSystem).RemoveAll(ctx, "/"), fs.ErrFileSystemClosed)
	assert.True(t, errors.Is(davFS.(fs.WriteFileSystem).Remove("/"), fs.ErrFileSystemClosed))
}
