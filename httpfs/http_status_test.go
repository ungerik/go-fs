package httpfs

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// newStatusTestServer returns an httptest server whose routes exercise the
// different ways info() must classify an HTTP response, plus the address of
// the server with the "http://" scheme stripped so it can be used as a
// filePath for the http:// file system.
func newStatusTestServer(t *testing.T) (addr string) {
	t.Helper()

	mux := http.NewServeMux()

	// 200 with a Content-Length: definitely exists.
	mux.HandleFunc("/exists.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, "hello")
		}
	})

	// 404: definitely does not exist.
	mux.HandleFunc("/missing.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})

	// 404 with a non-empty body and Content-Length. The old code looked only
	// at Content-Length and reported Exists:true; the status must win.
	mux.HandleFunc("/missing-with-body.txt", func(w http.ResponseWriter, r *http.Request) {
		body := "<html>404 not found</html>"
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, body)
		}
	})

	// 500: existence is unknown, must surface as an error (not "does not exist").
	mux.HandleFunc("/servererror.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	// 403: existence is unknown, must surface as an error (not "does not exist").
	mux.HandleFunc("/forbidden.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})

	// Server that does not support HEAD (405) but answers GET: info() must fall
	// back to GET and report the file as existing.
	mux.HandleFunc("/no-head.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Length", "5")
		_, _ = io.WriteString(w, "hello")
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return strings.TrimPrefix(server.URL, Prefix)
}

func TestInfoStatusHandling(t *testing.T) {
	addr := newStatusTestServer(t)

	t.Run("Exists200", func(t *testing.T) {
		path := addr + "/exists.txt"
		assert.True(t, FileSystem.Exists(path), "200 response should exist")

		info, err := FileSystem.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, int64(5), info.Size())
	})

	t.Run("Missing404", func(t *testing.T) {
		path := addr + "/missing.txt"
		assert.False(t, FileSystem.Exists(path), "404 response should not exist")

		_, err := FileSystem.Stat(path)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist, "404 must map to ErrDoesNotExist")
	})

	t.Run("Missing404WithBody", func(t *testing.T) {
		path := addr + "/missing-with-body.txt"
		// The key regression: a 404 with a body must report not-exist so that
		// Exists() and OpenReader() agree.
		assert.False(t, FileSystem.Exists(path), "404 with body should not exist")

		_, err := FileSystem.Stat(path)
		assert.ErrorIs(t, err, os.ErrNotExist)

		_, err = FileSystem.OpenReader(path)
		require.Error(t, err, "OpenReader must also fail for a 404")
	})

	t.Run("ServerError500", func(t *testing.T) {
		path := addr + "/servererror.txt"
		// A 500 means existence is unknown: it must NOT be reported as
		// "does not exist", otherwise a flaky server makes files vanish.
		_, err := FileSystem.Stat(path)
		require.Error(t, err)
		assert.NotErrorIs(t, err, os.ErrNotExist, "5xx must not map to ErrDoesNotExist")
	})

	t.Run("Forbidden403", func(t *testing.T) {
		path := addr + "/forbidden.txt"
		_, err := FileSystem.Stat(path)
		require.Error(t, err)
		assert.NotErrorIs(t, err, os.ErrNotExist, "403 must not map to ErrDoesNotExist")
	})

	t.Run("HeadNotAllowedFallsBackToGet", func(t *testing.T) {
		path := addr + "/no-head.txt"
		assert.True(t, FileSystem.Exists(path), "should fall back to GET when HEAD is rejected")

		info, err := FileSystem.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, int64(5), info.Size())
	})
}

// TestInfoTransportErrorNotMissing verifies that a transport failure (here a
// connection refused to a closed server) is reported as an error rather than
// as "file does not exist".
func TestInfoTransportErrorNotMissing(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	addr := strings.TrimPrefix(server.URL, Prefix)
	server.Close() // nothing is listening anymore

	assert.False(t, FileSystem.Exists(addr+"/whatever.txt"))

	_, err := FileSystem.Stat(addr + "/whatever.txt")
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist, "a transport error must not look like a missing file")
	// Sanity: it is a real transport error, not our sentinel.
	assert.False(t, errors.Is(err, fs.ErrFileSystemClosed))
}
