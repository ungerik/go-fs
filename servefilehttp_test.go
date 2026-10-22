package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fsimpl"
)

// serveTestFS wraps a file system so that OpenReader returns
// readers that can't seek and counts the Read calls and the bytes read.
type serveTestFS struct {
	WriteFileSystem
	reads     atomic.Int64
	bytesRead atomic.Int64
}

func (f *serveTestFS) OpenReader(filePath string) (io.ReadCloser, error) {
	r, err := f.WriteFileSystem.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	return &serveTestReader{ReadCloser: r, fs: f}, nil
}

// serveTestReader only has the methods Read and Close.
type serveTestReader struct {
	io.ReadCloser
	fs *serveTestFS
}

func (r *serveTestReader) Read(p []byte) (int, error) {
	r.fs.reads.Add(1)
	n, err := r.ReadCloser.Read(p)
	r.fs.bytesRead.Add(int64(n))
	return n, err
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

// multipleRanges is a Range header with 100 pairs of ranges at the end and
// at the start of the content. The 200 ranges are the most net/http accepts.
var multipleRanges = "bytes=" + strings.Repeat("-1,0-0,", 99) + "-1,0-0"

// TestServeFileHTTP_MultipleRanges is a regression test: every range of a
// multi-range request that starts before the previous one reopened a reader
// that can't seek and read it again from the start, so multipleRanges made
// the file system read the whole file 100 times. A reader that can't seek
// now serves at most one range, multiple ranges get the full content.
func TestServeFileHTTP_MultipleRanges(t *testing.T) {
	content := strings.Repeat("0123456789abcdef", 4096)
	fileSystem, root := registerServeTestFS(t,
		NewMemFile("/a.txt", []byte(content)),
		NewMemFile("/noext", []byte(content)),
	)

	response := serveTestRequest(t, http.MethodGet, root.Join("a.txt"), "", "Range", multipleRanges)
	assert.LessOrEqual(t, fileSystem.bytesRead.Load(), int64(len(content)), "the file is read once")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.True(t, response.Body.String() == content, "the full content")

	// Sniffing the content type reopens the reader once
	fileSystem.bytesRead.Store(0)
	response = serveTestRequest(t, http.MethodGet, root.Join("noext"), "", "Range", multipleRanges)
	assert.LessOrEqual(t, fileSystem.bytesRead.Load(), int64(len(content)+512), "the file is read once after sniffing")
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "text/plain; charset=utf-8", response.Header().Get("Content-Type"))
	assert.True(t, response.Body.String() == content, "the full content")

	// A single range is still served
	fileSystem.bytesRead.Store(0)
	response = serveTestRequest(t, http.MethodGet, root.Join("noext"), "", "Range", "bytes=-4")
	assert.LessOrEqual(t, fileSystem.bytesRead.Load(), int64(len(content)+512))
	assert.Equal(t, http.StatusPartialContent, response.Code)
	assert.Equal(t, "cdef", response.Body.String())

	// The content of a reader that can seek, here an fsimpl.RangeReader
	// like the ones of azureblobfs and webdavfs, is read for the ranges only
	rangeFS, rangeRoot := registerRangeReaderTestFS(t, NewMemFile("/a.txt", []byte(content)))
	response = serveTestRequest(t, http.MethodGet, rangeRoot.Join("a.txt"), "", "Range", multipleRanges)
	assert.LessOrEqual(t, rangeFS.bytesRead.Load(), int64(200), "only the ranges are read")
	assert.Equal(t, http.StatusPartialContent, response.Code)
	assert.True(t, strings.HasPrefix(response.Header().Get("Content-Type"), "multipart/byteranges"), "multipart response")
}

// rangeReaderTestFS wraps a file system so that OpenReader returns an
// fsimpl.RangeReader, which seeks by opening the content at another offset,
// and counts the bytes read. The RangeReader has no Stat method.
type rangeReaderTestFS struct {
	WriteFileSystem
	bytesRead atomic.Int64
}

func (f *rangeReaderTestFS) OpenReader(filePath string) (io.ReadCloser, error) {
	data, err := fsReadAll(context.Background(), f.WriteFileSystem, filePath)
	if err != nil {
		return nil, err
	}
	return &fsimpl.RangeReader{
		Size: int64(len(data)),
		Open: func(offset, count int64) (io.ReadCloser, error) {
			return io.NopCloser(&countingReader{Reader: bytes.NewReader(data[offset:]), n: &f.bytesRead}), nil
		},
	}, nil
}

// countingReader adds the number of bytes read to n.
type countingReader struct {
	io.Reader
	n *atomic.Int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n.Add(int64(n))
	return n, err
}

