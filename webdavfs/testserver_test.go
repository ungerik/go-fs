package webdavfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/webdav"
)

// serverStyle selects the quirks of a real WebDAV server
// that the test server adds to the golang.org/x/net/webdav handler.
type serverStyle int

const (
	// styleXNet is the golang.org/x/net/webdav handler as it is.
	styleXNet serverStyle = iota

	// styleNginx behaves like the nginx dav module: DELETE, MKCOL, PUT,
	// MOVE and COPY answer a collection URL without trailing slash,
	// a file URL with trailing slash and a slash mismatch between
	// the URL and the Destination of MOVE or COPY with 409 Conflict,
	// GET and PROPFIND answer a file URL with trailing slash with 404,
	// GET redirects a collection URL without trailing slash with 301
	// and answers it with trailing slash with 403.
	styleNginx

	// styleApache behaves like Apache mod_dav with mod_dir: every
	// method for a collection URL without trailing slash is redirected
	// with 301, every method for a file URL with trailing slash is 404,
	// GET of a collection returns an HTML index and PUT to it is 405.
	styleApache
)

// testServer is an in-process WebDAV server on a memory file system.
type testServer struct {
	*httptest.Server

	mem webdav.FileSystem

	mtx     sync.Mutex
	methods []string                // "METHOD path" of every request
	refuse  func(*http.Request) int // if not nil, a status other than 0 it returns answers the request
}

// setRefuse sets the refuse hook of the server.
func (s *testServer) setRefuse(refuse func(*http.Request) int) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.refuse = refuse
}

// requests returns the "METHOD path" of every request so far.
func (s *testServer) requests() []string {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return append([]string(nil), s.methods...)
}

// newTestServer starts an in-process WebDAV server on a memory file
// system below the URL path prefix, protected by basic authentication.
func newTestServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	return newStyledServer(t, styleXNet, prefix, true).Server
}

// newStyledServer starts an in-process WebDAV server on a memory file system
// below the URL path prefix with the quirks of style. If auth is true it
// requires basic authentication with the user alice and the password secret.
func newStyledServer(t *testing.T, style serverStyle, prefix string, auth bool) *testServer {
	t.Helper()
	return newStyledServerOn(t, webdav.NewMemFS(), style, prefix, auth)
}

