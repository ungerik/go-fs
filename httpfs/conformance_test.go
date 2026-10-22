package httpfs

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

func TestConformance(t *testing.T) {
	addr := serveSeed(t, "")
	fstest.RunConformance(t, Default, fstest.Config{
		Name:          "HTTP",
		Prefix:        "http://",
		TestDir:       addr,
		NoDirectories: true,
		SkipClose:     true, // Default is a package level singleton
	})
}

// TestConformance_New runs the suite against a file system of New for the
// host of the test server, including the Close checks that the package
// level Default skips: Close unregisters it and every method returns
// fs.ErrFileSystemClosed afterwards.
func TestConformance_New(t *testing.T) {
	addr := serveSeed(t, "testdata")
	f, err := New("http://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Register(f); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	fstest.RunConformance(t, f, fstest.Config{
		Name:          "HTTP",
		Prefix:        "http://" + addr + "/",
		TestDir:       "/testdata",
		NoDirectories: true,
	})
}

// serveSeed serves the seed tree in the directory dir of a temporary
// directory and returns the host and port of the server. The seed names
// are paths of URLs, so the server decodes them to the names of the files.
func serveSeed(t *testing.T, dir string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range fstest.DefaultSeed() {
		name, err := url.PathUnescape(name)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}
