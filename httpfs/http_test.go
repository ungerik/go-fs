package httpfs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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

	info, err := Default.Stat(addr + "/README.md")
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

	data, err := Default.ReadAll(t.Context(), addr+"/README.md")
	require.NoError(t, err)
	assert.Equal(t, testFileContent, string(data))

	data2, err := fs.File("http://" + addr + "/README.md").ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, data, data2)

	r, err := fs.File("http://" + addr + "/README.md").OpenReader()
	require.NoError(t, err)
	data3, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	assert.Equal(t, data, data3)
}

// recordingServer is a test server that records the
// request targets it receives exactly as sent.
type recordingServer struct {
	*httptest.Server

	mtx      sync.Mutex
	requests []string // method, host and request target
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	s := &recordingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mtx.Lock()
		s.requests = append(s.requests, r.Method+" "+r.Host+" "+r.RequestURI)
		s.mtx.Unlock()
		w.Header().Set("Content-Length", "2")
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// take returns and resets the recorded requests.
func (s *recordingServer) take() []string {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	requests := s.requests
	s.requests = nil
	return requests
}

// host returns the host of the server URL.
func (s *recordingServer) host() string {
	return strings.TrimPrefix(s.URL, Prefix)
}

// TestURLsVerbatim verifies that URLs are requested exactly as passed.
// The path of a URI used to be URL decoded and cleaned as a whole, so
// "?q=a%26b" arrived as "q=a&b", "?to=https://x" as "to=https:/x",
// "/file%3Fname" as path "/file" with the query "name", "/a%2Fb" as
// "/a/b", and the trailing slash of "/dir/" was lost. Name included
// the query.
func TestURLsVerbatim(t *testing.T) {
	server := newRecordingServer(t)
	host := server.host()
	presigned := "/report.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAEXAMPLE%2F20261021%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Signature=ab%2Bcd%3D"

	tests := []struct {
		target   string // appended to the server URL
		received string // request target the server must receive
		uri      string // File.URL, the cleaned target
		name     string
	}{
		{"/search?q=a%26b", "/search?q=a%26b", "", "search"},
		{"/redirect?to=https://other.example//x", "/redirect?to=https://other.example//x", "", "redirect"},
		{"/file%3Fname.txt", "/file%3Fname.txt", "", "file%3Fname.txt"},
		{"/a%2Fb.txt", "/a%2Fb.txt", "", "a%2Fb.txt"},
		{"/dir/", "/dir/", "", "dir"},
		{presigned, presigned, "", "report.pdf"},
		{"/page.html#section", "/page.html", "", "page.html"},
		{"/a/./b/../c.txt?x=../y//z", "/a/c.txt?x=../y//z", "/a/c.txt?x=../y//z", "c.txt"},
		{"//double//slash.txt", "/double/slash.txt", "/double/slash.txt", "slash.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			uri := server.URL + tt.target
			wantURI := uri
			if tt.uri != "" {
				wantURI = server.URL + tt.uri
			}
			file := fs.File(uri)
			assert.Same(t, Default, file.FileSystem())
			assert.Equal(t, wantURI, file.URL(), "URL")
			assert.Equal(t, strings.TrimPrefix(wantURI, Prefix), file.Path(), "Path")
			assert.Equal(t, tt.name, file.Name(), "Name without query")
			assert.False(t, file.IsHidden())

			data, err := file.ReadAll(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "ok", string(data))
			info, err := file.Stat()
			require.NoError(t, err)
			assert.Equal(t, tt.name, info.Name(), "Stat Name without query")
			r, err := file.OpenReader()
			require.NoError(t, err)
			require.NoError(t, r.Close())

			assert.Equal(t, []string{
				"GET " + host + " " + tt.received,
				"HEAD " + host + " " + tt.received,
				"GET " + host + " " + tt.received,
			}, server.take(), "requests received by the server")
		})
	}
}

// TestJoinStaysOnHost verifies that the host is treated like a volume.
// File("http://h/files").Join("..", "..", "evil.example", "x") used to
// be "http://evil.example/x", a request to another host (SSRF).
func TestJoinStaysOnHost(t *testing.T) {
	server := newRecordingServer(t)
	host := server.host()

	joined := fs.File(server.URL+"/files").Join("..", "..", "evil.example", "x")
	assert.Equal(t, fs.File(server.URL+"/evil.example/x"), joined)
	_, err := joined.ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"GET " + host + " /evil.example/x"}, server.take())

	assert.Equal(t, fs.File(server.URL), fs.File(server.URL+"/a/b").Join("..", "..", ".."), "no element above the host")
	assert.Equal(t, fs.File(server.URL+"/a/b"), fs.File(server.URL+"/a?q=1#frag").Join("b"), "Join drops the query and fragment")
	assert.Equal(t, fs.File(server.URL+"/a/b%3Fc%23d"), fs.File(server.URL+"/a").Join("b?c#d"), "a joined part is a path element")
	assert.Equal(t, fs.File(server.URL+"/a"), fs.File(server.URL+"/a/b?q=1").Dir(), "Dir drops the query")
	assert.Equal(t, fs.File(server.URL), fs.File(server.URL+"/a").Dir(), "the host is the parent of its elements")
}

