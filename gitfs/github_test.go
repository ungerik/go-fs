package gitfs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/gitfs/internal/gittest"
)

// wrapTestServer serves the fake server of newTestServer through
// handle, which can answer a request itself by returning true.
func wrapTestServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request) bool) (*gittest.Server, *httptest.Server) {
	t.Helper()
	server, _ := newTestServer(t)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !handle(w, r) {
			server.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func TestGitHubTokenAndTruncatedTree(t *testing.T) {
	ctx := t.Context()
	var (
		mtx            sync.Mutex
		authorizations []string
	)
	server, httpServer := wrapTestServer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/raw/") {
			mtx.Lock()
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			mtx.Unlock()
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Query().Get("recursive") != "" {
			// GitHub answers a tree that is too large with the
			// entries that fit and truncated set
			tree := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": tree, "tree": []any{}, "truncated": true})
			return true
		}
		return false
	})
	gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, &Options{Token: "tok"}))

	// The truncated recursive tree is read through git, complete
	var files []string
	err := gfs.ListDirRecursive(ctx, "/extra", nil, func(info *fs.FileInfo) error {
		files = append(files, info.Name)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"c.txt", "run.sh"}, files)

	// The API still serves the other trees, and raw the content
	apiBefore := server.Requests()["api"]
	_, err = gfs.ReadAll(ctx, "/seed/hello.txt")
	require.NoError(t, err)
	assert.Greater(t, server.Requests()["api"], apiBefore)
	assert.Equal(t, 1, server.Requests()["raw"])

	mtx.Lock()
	defer mtx.Unlock()
	require.NotEmpty(t, authorizations)
	for _, authorization := range authorizations {
		assert.Equal(t, "Bearer tok", authorization)
	}
}

func TestGitHubCanceledListingKeepsAPI(t *testing.T) {
	// A listing canceled while it waits for the API is no API failure,
	// the file system keeps using the API afterwards
	var (
		mtx     sync.Mutex
		block   = true
		arrived = make(chan struct{})
	)
	server, httpServer := wrapTestServer(t, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return false
		}
		mtx.Lock()
		blocking := block
		block = false
		mtx.Unlock()
		if blocking {
			close(arrived)
			<-r.Context().Done()
			return true
		}
		return false
	})
	gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))

	ctx, cancel := context.WithCancel(t.Context())
	listed := make(chan error, 1)
	go func() {
		listed <- gfs.ListDir(ctx, "/seed", nil, func(*fs.FileInfo) error { return nil })
	}()
	<-arrived
	cancel()
	assert.ErrorIs(t, <-listed, context.Canceled)

	apiBefore, fetchBefore := server.Requests()["api"], server.Requests()["fetch"]
	var names []string
	err := gfs.ListDir(t.Context(), "/seed", nil, func(info *fs.FileInfo) error {
		names = append(names, info.Name)
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, names, 5)
	assert.Equal(t, apiBefore+2, server.Requests()["api"], "the trees of / and /seed from the API")
	assert.Equal(t, fetchBefore, server.Requests()["fetch"], "no tree or blob through git")
}
