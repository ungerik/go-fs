package webdavfs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		// url.Parse takes the username for the scheme of a password with "://"
		// and a URL without "//" for an opaque one without user info, whose
		// errors repeated the whole password
		"alice:topsecret://x@example.com/dav",  // wrong scheme
		"http:alice:topsecret@example.com/dav", // no host
	} {
		_, err := New(t.Context(), baseURL, nil)
		require.Error(t, err, baseURL)
		assert.NotContains(t, err.Error(), "topsecret", "password leaked in the error for %s", baseURL)
	}
}

// TestNew_NotHTTP is a regression test: the error of a base URL without the
// scheme http or https named the URL redacted by fsimpl.RedactAddress,
// which takes the text before "://" for a scheme. The username alice with
// the password "//S3cret" of "alice://S3cret@host/dav" without scheme was
// taken for the scheme alice and the username S3cret, so the error
// contained the password. Such a URL is redacted as one without scheme,
// also if it has an '@' after the host or url.Parse rejects it, which
// were checked before the scheme.
func TestNew_NotHTTP(t *testing.T) {
	for baseURL, redacted := range map[string]string{
		"alice://S3cret@host/dav":      "alice:xxxxx@host/dav",
		"alice://@S3cret@host/dav":     "alice:xxxxx@host/dav",
		"a1.b+c-d://S3cret@host/dav":   "a1.b+c-d:xxxxx@host/dav",
		"alice://S3cret:x/y@host/dav":  "alice:xxxxx@host/dav",
		"alice://S3cr%zzet@host/dav":   "alice:xxxxx@host/dav",
		"alice://host:1/S3cret@host":   "alice:xxxxx@host",
		"ftp://u:S3cret@host/dav":      "ftp:xxxxx@host/dav",
		"alice:S3c://ret@host/dav":     "alice:xxxxx@host/dav",
		"alice:S3cret@host/dav":        "alice:xxxxx@host/dav",
		"alice://S3cret@host/dav/a@b":  "alice:xxxxx@b",
		"alice://host/dav/S3cret@host": "alice:xxxxx@host",
	} {
		_, err := New(t.Context(), baseURL, nil)
		require.Error(t, err, baseURL)
		assert.Equal(t, "webdavfs: base URL must use http or https: "+redacted, err.Error(), baseURL)
	}
}