// TestHostOnly verifies that the URL of a host without path is the root
// of the host. Its name used to be "." and so it was hidden.
func TestHostOnly(t *testing.T) {
	server := newRecordingServer(t)
	for _, uri := range []string{server.URL, server.URL + "/"} {
		file := fs.File(uri)
		assert.Equal(t, "", file.Name(), "Name of %s", uri)
		assert.False(t, file.IsHidden(), "IsHidden of %s", uri)
		assert.Equal(t, file, file.Dir(), "the host is its own parent")
		assert.Equal(t, uri, file.URL(), "the trailing slash is kept")

		info, err := file.Stat()
		require.NoError(t, err)
		assert.Equal(t, server.host(), info.Name(), "the name in the FileInfo is the host")
		assert.Equal(t, int64(2), info.Size())
		assert.False(t, file.Info().IsHidden)
	}
}

// TestPaths checks CleanPath and SplitDirAndName directly.
func TestPaths(t *testing.T) {
	custom, err := New("https://api.example.com/v1/", nil)
	require.NoError(t, err)
	noSlash, err := New("https://api.example.com/v1", nil)
	require.NoError(t, err)

	cleanTests := []struct {
		fs   *FileSystem
		in   []string
		want string
	}{
		{Default, []string{"http://h/a/../b?x=..%2F#f"}, "h/b?x=..%2F#f"},
		{Default, []string{"h"}, "h"},
		{Default, []string{"h/"}, "h/"},
		{Default, []string{"h/a/"}, "h/a/"},
		{Default, []string{"h/a", "..", "..", "evil.example"}, "h/evil.example"},
		{Default, []string{"user:pw@h:8080/../x"}, "user:pw@h:8080/x"},
		{custom, []string{"https://api.example.com/v1/users/../items?id=1"}, "items?id=1"},
		{custom, []string{"users", "..", "..", "..", "x"}, "x"},
		{custom, []string{"https://api.example.com/v1/"}, ""},
		{noSlash, []string{"https://api.example.com/v1/x/"}, "/x/"},
		{noSlash, []string{"/x", "..", ".."}, ""},
		{noSlash, []string{"/"}, "/"},
		{custom, []string{"a/../"}, ""},
		{Default, []string{"h/a/b/../../.."}, "h"},
	}
	for _, tt := range cleanTests {
		got := tt.fs.CleanPath(tt.in...)
		assert.Equal(t, tt.want, got, "%s CleanPath(%q)", tt.fs.Prefix(), tt.in)
		assert.Equal(t, got, tt.fs.CleanPath(got), "CleanPath must be idempotent for %q", got)
		assert.Equal(t, got, tt.fs.PathFromURI(tt.fs.URIFromPath(got)), "PathFromURI(URIFromPath(%q))", got)
	}

	splitTests := []struct {
		fs            *FileSystem
		in, dir, name string
	}{
		{Default, "h", "h", ""},
		{Default, "h/", "h/", ""},
		{Default, "h/a", "h", "a"},
		{Default, "h/a/", "h", "a"},
		{Default, "h/a/b?q=/c", "h/a", "b"},
		{Default, "h?q=1", "h", ""},
		{custom, "", "", ""},
		{custom, "a", "", "a"},
		{custom, "a/b#x", "a", "b"},
		{noSlash, "/a", "/", "a"},
	}
	for _, tt := range splitTests {
		dir, name := tt.fs.SplitDirAndName(tt.in)
		assert.Equal(t, tt.dir, dir, "%s dir of %q", tt.fs.Prefix(), tt.in)
		assert.Equal(t, tt.name, name, "%s name of %q", tt.fs.Prefix(), tt.in)
	}
}

// TestPrefixWithHost verifies a registered file system for a more
// specific prefix: its paths are relative to the prefix and can't leave it.
func TestPrefixWithHost(t *testing.T) {
	server := newRecordingServer(t)
	prefix := server.URL + "/api/"
	apiFS, err := New(prefix, nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(apiFS))
	t.Cleanup(func() { fs.Unregister(apiFS) })

	file := fs.File(prefix + "v1/items?id=1")
	assert.Same(t, apiFS, file.FileSystem())
	assert.Equal(t, "v1/items?id=1", file.Path())
	assert.Equal(t, fs.File(prefix+"x"), file.Join("..", "..", "..", "x"), "Join can't leave the prefix")
	assert.Equal(t, fs.File(prefix+"v1"), file.Dir())
	assert.Equal(t, fs.File(prefix), file.Dir().Dir())

	_, err = file.ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"GET " + server.host() + " /api/v1/items?id=1"}, server.take())

	assert.Equal(t, "HTTP", apiFS.Name())
	assert.Equal(t, "HTTP "+prefix, apiFS.String())
}