// newStyledServerOn starts an in-process WebDAV server like newStyledServer
// on the file system mem.
func newStyledServerOn(t *testing.T, mem webdav.FileSystem, style serverStyle, prefix string, auth bool) *testServer {
	t.Helper()
	srv := &testServer{mem: mem}
	handler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: srv.mem,
		LockSystem: webdav.NewMemLS(),
	}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.mtx.Lock()
		srv.methods = append(srv.methods, r.Method+" "+r.URL.Path)
		refuse := srv.refuse
		srv.mtx.Unlock()
		if auth {
			if user, pass, ok := r.BasicAuth(); !ok || user != "alice" || pass != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		if refuse != nil {
			if status := refuse(r); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		if !srv.quirk(style, prefix, w, r) {
			handler.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// quirk answers a request like the server of style would if that
// differs from golang.org/x/net/webdav and reports whether it did,
// else the handler serves the request.
func (s *testServer) quirk(style serverStyle, prefix string, w http.ResponseWriter, r *http.Request) bool {
	status := s.quirkStatus(style, prefix, w, r)
	switch status {
	case 0:
		return false
	case http.StatusOK, http.StatusMovedPermanently:
		// Written by quirkStatus
	default:
		w.WriteHeader(status)
	}
	return true
}

// quirkStatus returns the status of quirk, or 0 for no quirk.
// It writes the response for the statuses 200 and 301.
func (s *testServer) quirkStatus(style serverStyle, prefix string, w http.ResponseWriter, r *http.Request) int {
	urlPath := r.URL.Path
	memPath := strings.TrimPrefix(urlPath, prefix)
	if memPath == "" || memPath == "/" {
		return 0 // the root
	}
	slash := strings.HasSuffix(urlPath, "/")
	info, err := s.mem.Stat(r.Context(), memPath)
	exists := err == nil
	isDir := exists && info.IsDir()
	isFile := exists && !info.IsDir()

	switch style {
	case styleNginx:
		switch r.Method {
		case http.MethodGet, http.MethodHead, "PROPFIND":
			switch {
			case isFile && slash:
				return http.StatusNotFound
			case isDir && !slash && r.Method != "PROPFIND":
				http.Redirect(w, r, urlPath+"/", http.StatusMovedPermanently)
				return http.StatusMovedPermanently
			case isDir && r.Method != "PROPFIND":
				return http.StatusForbidden
			}
		case http.MethodDelete, http.MethodPut:
			if (isDir && !slash) || (isFile && slash) || (r.Method == http.MethodPut && slash) {
				return http.StatusConflict
			}
		case "MKCOL":
			if !slash || isFile {
				return http.StatusConflict
			}
		case "MOVE", "COPY":
			destSlash := strings.HasSuffix(r.Header.Get("Destination"), "/")
			if (isDir && !slash) || (isFile && slash) || slash != destSlash {
				return http.StatusConflict
			}
		}
	case styleApache:
		switch {
		case isDir && !slash:
			http.Redirect(w, r, urlPath+"/", http.StatusMovedPermanently)
			return http.StatusMovedPermanently
		case isFile && slash:
			return http.StatusNotFound
		case isDir && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>Index of " + urlPath + "</body></html>"))
			return http.StatusOK
		case isDir && r.Method == http.MethodPut:
			return http.StatusMethodNotAllowed
		}
	}
	return 0
}

// writeMemFile writes a file directly to the memory file system of the server.
func (s *testServer) writeMemFile(t *testing.T, name, content string) {
	t.Helper()
	f, err := s.mem.OpenFile(t.Context(), name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// caseInsensitiveFS makes a webdav.FileSystem case-insensitive like the
// default file systems of macOS and Windows: a name matches an existing
// entry regardless of case, a new entry gets the case of its name.
type caseInsensitiveFS struct {
	webdav.FileSystem
}

// resolve returns name with every component that matches
// an existing entry regardless of case replaced by its name.
func (c caseInsensitiveFS) resolve(ctx context.Context, name string) string {
	resolved := "/"
	for part := range strings.SplitSeq(strings.Trim(name, "/"), "/") {
		if part == "" {
			continue
		}
		next := path.Join(resolved, part)
		if dir, err := c.FileSystem.OpenFile(ctx, resolved, os.O_RDONLY, 0); err == nil {
			infos, _ := dir.Readdir(-1)
			_ = dir.Close()
			for _, info := range infos {
				if strings.EqualFold(info.Name(), part) {
					next = path.Join(resolved, info.Name())
					break
				}
			}
		}
		resolved = next
	}
	return resolved
}

func (c caseInsensitiveFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return c.FileSystem.Mkdir(ctx, c.resolve(ctx, name), perm)
}

func (c caseInsensitiveFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return c.FileSystem.OpenFile(ctx, c.resolve(ctx, name), flag, perm)
}

func (c caseInsensitiveFS) RemoveAll(ctx context.Context, name string) error {
	return c.FileSystem.RemoveAll(ctx, c.resolve(ctx, name))
}

func (c caseInsensitiveFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return c.FileSystem.Stat(ctx, c.resolve(ctx, name))
}

// Rename onto the old name in another case changes the case of the entry.
func (c caseInsensitiveFS) Rename(ctx context.Context, oldName, newName string) error {
	oldName = c.resolve(ctx, oldName)
	resolved := c.resolve(ctx, newName)
	if resolved == oldName {
		resolved = path.Join(path.Dir(resolved), path.Base(newName))
	}
	return c.FileSystem.Rename(ctx, oldName, resolved)
}

// caseInsensitiveTempDir returns a new temporary directory
// on a case-insensitive file system or skips the test.
func caseInsensitiveTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "Probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "probe")); err != nil {
		t.Skip("the temporary directory is on a case-sensitive file system")
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	return dir
}