// TestNew_SpecialCharactersInPassword is a regression test: url.Parse ends
// the user info of the base URL at an unencoded '/', '?' or '#' of a password.
// It then rejected the rest of the password as port and New repeated it in
// its error, like "webdavfs: invalid base URL: invalid port \":s3cretpart\"
// after host", or url.Parse took a password of digits for the port of the
// host "alice" and New sent a request there with the rest of the password
// in the path, which the error contained too.
func TestNew_SpecialCharactersInPassword(t *testing.T) {
	var dialed atomic.Int64
	opts := &Options{Client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.Add(1)
			return nil, errors.New("must not dial " + addr)
		},
	}}}
	for baseURL, secrets := range map[string][]string{
		"http://alice:s3cretpart/r3st@127.0.0.1:1/dav": {"s3cretpart", "r3st"},
		"http://alice:s3cretpart?r3st@127.0.0.1:1/dav": {"s3cretpart", "r3st"},
		"http://alice:s3cretpart#r3st@127.0.0.1:1/dav": {"s3cretpart", "r3st"},
		"http://alice:1234/r3st@127.0.0.1:1/dav":       {"1234", "r3st"},
		"http://alice:1234?r3st@127.0.0.1:1/dav":       {"1234", "r3st"},
		"http://alice:1234#r3st@127.0.0.1:1/dav":       {"1234", "r3st"},
		"http://alice:/r3st@127.0.0.1:1/dav":           {"r3st"},
		// url.Parse repeats an invalid escape of the password in its error
		"http://alice:s3c%zzr3t@127.0.0.1:1/dav":  {"s3c", "%zz", "r3t"},
		"http://alice:1234/r3%zz@127.0.0.1:1/dav": {"1234", "r3", "%zz"},
	} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := New(t.Context(), baseURL, opts)
			require.Error(t, err)
			for _, secret := range secrets {
				assert.NotContains(t, err.Error(), secret, "the error must not contain the password")
			}
		})
	}

	// url.Parse takes the host from a password with an unencoded '@'
	// followed by a '/', '?' or '#' and the rest of the password for the
	// path, query or fragment. New dialed the host "et", and the prefix,
	// String and the errors contained the rest of the password.
	// An '@' after the host of a base URL with a password can't be told
	// apart from one in the password, so New rejects every such URL.
	for baseURL, secrets := range map[string][]string{
		"http://alice:s3cr@et/Pw@127.0.0.1:1/dav":                                           {"s3cr", "et/Pw"},
		"http://alice:s3cr@et?Pw@127.0.0.1:1/dav":                                           {"s3cr", "et?Pw"},
		"http://alice:s3cr@et#Pw@127.0.0.1:1/dav":                                           {"s3cr", "et#Pw"},
		"http://alice:s3cr3t@cloud.example.com:8080/remote.php/dav/files/alice@example.com": {"s3cr3t"},
		"http://alice:s3cr3t@cloud.example.com/dav?user=alice@example.com":                  {"s3cr3t"},
	} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := New(t.Context(), baseURL, opts)
			require.Error(t, err)
			for _, secret := range secrets {
				assert.NotContains(t, err.Error(), secret, "the error must not contain the password")
			}
			assert.ErrorIs(t, err, os.ErrInvalid)
			assert.ErrorContains(t, err, "Options.Password", "the error must tell how to pass the password")
		})
	}
	assert.Zero(t, dialed.Load(), "a base URL with a misplaced password must not be dialed")

	// An '@' in the path of a base URL with a username only, like the email
	// address of a Nextcloud user, or without user info and without a port
	// can't end a password, an encoded one never does
	for _, baseURL := range []string{
		"http://cloud.example.com/remote.php/dav/files/alice@example.com",
		"http://alice@cloud.example.com:8080/remote.php/dav/files/alice@example.com",
		"http://alice%40example.com@cloud.example.com/remote.php/dav/files/alice@example.com",
		"http://cloud.example.com:8080/remote.php/dav/files/alice%40example.com",
		"http://alice:secret@cloud.example.com:8080/remote.php/dav/files/alice%40example.com",
	} {
		_, err := New(t.Context(), baseURL, opts)
		require.Error(t, err, baseURL)
		assert.Contains(t, err.Error(), "must not dial cloud.example.com:", baseURL)
	}
	// Without a user info the host and port "cloud.example.com:8080" can be
	// a username with a password that starts with "8080/"
	_, err := New(t.Context(), "http://cloud.example.com:8080/remote.php/dav/files/alice@example.com", opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'@' after the host")
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

// TestCheckRedirect_Credentials is the regression test for redirects that
// sent the credentials to another server: Go repeats basic authentication
// for the same host name, also for another port and after a downgrade
// from https to http, and the Options.Header entries for every host.
func TestCheckRedirect_Credentials(t *testing.T) {
	// leak records the credentials of the requests that reach it
	var (
		mtx      sync.Mutex
		received []string
	)
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mtx.Lock()
		received = append(received, fmt.Sprintf("%s %s Authorization=%q X-Api-Key=%q",
			r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")))
		mtx.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(leak.Close)
	takeReceived := func() []string {
		mtx.Lock()
		defer mtx.Unlock()
		r := received
		received = nil
		return r
	}

	// davServer returns a WebDAV handler that requires the credentials
	// and redirects /same to / and /redirect to location, both with
	// 308 Permanent Redirect, which keeps the method
	davServer := func(location string) http.Handler {
		handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, pass, _ := r.BasicAuth(); user != "alice" || pass != "secret" || r.Header.Get("X-Api-Key") != "key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/same":
				http.Redirect(w, r, "/", http.StatusPermanentRedirect)
			case "/redirect":
				http.Redirect(w, r, location, http.StatusPermanentRedirect)
			default:
				handler.ServeHTTP(w, r)
			}
		})
	}
	newFS := func(t *testing.T, baseURL string, client *http.Client) fs.FileSystem {
		t.Helper()
		davFS, err := New(t.Context(), baseURL, &Options{
			Username: "alice",
			Password: "secret",
			Header:   http.Header{"X-Api-Key": {"key"}},
			Client:   client,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = davFS.Close() })

		// A redirect to the same server keeps the credentials
		info, err := davFS.Stat("/same")
		require.NoError(t, err)
		assert.True(t, info.IsDir)
		return davFS
	}
	requireRefused := func(t *testing.T, err error) {
		t.Helper()
		var statusErr *StatusError
		require.ErrorAs(t, err, &statusErr, "the redirect must not be followed")
		assert.Equal(t, http.StatusPermanentRedirect, statusErr.StatusCode)
		assert.Contains(t, err.Error(), "/stolen")
		assert.Empty(t, takeReceived(), "nothing must reach the other server")
	}

	t.Run("another port", func(t *testing.T) {
		srv := httptest.NewServer(davServer(leak.URL + "/stolen"))
		t.Cleanup(srv.Close)
		davFS := newFS(t, srv.URL, nil)
		_, err := davFS.Stat("/redirect")
		requireRefused(t, err)
	})

	t.Run("https to http", func(t *testing.T) {
		srv := httptest.NewTLSServer(davServer("http://example.com/stolen"))
		t.Cleanup(srv.Close)
		roots := x509.NewCertPool()
		roots.AddCert(srv.Certificate())
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			// https://example.com is the WebDAV server
			// and http://example.com the leak
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				target := srv.Listener.Addr().String()
				if addr == "example.com:80" {
					target = leak.Listener.Addr().String()
				}
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, target)
			},
		}
		t.Cleanup(transport.CloseIdleConnections)
		davFS := newFS(t, "https://example.com", &http.Client{Transport: transport})
		_, err := davFS.Stat("/redirect")
		requireRefused(t, err)
	})

	t.Run("allowed by the CheckRedirect of the client", func(t *testing.T) {
		srv := httptest.NewServer(davServer(leak.URL + "/stolen"))
		t.Cleanup(srv.Close)
		davFS := newFS(t, srv.URL, &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		})
		_, err := davFS.Stat("/redirect")
		assert.ErrorIs(t, err, os.ErrNotExist, "the other server answers 404")
		assert.Equal(t, []string{`PROPFIND /stolen Authorization="" X-Api-Key=""`}, takeReceived(),
			"the redirect is followed without the credentials")
	})
}