func TestNew(t *testing.T) {
	_, err := New("ftp://example.com/", nil)
	assert.Error(t, err, "not an HTTP prefix")
	_, err = New("https://example.com/?key=x", nil)
	assert.Error(t, err, "a prefix with query")

	tls, err := New("https://user:secret@api.example.com/", &Options{Client: &http.Client{}})
	require.NoError(t, err)
	assert.Equal(t, "HTTPS", tls.Name())
	assert.Equal(t, "HTTPS https://user:xxxxx@api.example.com/", tls.String(), "the password is redacted")
	assert.Equal(t, "HTTP", Default.Name())
	assert.Equal(t, "HTTPS", DefaultTLS.Name())
	assert.Equal(t, "HTTP http://", Default.String())
}

// TestStatRangeRequest verifies that a HEAD response without Content-Length
// is followed by a GET for the first byte, whose Content-Range has the size.
// The whole body of a GET used to be read to count its bytes.
func TestStatRangeRequest(t *testing.T) {
	var (
		mtx    sync.Mutex
		ranges []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/big.bin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.(http.Flusher).Flush() // a response without Content-Length
			return
		}
		mtx.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mtx.Unlock()
		w.Header().Set("Content-Range", "bytes 0-0/123456789")
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	})
	mux.HandleFunc("/unknown-length.bin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.(http.Flusher).Flush()
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/*")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	})
	mux.HandleFunc("/empty.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.(http.Flusher).Flush()
			return
		}
		w.Header().Set("Content-Range", "bytes */0")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	})
	mux.HandleFunc("/gone.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.Error(w, "no HEAD", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "gone", http.StatusGone)
	})
	mux.HandleFunc("/broken.txt", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.Error(w, "no HEAD", http.StatusNotImplemented)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	addr := strings.TrimPrefix(server.URL, Prefix)

	info, err := Default.Stat(addr + "/big.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(123456789), info.Size, "size from Content-Range")
	assert.Equal(t, time.Date(2015, 10, 21, 7, 28, 0, 0, time.UTC), info.Modified.UTC())
	assert.Equal(t, []string{"bytes=0-0"}, ranges, "only the first byte is requested")

	info, err = Default.Stat(addr + "/unknown-length.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(-1), info.Size, "an unknown size")

	info, err = Default.Stat(addr + "/empty.txt")
	require.NoError(t, err)
	assert.Zero(t, info.Size, "416 for the first byte of an empty file")

	_, err = Default.Stat(addr + "/gone.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "410 of the range request")
	exists, err := Default.Exists(addr + "/gone.txt")
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = Default.Stat(addr + "/broken.txt")
	require.Error(t, err)
	assert.NotErrorIs(t, err, os.ErrNotExist, "500 of the range request")
}

// TestStatIgnoredRange verifies that Stat doesn't download the content
// when a server ignores the Range header and streams the whole body
// without Content-Length. The body below never ends, so Stat would
// hang if it read the body.
func TestStatIgnoredRange(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			http.Error(w, "no HEAD", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write(make([]byte, 64*1024))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	addr := strings.TrimPrefix(server.URL, Prefix)

	type result struct {
		info *fs.FileInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := Default.Stat(addr + "/stream")
		done <- result{info, err}
	}()
	select {
	case res := <-done:
		require.NoError(t, res.err)
		assert.Equal(t, int64(-1), res.info.Size, "the size is unknown")
		assert.True(t, res.info.Exists)
	case <-time.After(10 * time.Second):
		t.Fatal("Stat reads the whole body of a GET")
	}
}

// TestErrorsRedacted verifies that status errors don't leak the query,
// which can hold the signature of a presigned URL, or a password.
func TestErrorsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	uri := strings.Replace(server.URL, Prefix, Prefix+"user:topsecret@", 1) + "/key?X-Amz-Signature=topsecret"

	_, err := fs.File(uri).Stat()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "topsecret")
	assert.Contains(t, err.Error(), "403")

	_, err = fs.File(uri).ReadAll(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "topsecret")
}

// TestFile_ExtTrimExt_query checks that the extension of an HTTP file
// is the one of the name before the query string.
func TestFile_ExtTrimExt_query(t *testing.T) {
	file := fs.File("https://example.com/dir/report.pdf?token=a.b")
	assert.Equal(t, ".pdf", file.Ext())
	assert.Equal(t, fs.File("https://example.com/dir/report?token=a.b"), file.TrimExt())
	assert.Equal(t, fs.File("https://example.com/dir/report"), fs.File("https://example.com/dir/report").TrimExt())
}

// TestErrorMessage_noQuery checks that error messages of HTTP files
// don't contain the query, which can carry presigned URL signatures.
func TestErrorMessage_noQuery(t *testing.T) {
	err := fs.NewErrDoesNotExist(fs.File("https://example.com/key.txt?X-Amz-Signature=secret"))
	assert.NotContains(t, err.Error(), "secret")
	assert.Contains(t, err.Error(), "https://example.com/key.txt")
}
