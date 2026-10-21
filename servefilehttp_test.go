package fs

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// serveTestFS wraps a file system so that OpenReader returns
// readers that can't seek and counts the Read calls.
type serveTestFS struct {
	WriteFileSystem
	reads atomic.Int64
}

func (f *serveTestFS) OpenReader(filePath string) (io.ReadCloser, error) {
	r, err := f.WriteFileSystem.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	return &serveTestReader{ReadCloser: r, reads: &f.reads}, nil
}

// serveTestReader only has the methods Read and Close.
type serveTestReader struct {
	io.ReadCloser
	reads *atomic.Int64
}

func (r *serveTestReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.ReadCloser.Read(p)
}

// registerServeTestFS returns the root of a serveTestFS
// over a MemFileSystem with initialFiles.
func registerServeTestFS(t *testing.T, initialFiles ...MemFile) (*serveTestFS, File) {
	t.Helper()
	memFS, err := NewMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	wrapped := &serveTestFS{WriteFileSystem: memFS}
	require.NoError(t, Register(wrapped))
	t.Cleanup(func() {
		Unregister(wrapped)
		_ = memFS.Close()
	})
	return wrapped, wrapped.RootDir()
}

// serveTestFileReader is a FileReader with errors from Stat and OpenReader.
type serveTestFileReader struct {
	MemFile
	statErr error
	openErr error
}

func (f serveTestFileReader) Stat() (iofs.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.MemFile.Stat()
}

func (f serveTestFileReader) OpenReader() (ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.MemFile.OpenReader()
}

func serveTestRequest(t *testing.T, method string, file FileReader, contentType string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, "/", nil)
	for i := 0; i+1 < len(header); i += 2 {
		request.Header.Set(header[i], header[i+1])
	}
	recorder := httptest.NewRecorder()
	ServeFileHTTP(recorder, request, file, contentType)
	return recorder
}

// TestServeFileHTTP_StatusCodes is a regression test: a local directory
// was served as 200 with the directory size as Content-Length,
// and a permission error was answered with 500 instead of 403.
func TestServeFileHTTP_StatusCodes(t *testing.T) {
	dir := File(t.TempDir())
	require.NoError(t, dir.Join("a.txt").WriteAllString(t.Context(), "x"))

	tests := []struct {
		name string
		file FileReader
		want int
	}{
		{name: "local directory", file: dir, want: http.StatusNotFound},
		{name: "MemFile directory", file: NewMemFile("dir/", nil), want: http.StatusNotFound},
		{name: "missing local file", file: dir.Join("missing.txt"), want: http.StatusNotFound},
		{name: "stat permission error", file: serveTestFileReader{MemFile: NewMemFile("a.txt", nil), statErr: NewErrPermission(dir)}, want: http.StatusForbidden},
		{name: "open permission error", file: serveTestFileReader{MemFile: NewMemFile("a.txt", nil), openErr: os.ErrPermission}, want: http.StatusForbidden},
		{name: "open directory error", file: serveTestFileReader{MemFile: NewMemFile("a.txt", nil), openErr: NewErrIsDirectory(dir)}, want: http.StatusNotFound},
		{name: "other error", file: serveTestFileReader{MemFile: NewMemFile("a.txt", nil), statErr: errors.New("boom")}, want: http.StatusInternalServerError},
		{name: "local file", file: dir.Join("a.txt"), want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				response := serveTestRequest(t, method, tt.file, "")
				require.Equal(t, tt.want, response.Code, method)
			}
		})
	}

	t.Run("local file without read permission", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("file permissions are not enforced")
		}
		file := dir.Join("secret.txt")
		require.NoError(t, file.WriteAllString(t.Context(), "secret"))
		require.NoError(t, os.Chmod(file.LocalPath(), 0))
		response := serveTestRequest(t, http.MethodGet, file, "")
		require.Equal(t, http.StatusForbidden, response.Code)
		require.NotContains(t, response.Body.String(), "secret")
	})
}

// TestServeFileHTTP_ContentType is a regression test: every passed
// content type was added as another Content-Type header.
func TestServeFileHTTP_ContentType(t *testing.T) {
	html := NewMemFile("a.html", []byte("<b>x</b>"))

	response := serveTestRequest(t, http.MethodGet, html, "text/plain")
	require.Equal(t, []string{"text/plain"}, response.Header()["Content-Type"])
	require.Equal(t, "<b>x</b>", response.Body.String())

	response = serveTestRequest(t, http.MethodGet, html, "")
	require.Equal(t, []string{"text/html; charset=utf-8"}, response.Header()["Content-Type"], "from the extension")

	response = serveTestRequest(t, http.MethodGet, NewMemFile("noext", []byte("%PDF-1.7")), "")
	require.Equal(t, []string{"application/pdf"}, response.Header()["Content-Type"], "from the content")

	// A Content-Type set before is replaced by contentType, not duplicated
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	ServeFileHTTP(recorder, request, html, "text/html")
	require.Equal(t, []string{"text/html"}, recorder.Header()["Content-Type"])

	// An empty contentType keeps a Content-Type that was set before
	recorder = httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/plain")
	ServeFileHTTP(recorder, request, html, "")
	require.Equal(t, []string{"text/plain"}, recorder.Header()["Content-Type"])
}

