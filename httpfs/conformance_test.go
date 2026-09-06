package httpfs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
	// Serve the seed tree from a temporary directory
	root := t.TempDir()
	for name, content := range fstest.DefaultSeed() {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	addr := strings.TrimPrefix(server.URL, "http://")

	fstest.RunConformance(t, FileSystem, fstest.Config{
		Name:          "HTTP",
		Prefix:        "http://",
		TestDir:       addr,
		NoDirectories: true,
		SkipClose:     true, // FileSystem is a package level singleton
	})
}
