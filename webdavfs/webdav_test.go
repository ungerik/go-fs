package webdavfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// newTestServer starts an in-process WebDAV server on a memory file
// system below the URL path prefix, protected by basic authentication.
func newTestServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "alice" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConformance(t *testing.T) {
	srv := newTestServer(t, "/dav")
	davFS, err := NewAndRegister(t.Context(), srv.URL+"/dav", &Options{Username: "alice", Password: "secret"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = davFS.Close() })
	require.Equal(t, "webdav://"+srv.Listener.Addr().String()+"/dav", davFS.Prefix())

	require.NoError(t, davFS.(fs.WriteFileSystem).MakeDir("/conformance", 0))
	fstest.RunConformance(t, davFS, fstest.Config{
		Name:    "WebDAV file system",
		Prefix:  davFS.Prefix(),
		TestDir: "/conformance",
	})
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

// TestRangedRead verifies that seeking issues Range requests
// and that the reader implements fs.ReadSeekCloser.
func TestRangedRead(t *testing.T) {
	ctx := t.Context()
	srv := newTestServer(t, "")
	davFS, err := NewAndRegister(ctx, "http://alice:secret@"+srv.Listener.Addr().String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = davFS.Close() })

	file := davFS.RootDir().Join("ranged.txt")
	require.NoError(t, file.WriteAllString(ctx, "0123456789"))

	r, err := file.OpenReadSeeker()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, isRangeReader := r.(*rangeReader)
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