// TestServeFileHTTP_Streaming is a regression test: ServeFileHTTP used
// OpenReadSeeker, which reads a file into memory if its reader can't seek,
// also for HEAD requests that send no content.
func TestServeFileHTTP_Streaming(t *testing.T) {
	content := "hello world!"
	fileSystem, root := registerServeTestFS(t,
		NewMemFile("/a.txt", []byte(content)),
		NewMemFile("/noext", []byte(content)),
	)
	file := root.Join("a.txt")

	response := serveTestRequest(t, http.MethodHead, file, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, strconv.Itoa(len(content)), response.Header().Get("Content-Length"))
	require.Empty(t, response.Body.String())
	require.Zero(t, fileSystem.reads.Load(), "HEAD must not read the content")

	response = serveTestRequest(t, http.MethodGet, file, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, content, response.Body.String())
	require.Equal(t, file.Modified().UTC().Format(http.TimeFormat), response.Header().Get("Last-Modified"))

	// A range of a reader that can't seek is reached by skipping forward
	response = serveTestRequest(t, http.MethodGet, file, "", "Range", "bytes=6-10")
	require.Equal(t, http.StatusPartialContent, response.Code)
	require.Equal(t, "world", response.Body.String())

	// Sniffing the content type reads the start and goes back to it
	response = serveTestRequest(t, http.MethodGet, root.Join("noext"), "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "text/plain; charset=utf-8", response.Header().Get("Content-Type"))
	require.Equal(t, content, response.Body.String())

	response = serveTestRequest(t, http.MethodGet, root.Join("noext"), "", "Range", "bytes=0-4")
	require.Equal(t, http.StatusPartialContent, response.Code)
	require.Equal(t, "hello", response.Body.String())

	// Conditional requests use the modification time from Stat
	response = serveTestRequest(t, http.MethodGet, file, "",
		"If-Modified-Since", file.Modified().Add(time.Hour).UTC().Format(http.TimeFormat))
	require.Equal(t, http.StatusNotModified, response.Code)
}

func TestServeFileHTTP_Seekable(t *testing.T) {
	// MemFile readers can seek
	memFile := NewMemFile("a.txt", []byte("hello world!"))
	response := serveTestRequest(t, http.MethodGet, memFile, "", "Range", "bytes=6-10")
	require.Equal(t, http.StatusPartialContent, response.Code)
	require.Equal(t, "world", response.Body.String())
	require.Empty(t, response.Header().Get("Last-Modified"), "a MemFile has no modification time")

	response = serveTestRequest(t, http.MethodGet, NewMemFile("noext", []byte("hello world!")), "", "Range", "bytes=0-4")
	require.Equal(t, http.StatusPartialContent, response.Code)
	require.Equal(t, "hello", response.Body.String())
}

func TestServeFileHTTPHandler(t *testing.T) {
	handler := ServeFileHTTPHandler(NewMemFile("a.json", []byte(`{}`)), "application/json")
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Equal(t, `{}`, recorder.Body.String())
}

func TestHTTPContent_Seek(t *testing.T) {
	c := &httpContent{file: NewMemFile("a.txt", []byte("hello")), size: 5}
	defer c.Close()

	pos, err := c.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	require.Equal(t, int64(5), pos)
	pos, err = c.Seek(-2, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(3), pos)
	data, err := io.ReadAll(c)
	require.NoError(t, err)
	require.Equal(t, "lo", string(data))

	_, err = c.Seek(-1, io.SeekStart)
	require.Error(t, err, "negative position")
	_, err = c.Seek(0, 42)
	require.Error(t, err, "invalid whence")

	// Seeking past the end reads nothing
	_, err = c.Seek(10, io.SeekStart)
	require.NoError(t, err)
	n, err := c.Read(make([]byte, 1))
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)

	// Open errors are returned by Read
	failing := &httpContent{file: serveTestFileReader{MemFile: NewMemFile("a.txt", nil), openErr: context.Canceled}}
	_, err = failing.Read(make([]byte, 1))
	require.ErrorIs(t, err, context.Canceled)
}