// registerRangeReaderTestFS returns the root of a rangeReaderTestFS
// over a MemFileSystem with initialFiles.
func registerRangeReaderTestFS(t *testing.T, initialFiles ...MemFile) (*rangeReaderTestFS, File) {
	t.Helper()
	memFS, err := NewMemFileSystem(nil, initialFiles...)
	require.NoError(t, err)
	wrapped := &rangeReaderTestFS{WriteFileSystem: memFS}
	require.NoError(t, Register(wrapped))
	t.Cleanup(func() {
		Unregister(wrapped)
		_ = memFS.Close()
	})
	return wrapped, wrapped.RootDir()
}

// readFromRecorder is a ResponseRecorder that implements io.ReaderFrom
// like the http.ResponseWriter of net/http, which sends an *os.File
// with sendfile if it gets one wrapped in an *io.LimitedReader.
type readFromRecorder struct {
	*httptest.ResponseRecorder
	src io.Reader // passed to ReadFrom
}

func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.src = src
	return io.Copy(r.ResponseRecorder, src)
}

// TestServeFileHTTP_SendFile is a regression test: every reader was wrapped
// in a type of the package, so net/http couldn't send an *os.File
// with sendfile and copied it through user space, at half the speed.
func TestServeFileHTTP_SendFile(t *testing.T) {
	file := File(t.TempDir()).Join("a.txt")
	require.NoError(t, file.WriteAllString(t.Context(), "hello world!"))

	for _, fileReader := range []FileReader{file, NewMemFile("a.txt", []byte("hello world!"))} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		recorder := &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
		ServeFileHTTP(recorder, request, fileReader, "")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "hello world!", recorder.Body.String())
		limited, ok := recorder.src.(*io.LimitedReader)
		require.True(t, ok, "ServeContent copies an *io.LimitedReader, got %T", recorder.src)
		if _, isFile := fileReader.(File); isFile {
			assert.IsType(t, (*os.File)(nil), limited.R, "the *os.File is passed to net/http")
		} else {
			assert.IsType(t, (*fsimpl.ReadonlyFileBuffer)(nil), limited.R, "a reader that can seek is passed to net/http")
		}
	}
}

// unknownSizeFileReader is a FileReader like an HTTP URL without
// Content-Length: Stat reports the size -1 and the reader can't seek.
type unknownSizeFileReader struct {
	MemFile
	modified time.Time
}

func (f unknownSizeFileReader) Stat() (iofs.FileInfo, error) {
	info, err := f.MemFile.Stat()
	return unknownSizeInfo{FileInfo: info, modified: f.modified}, err
}

func (f unknownSizeFileReader) OpenReader() (ReadCloser, error) {
	return statReadCloser{ReadCloser: io.NopCloser(strings.NewReader(string(f.FileData)))}, nil
}

type unknownSizeInfo struct {
	iofs.FileInfo
	modified time.Time
}

func (unknownSizeInfo) Size() int64 { return -1 }

func (i unknownSizeInfo) ModTime() time.Time { return i.modified }

// TestServeFileHTTP_UnknownSize is a regression test: a file with the
// size -1 was answered with 500 "seeker can't seek", because
// http.ServeContent needs the size. The content is streamed now
// without Content-Length, a Range header is ignored.
func TestServeFileHTTP_UnknownSize(t *testing.T) {
	content := "hello world!"
	modified := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	file := unknownSizeFileReader{MemFile: NewMemFile("noext", []byte(content))}

	response := serveTestRequest(t, http.MethodGet, file, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, content, response.Body.String())
	require.Empty(t, response.Header().Get("Content-Length"))
	require.Equal(t, "text/plain; charset=utf-8", response.Header().Get("Content-Type"), "sniffed from the content")

	response = serveTestRequest(t, http.MethodGet, file, "", "Range", "bytes=0-4")
	require.Equal(t, http.StatusOK, response.Code, "the Range header is ignored")
	require.Equal(t, content, response.Body.String())

	response = serveTestRequest(t, http.MethodHead, file, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Empty(t, response.Body.String())

	file = unknownSizeFileReader{MemFile: NewMemFile("a.json", []byte(`{}`)), modified: modified}
	response = serveTestRequest(t, http.MethodGet, file, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, `{}`, response.Body.String())
	require.Equal(t, "application/json", response.Header().Get("Content-Type"), "from the extension")
	require.Equal(t, modified.Format(http.TimeFormat), response.Header().Get("Last-Modified"))

	response = serveTestRequest(t, http.MethodGet, file, "text/plain")
	require.Equal(t, "text/plain", response.Header().Get("Content-Type"), "the passed content type")
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

	// A reader that can't seek is reopened once to go back, not twice
	_, root := registerServeTestFS(t, NewMemFile("/a.txt", []byte("hello")))
	once := &httpContent{file: root.Join("a.txt"), size: 5}
	defer once.Close()
	for i := range 2 {
		data = make([]byte, 2)
		_, err = io.ReadFull(once, data)
		require.NoError(t, err, "read %d", i)
		require.Equal(t, "he", string(data))
		_, err = once.Seek(0, io.SeekStart)
		require.NoError(t, err)
	}
	_, err = once.Read(make([]byte, 1))
	require.Error(t, err, "going back a second time")
}