// Test_keepsCredentials checks which redirect targets get the credentials:
// the host and port of the base URL, also after an upgrade to https.
func Test_keepsCredentials(t *testing.T) {
	for _, check := range []struct {
		base, target string
		want         bool
	}{
		{"https://dav.example.com/files", "https://dav.example.com/other", true},
		{"https://dav.example.com", "https://DAV.example.com/", true},
		{"https://dav.example.com", "http://dav.example.com/", false},
		{"https://dav.example.com", "https://dav.example.com:8443/", false},
		{"https://dav.example.com", "https://cdn.example.com/", false},
		{"http://dav.example.com:8080", "http://dav.example.com:8080/", true},
		{"http://dav.example.com:8080", "http://dav.example.com:9090/", false},
		{"http://dav.example.com", "https://dav.example.com/", true},
	} {
		base, err := url.Parse(check.base)
		require.NoError(t, err)
		target, err := url.Parse(check.target)
		require.NoError(t, err)
		f := &fileSystem{baseURL: *base}
		assert.Equal(t, check.want, f.keepsCredentials(target), "%s -> %s", check.base, check.target)
	}
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

// listNames returns the sorted names of the files in dir.
func listNames(t *testing.T, dir fs.File) []string {
	t.Helper()
	var names []string
	require.NoError(t, dir.ListDir(t.Context(), func(f fs.File) error {
		names = append(names, f.Name())
		return nil
	}))
	slices.Sort(names)
	return names
}

// TestMoveCopy_CaseInsensitive is the regression test for MOVE and COPY onto
// a path that only differs in case from the source on a case-insensitive
// server: it answered Overwrite F with 412 Precondition Failed, because the
// destination exists, it is the source itself, and the repeated request with
// Overwrite T made it delete the destination, so the source was lost.
func TestMoveCopy_CaseInsensitive(t *testing.T) {
	for _, variant := range []struct {
		name string
		mem  func(t *testing.T) webdav.FileSystem
	}{
		{"simulated", func(*testing.T) webdav.FileSystem { return caseInsensitiveFS{webdav.NewMemFS()} }},
		{"webdav.Dir", func(t *testing.T) webdav.FileSystem { return webdav.Dir(caseInsensitiveTempDir(t)) }},
	} {
		t.Run(variant.name, func(t *testing.T) {
			ctx := t.Context()
			srv := newStyledServerOn(t, variant.mem(t), styleXNet, "", true)
			davFS := newTestFS(t, srv.Server, "")
			root := davFS.RootDir()
			require.NoError(t, root.Join("File.txt").WriteAllString(ctx, "content"))

			// Move changes the case of the name
			err := davFS.(fs.MoveFileSystem).Move(ctx, "/File.txt", "/file.txt")
			assert.Equal(t, []string{"file.txt"}, listNames(t, root))
			require.NoError(t, err)
			content, err := root.Join("file.txt").ReadAllString(ctx)
			require.NoError(t, err)
			assert.Equal(t, "content", content)

			// A copy onto the file itself changes nothing
			require.NoError(t, davFS.(fs.CopyFileSystem).CopyFile(ctx, "/file.txt", "/FILE.txt"))
			assert.Equal(t, []string{"file.txt"}, listNames(t, root))
			content, err = root.Join("file.txt").ReadAllString(ctx)
			require.NoError(t, err)
			assert.Equal(t, "content", content)

			// A collection is renamed with its members
			require.NoError(t, root.Join("Dir").MakeDir())
			require.NoError(t, root.Join("Dir", "a.txt").WriteAllString(ctx, "a"))
			require.NoError(t, davFS.(fs.MoveFileSystem).Move(ctx, "/Dir", "/dir"))
			assert.Equal(t, []string{"dir", "file.txt"}, listNames(t, root))
			assert.Equal(t, []string{"a.txt"}, listNames(t, root.Join("dir")))
		})
	}
}

// TestMoveCopy_MaybeSource checks MOVE and COPY onto existing files that are
// not the source, but could be: their path only differs in case, or they have
// the same size and modification time. They are replaced, a collection is
// kept, and no temporary name is left over.
func TestMoveCopy_MaybeSource(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	srv := newStyledServerOn(t, webdav.Dir(dir), styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	root := davFS.RootDir()
	throughTemp := func() bool {
		return slices.ContainsFunc(srv.requests(), func(r string) bool { return strings.HasPrefix(r, "MOVE /.gofs-rename-") })
	}
	moveFS, copyFS := davFS.(fs.MoveFileSystem), davFS.(fs.CopyFileSystem)
	write := func(name, content string, modified time.Time) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
		require.NoError(t, os.Chtimes(filepath.Join(dir, name), modified, modified))
	}
	modified := time.Now().Add(-time.Hour).Truncate(time.Second)

	// The same size and modification time
	write("a.txt", "aaa", modified)
	write("b.txt", "bbb", modified)
	require.NoError(t, moveFS.Move(ctx, "/a.txt", "/b.txt"))
	assert.True(t, throughTemp(), "the source is moved through a temporary name")
	content, err := root.Join("b.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "aaa", content)
	assert.Equal(t, []string{"b.txt"}, listNames(t, root))

	write("c.txt", "ccc", modified)
	require.NoError(t, copyFS.CopyFile(ctx, "/b.txt", "/c.txt"))
	content, err = root.Join("c.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "aaa", content)
	assert.Equal(t, []string{"b.txt", "c.txt"}, listNames(t, root))

	// An existing collection is never replaced
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "d1", "sub"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "d2"), 0o700))
	require.NoError(t, os.Chtimes(filepath.Join(dir, "d1"), modified, modified))
	require.NoError(t, os.Chtimes(filepath.Join(dir, "d2"), modified, modified))
	assert.ErrorIs(t, moveFS.Move(ctx, "/d1", "/d2"), os.ErrExist)
	assert.Equal(t, []string{"b.txt", "c.txt", "d1", "d2"}, listNames(t, root))
	assert.Equal(t, []string{"sub"}, listNames(t, root.Join("d1")))
}

