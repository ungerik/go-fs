package onedrivefs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ungerik/go-fs"
)

// TestItemURL checks the path-based addressing of items,
// with the names escaped and a colon that doesn't end the path.
func TestItemURL(t *testing.T) {
	f := newFileSystem("b!id", oauth2.StaticTokenSource(&oauth2.Token{}), nil)
	base := DefaultGraphURL + "/drives/b%21id"
	assert.Equal(t, base+"/root", f.itemURL("/", ""))
	assert.Equal(t, base+"/root/children", f.itemURL("/", "/children"))
	assert.Equal(t, base+"/root:/a/b.txt", f.itemURL("/a/b.txt", ""))
	assert.Equal(t, base+"/root:/a%20b/c%23d%3Ae%25%3F:/content", f.itemURL("/a b/c#d:e%?", "/content"))
}

// TestTokenOnlyForGraph checks that downloads, upload sessions and copy
// monitors get no access token: the fake rejects it there.
func TestTokenOnlyForGraph(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	large := make([]byte, testMaxSimple+testFragmentSize+1)
	require.NoError(t, f.WriteAll(ctx, "/large.bin", large, 0))
	require.NoError(t, f.CopyFile(ctx, "/large.bin", "/copy.bin"))
	r, err := f.OpenReader("/copy.bin")
	require.NoError(t, err)
	data, err := fs.ReadAll(ctx, r)
	require.NoError(t, errors.Join(err, r.Close()))
	require.NoError(t, err)
	assert.Equal(t, large, data)
	assert.Equal(t, 3, g.callCount("UploadFragment"))
	assert.Zero(t, g.leaks, "the access token must not be sent to pre-authenticated URLs")

	// A nextLink that leaves the Graph API gets no token and is an error
	g.onCall("ListChildren", func() { g.srv.URL = "http://other.example" })
	defer func(url string) { g.srv.URL = url }(g.srv.URL)
	for _, name := range []string{"a", "b", "c", "d"} {
		g.put("/list/"+name, nil)
	}
	err = f.ListDir(ctx, "/list", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorContains(t, err, "is not at the Graph API URL")
}

// TestRetry checks that throttled requests are repeated after their
// Retry-After delay or a backoff, and that the error of the last
// attempt is returned unchanged, never as a missing file.
func TestRetry(t *testing.T) {
	f, g := newTestFS(t)
	g.put("/file.txt", []byte("x"))

	g.setFailNext("GetItem", fakeFailure{status: http.StatusTooManyRequests, retryAfter: "0"}, fakeFailure{status: http.StatusServiceUnavailable})
	info, err := f.Stat("/file.txt")
	require.NoError(t, err)
	assert.Equal(t, "file.txt", info.Name)
	assert.Equal(t, 3, g.callCount("GetItem"))

	failures := make([]fakeFailure, maxRetries+1)
	for i := range failures {
		failures[i] = fakeFailure{status: http.StatusTooManyRequests, code: "activityLimitReached", retryAfter: "0"}
	}
	g.setFailNext("GetItem", failures...)
	_, err = f.Stat("/file.txt")
	assert.ErrorContains(t, err, "activityLimitReached")
	assert.Equal(t, http.StatusTooManyRequests, statusOf(err))
	assert.NotErrorIs(t, err, os.ErrNotExist)

	// Other errors are not repeated
	g.setFailNext("GetItem", fakeFailure{status: http.StatusInternalServerError})
	calls := g.callCount("GetItem")
	_, err = f.Stat("/file.txt")
	assert.Equal(t, http.StatusInternalServerError, statusOf(err))
	assert.Equal(t, calls+1, g.callCount("GetItem"))

	// The end of the context ends the wait for a long Retry-After
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	g.setFailNext("ListChildren", fakeFailure{status: http.StatusTooManyRequests, retryAfter: "60"})
	start := time.Now()
	err = f.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "the wait must end with the context, not after Retry-After")
	assert.Equal(t, 1, g.callCount("ListChildren"))
}

// TestTokenSourceError checks that a failing token source fails the
// request before it is sent, with an error that wraps the cause.
func TestTokenSourceError(t *testing.T) {
	g := newFakeGraph(t, "b!drive-id")
	errExpired := errors.New("refresh token expired")
	tokens := tokenSourceFunc(func() (*oauth2.Token, error) { return nil, errExpired })
	_, err := New(t.Context(), "me/drive", tokens, &Options{HTTPClient: g.srv.Client(), GraphURL: g.graphURL()})
	assert.ErrorIs(t, err, errExpired)
	assert.Zero(t, g.callCount("GetDrive"), "no request without a token")
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

// TestTransportErrorRedacted checks that a failed connection to the
// pre-authenticated download URL doesn't put that URL into the error,
// because its path and query grant access to the file.
func TestTransportErrorRedacted(t *testing.T) {
	f, g := newTestFS(t)
	g.put("/file.txt", []byte("x"))
	// Port 1 refuses the connection to the download URL
	g.onCall("GetContent", func() { g.srv.URL = "http://127.0.0.1:1" })
	defer func(url string) { g.srv.URL = url }(g.srv.URL)

	_, err := f.OpenReader("/file.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tempauth")
	assert.NotContains(t, err.Error(), "/download/")
	assert.Contains(t, err.Error(), "http://127.0.0.1:1/...", "the error names the host")
}
