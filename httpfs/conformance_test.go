package httpfs

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
	// Serve the seed tree from a temporary directory. The seed names are
	// paths of URLs, so the server decodes them to the names of the files.
	root := t.TempDir()
	for name, content := range fstest.DefaultSeed() {
		name, err := url.PathUnescape(name)
		if err != nil {
			t.Fatal(err)
		}
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

	fstest.RunConformance(t, Default, fstest.Config{
		Name:          "HTTP",
		Prefix:        "http://",
		TestDir:       addr,
		NoDirectories: true,
		SkipClose:     true, // Default is a package level singleton
	})
}
