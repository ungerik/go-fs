package fs

import (
	"errors"
	"io"
	"net/http"
	"os"
)

// ServeFileHTTPHandler returns a http.Handler that serves the passed file via HTTP,
// see ServeFileHTTP for the contentType and the status codes.
func ServeFileHTTPHandler(file FileReader, contentType string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ServeFileHTTP(response, request, file, contentType)
	})
}

// ServeFileHTTP serves the passed file via HTTP using http.ServeContent,
// which handles Range, conditional and HEAD requests.
//
// A non empty contentType is set as the Content-Type header,
// replacing a Content-Type that is already set.
// If contentType is empty, then an already set Content-Type header is used,
// or else the type is deduced from the file name and if that fails from the content.
//
// The status code is 404 Not Found if the file does not exist or is a directory,
// because a directory has no content to serve and a 404 doesn't disclose
// that a directory exists. It is 403 Forbidden if reading the file is not
// permitted and 500 Internal Server Error for any other error.
//
// The content is streamed from OpenReader, it is not read into memory.
// The size and modification time come from Stat,
// so HEAD requests don't read the content
// unless it is needed to deduce the content type.
func ServeFileHTTP(response http.ResponseWriter, request *http.Request, file FileReader, contentType string) {
	info, err := file.Stat()
	if err != nil {
		serveFileHTTPError(response, request, err)
		return
	}
	if info.IsDir() {
		http.NotFound(response, request)
		return
	}
	content := &httpContent{file: file, size: info.Size()}
	defer content.Close()
	// Open the file before ServeContent writes the status code,
	// so that errors like a missing permission get their status code
	err = content.open()
	if err != nil {
		serveFileHTTPError(response, request, err)
		return
	}
	if contentType != "" {
		response.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(response, request, file.Name(), info.ModTime(), content)
}

// serveFileHTTPError writes the HTTP error status for err.
func serveFileHTTPError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrIsDirectory{}):
		http.NotFound(response, request)
	case errors.Is(err, os.ErrPermission):
		http.Error(response, http.StatusText(http.StatusForbidden), http.StatusForbidden)
	default:
		http.Error(response, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

// httpContent is the io.ReadSeeker that ServeFileHTTP passes to
// http.ServeContent. The size is known from Stat, so Seek only sets
// the position of the next Read. Read streams the file from OpenReader:
// a reader that can't seek skips forward by reading and is reopened
// to go back, which ServeContent does only after sniffing the content type.
type httpContent struct {
	file   FileReader
	size   int64
	offset int64      // position of the next Read
	reader ReadCloser // nil until opened
	pos    int64      // position of reader
}

// open opens the reader at position zero if it isn't open yet.
func (c *httpContent) open() error {
	if c.reader != nil {
		return nil
	}
	reader, err := c.file.OpenReader()
	if err != nil {
		return err
	}
	c.reader, c.pos = reader, 0
	return nil
}

// Seek implements io.Seeker.
func (c *httpContent) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += c.offset
	case io.SeekEnd:
		offset += c.size
	default:
		return 0, errors.New("httpContent.Seek: invalid whence")
	}
	if offset < 0 {
		return 0, errors.New("httpContent.Seek: negative position")
	}
	c.offset = offset
	return offset, nil
}

// Read implements io.Reader.
func (c *httpContent) Read(p []byte) (int, error) {
	err := c.moveReader()
	if err != nil {
		return 0, err
	}
	n, err := c.reader.Read(p)
	c.pos += int64(n)
	c.offset = c.pos
	return n, err
}

// moveReader opens the reader if necessary and moves it to the offset.
func (c *httpContent) moveReader() error {
	err := c.open()
	if err != nil {
		return err
	}
	if c.pos == c.offset {
		return nil
	}
	if seeker, ok := c.reader.(io.Seeker); ok {
		_, err = seeker.Seek(c.offset, io.SeekStart)
		if err != nil {
			return err
		}
		c.pos = c.offset
		return nil
	}
	if c.offset < c.pos {
		// The reader can't go back, read again from the start
		err = c.Close()
		if err != nil {
			return err
		}
		err = c.open()
		if err != nil {
			return err
		}
	}
	n, err := io.CopyN(io.Discard, c.reader, c.offset-c.pos)
	c.pos += n
	return err
}

// Close closes the reader if it is open.
func (c *httpContent) Close() error {
	if c.reader == nil {
		return nil
	}
	err := c.reader.Close()
	c.reader = nil
	return err
}