// TestMoveCopy_MaybeSourceCaseSensitive checks MOVE and COPY onto existing
// paths that only differ in case from the source on a case-sensitive server,
// where they are other files: they are replaced, a collection is kept.
func TestMoveCopy_MaybeSourceCaseSensitive(t *testing.T) {
	ctx := t.Context()
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	root := davFS.RootDir()
	moveFS, copyFS := davFS.(fs.MoveFileSystem), davFS.(fs.CopyFileSystem)

	srv.writeMemFile(t, "/File.txt", "File")
	srv.writeMemFile(t, "/file.txt", "file")
	require.NoError(t, moveFS.Move(ctx, "/File.txt", "/file.txt"))
	assert.Equal(t, []string{"file.txt"}, listNames(t, root))
	content, err := root.Join("file.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "File", content)

	srv.writeMemFile(t, "/FILE.txt", "FILE")
	require.NoError(t, copyFS.CopyFile(ctx, "/file.txt", "/FILE.txt"))
	assert.Equal(t, []string{"FILE.txt", "file.txt"}, listNames(t, root))
	content, err = root.Join("FILE.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "File", content)

	require.NoError(t, root.Join("Dir", "a").MakeAllDirs())
	require.NoError(t, root.Join("dir").MakeDir())
	assert.ErrorIs(t, moveFS.Move(ctx, "/Dir", "/dir"), os.ErrExist)
	assert.Equal(t, []string{"Dir", "FILE.txt", "dir", "file.txt"}, listNames(t, root))
	assert.Equal(t, []string{"a"}, listNames(t, root.Join("Dir")))
}

// TestMove_MaybeSourceRestore checks that a failed rename through a
// temporary name restores the source, or tells where it is.
func TestMove_MaybeSourceRestore(t *testing.T) {
	ctx := t.Context()
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	root := davFS.RootDir()
	moveFS := davFS.(fs.MoveFileSystem)
	srv.writeMemFile(t, "/File.txt", "File")
	srv.writeMemFile(t, "/file.txt", "file")

	// The server refuses to replace the destination
	srv.setRefuse(func(r *http.Request) int {
		if r.Method == "MOVE" && r.Header.Get("Overwrite") == "T" {
			return http.StatusForbidden
		}
		return 0
	})
	require.Error(t, moveFS.Move(ctx, "/File.txt", "/file.txt"))
	assert.Equal(t, []string{"File.txt", "file.txt"}, listNames(t, root))
	content, err := root.Join("File.txt").ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "File", content, "the source is restored")

	// The server refuses every MOVE from the temporary name
	srv.setRefuse(func(r *http.Request) int {
		if r.Method == "MOVE" && strings.HasPrefix(r.URL.Path, "/.gofs-") {
			return http.StatusForbidden
		}
		return 0
	})
	err = moveFS.Move(ctx, "/File.txt", "/file.txt")
	require.Error(t, err)
	names := listNames(t, root)
	require.Len(t, names, 2)
	assert.Equal(t, "file.txt", names[1])
	assert.True(t, strings.HasPrefix(names[0], ".gofs-"), "the source is at a temporary name: %v", names)
	assert.Contains(t, err.Error(), names[0], "the error names the temporary name")
	content, err = root.Join(names[0]).ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "File", content)
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

// TestRemoveRoot checks that Remove and RemoveAll of the root fail with an
// error wrapping os.ErrInvalid, like the other file systems, without a
// request and without deleting anything.
func TestRemoveRoot(t *testing.T) {
	ctx := t.Context()
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	require.NoError(t, davFS.RootDir().Join("keep.txt").WriteAllString(ctx, "keep"))
	requestsBefore := len(srv.requests())

	for _, root := range []string{"/", "//", "/.", "/dir/.."} {
		err := davFS.(fs.WriteFileSystem).Remove(root)
		assert.ErrorIs(t, err, os.ErrInvalid, "Remove(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "Remove(%q)", root)
		err = davFS.(fs.RemoveAllFileSystem).RemoveAll(ctx, root)
		assert.ErrorIs(t, err, os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "RemoveAll(%q)", root)
	}
	assert.Len(t, srv.requests(), requestsBefore, "removing the root must not send a request")
	assert.True(t, davFS.RootDir().Join("keep.txt").Exists(), "removing the root must not delete anything")
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

// TestReadAll_Preallocates checks that ReadAll allocates the buffer for the
// Content-Length of the response at once, instead of growing it from 512
// bytes while reading, which copies the data over and over.
func TestReadAll_Preallocates(t *testing.T) {
	srv := newStyledServer(t, styleXNet, "", true)
	davFS := newTestFS(t, srv.Server, "")
	content := strings.Repeat("0123456789", 100_000)
	srv.writeMemFile(t, "/big.bin", content)

	data, err := davFS.(fs.ReadAllFileSystem).ReadAll(t.Context(), "/big.bin")
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
	assert.Equal(t, len(content)+1, cap(data), "one byte more for reading the end of the body")
}

// Test_readAll checks readAll with a size that is unknown, smaller or
// larger than the data, or too large to allocate, and a done context.
func Test_readAll(t *testing.T) {
	ctx := t.Context()
	content := []byte(strings.Repeat("x", 1000))
	for _, size := range []int64{-1, 0, 10, 1000, 2000} {
		data, err := readAll(ctx, bytes.NewReader(content), size)
		require.NoError(t, err, "size %d", size)
		assert.Equal(t, content, data, "size %d", size)
	}

	data, err := readAll(ctx, bytes.NewReader(content), 1<<40)
	require.NoError(t, err)
	assert.Equal(t, content, data)
	assert.Equal(t, maxPreallocate+1, cap(data), "the allocation is limited")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = readAll(canceled, bytes.NewReader(content), 1000)
	assert.ErrorIs(t, err, context.Canceled)
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

// TestBackslashInNames checks that paths with a backslash are rejected
// unless Options.AllowBackslashInNames is set, because a WebDAV server
// on Windows takes `..\x` for a name in the parent directory.
func TestBackslashInNames(t *testing.T) {
	srv := newStyledServer(t, styleXNet, "", true)
	f := newTestFS(t, srv.Server, "").(*fileSystem)
	ctx := t.Context()
	require.NoError(t, f.MakeDir("/dir", 0))
	require.NoError(t, f.WriteAll(ctx, "/victim.txt", []byte("keep me"), 0))
	requestsBefore := len(srv.requests())

	noCallback := func(*fs.FileInfo) error { return nil }
	for _, name := range []string{`/dir/..\victim.txt`, `/dir\x`} {
		for op, call := range map[string]func() error{
			"Stat":          func() error { _, err := f.Stat(name); return err },
			"ListDir":       func() error { return f.ListDir(ctx, name, nil, noCallback) },
			"OpenReader":    func() error { _, err := f.OpenReader(name); return err },
			"ReadAll":       func() error { _, err := f.ReadAll(ctx, name); return err },
			"WriteAll":      func() error { return f.WriteAll(ctx, name, []byte("x"), 0) },
			"OpenWriter":    func() error { _, err := f.OpenWriter(name, 0); return err },
			"MakeDir":       func() error { return f.MakeDir(name, 0) },
			"Remove":        func() error { return f.Remove(name) },
			"RemoveAll":     func() error { return f.RemoveAll(ctx, name) },
			"Move from":     func() error { return f.Move(ctx, name, "/moved.txt") },
			"Move to":       func() error { return f.Move(ctx, "/victim.txt", name) },
			"CopyFile from": func() error { return f.CopyFile(ctx, name, "/copied.txt") },
			"CopyFile to":   func() error { return f.CopyFile(ctx, "/victim.txt", name) },
		} {
			assert.ErrorIs(t, call(), os.ErrInvalid, "%s(%q)", op, name)
		}
	}
	assert.Len(t, srv.requests(), requestsBefore, "no request may reach the server")

	// Servers on Unix store a backslash as part of a name
	allowFS, err := New(ctx, srv.URL, &Options{Username: "alice", Password: "secret", AllowBackslashInNames: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = allowFS.Close() })
	allow := allowFS.(*fileSystem)
	require.NoError(t, allow.WriteAll(ctx, `/dir/a\b.txt`, []byte("unix name"), 0))
	var names []string
	require.NoError(t, allow.ListDir(ctx, "/dir", nil, func(info *fs.FileInfo) error {
		names = append(names, info.Name)
		return nil
	}))
	assert.Equal(t, []string{`a\b.txt`}, names)
	data, err := allow.ReadAll(ctx, `/dir/a\b.txt`)
	require.NoError(t, err)
	assert.Equal(t, "unix name", string(data))
}
