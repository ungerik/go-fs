package httpfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

const testFileContent = "# go-fs\n\nA unified file system for Go.\n"

// newFileTestServer serves a single file at /README.md
// and returns the server address without the "http://" scheme.
func newFileTestServer(t *testing.T) (addr string) {
	t.Helper()
	mux := http.NewServeMux()
	modTime := time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC)
	mux.HandleFunc("/README.md", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "README.md", modTime, strings.NewReader(testFileContent))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

func TestStat(t *testing.T) {
	addr := newFileTestServer(t)

	info, err := FileSystem.Stat(addr + "/README.md")
	require.NoError(t, err)
	assert.False(t, info.IsDir)
	assert.True(t, info.IsRegular)
	assert.Equal(t, int64(len(testFileContent)), info.Size)
	assert.NotZero(t, info.Modified, "has modified time")

	file := fs.File("http://" + addr + "/README.md")
	assert.Equal(t, file, info.File)
	assert.Equal(t, info, file.Info())
}

func TestReadAll(t *testing.T) {
	addr := newFileTestServer(t)

	data, err := FileSystem.ReadAll(t.Context(), addr+"/README.md")
	require.NoError(t, err)
	assert.Equal(t, testFileContent, string(data))

	data2, err := fs.File("http://" + addr + "/README.md").ReadAll()
	require.NoError(t, err)
	assert.Equal(t, data, data2)

	r, err := fs.File("http://" + addr + "/README.md").OpenReader()
	require.NoError(t, err)
	data3, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, data, data3)
}
