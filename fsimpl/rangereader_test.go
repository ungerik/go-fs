package fsimpl_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fsimpl"
)

// rangeSource records the ranges opened by a RangeReader,
// so the tests can verify that seeks and ReadAt translate
// into the expected requests.
type rangeSource struct {
	data  []byte
	opens [][2]int64
}

func (s *rangeSource) reader() *fsimpl.RangeReader {
	return &fsimpl.RangeReader{
		Size: int64(len(s.data)),
		Open: func(offset, count int64) (io.ReadCloser, error) {
			s.opens = append(s.opens, [2]int64{offset, count})
			end := int64(len(s.data))
			if count >= 0 {
				end = min(offset+count, end)
			}
			return io.NopCloser(bytes.NewReader(s.data[offset:end])), nil
		},
	}
}

func TestRangeReader(t *testing.T) {
	t.Run("sequential reads open one body", func(t *testing.T) {
		src := &rangeSource{data: []byte("0123456789")}
		r := src.reader()
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, src.data, got)
		assert.Equal(t, [][2]int64{{0, -1}}, src.opens, "one request from the start to the end")
		require.NoError(t, r.Close())
	})

	t.Run("Seek reopens from the new position", func(t *testing.T) {
		src := &rangeSource{data: []byte("0123456789")}
		r := src.reader()
		buf := make([]byte, 3)
		_, err := io.ReadFull(r, buf)
		require.NoError(t, err)
		pos, err := r.Seek(-2, io.SeekEnd)
		require.NoError(t, err)
		assert.Equal(t, int64(8), pos)
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, []byte("89"), got)
		assert.Equal(t, [][2]int64{{0, -1}, {8, -1}}, src.opens, "the seek must reopen the body at the new position")
		_, err = r.Seek(-1, io.SeekStart)
		assert.Error(t, err, "negative positions are rejected")
	})

	t.Run("ReadAt requests exactly the range", func(t *testing.T) {
		src := &rangeSource{data: []byte("0123456789")}
		r := src.reader()
		buf := make([]byte, 4)
		n, err := r.ReadAt(buf, 2)
		require.NoError(t, err)
		assert.Equal(t, []byte("2345"), buf[:n])
		n, err = r.ReadAt(buf, 8)
		assert.Equal(t, io.EOF, err, "a short read at the end reports EOF")
		assert.Equal(t, []byte("89"), buf[:n])
		_, err = r.ReadAt(buf, 10)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, [][2]int64{{2, 4}, {8, 2}}, src.opens, "ReadAt must not request beyond the size")
	})

	t.Run("reads after Close fail", func(t *testing.T) {
		src := &rangeSource{data: []byte("0123456789")}
		r := src.reader()
		require.NoError(t, r.Close())
		require.NoError(t, r.Close(), "Close must be idempotent")
		_, err := r.Read(make([]byte, 1))
		assert.ErrorIs(t, err, os.ErrClosed)
		_, err = r.ReadAt(make([]byte, 1), 0)
		assert.ErrorIs(t, err, os.ErrClosed)
	})
}

// TestRangeReader_TruncatedBody verifies that a body which ends before
// Size, like after a dropped connection, is reported as
// io.ErrUnexpectedEOF and not as a complete file.
func TestRangeReader_TruncatedBody(t *testing.T) {
	data := []byte("0123456789")
	var opens [][2]int64
	r := &fsimpl.RangeReader{Size: int64(len(data)), Open: func(offset, count int64) (io.ReadCloser, error) {
		opens = append(opens, [2]int64{offset, count})
		// Every body is cut off after 4 bytes
		end := min(offset+4, int64(len(data)))
		return io.NopCloser(bytes.NewReader(data[offset:end])), nil
	}}

	got, err := io.ReadAll(r)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "ReadAll of a truncated body")
	assert.Equal(t, "0123", string(got))

	// The truncated body was closed, the next Read resumes at the position
	got, err = io.ReadAll(r)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, "4567", string(got))
	assert.Equal(t, [][2]int64{{0, -1}, {4, -1}}, opens, "the next Read opens a body at the position")

	p := make([]byte, 8)
	n, err := r.ReadAt(p, 0)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "ReadAt of a truncated body")
	assert.Equal(t, 4, n)

	// A body that delivers nothing of a range within Size
	empty := &fsimpl.RangeReader{Size: 10, Open: func(int64, int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}}
	_, err = empty.ReadAt(p, 0)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "ReadAt of an empty body")
	_, err = empty.Read(p)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "Read of an empty body")
}

// TestRangeReader_NotBeyondSize verifies that sequential reads stop at
// Size even if the body delivers more, for example because the file
// grew after its size was determined.
func TestRangeReader_NotBeyondSize(t *testing.T) {
	r := &fsimpl.RangeReader{Size: 4, Open: func(offset, count int64) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader([]byte("0123456789")[offset:])), nil
	}}
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "0123", string(got))
}

// ExampleRangeReader reads a file with HTTP range requests. A server that
// ignores the Range header answers 200 with the whole file, so the body
// is moved to the offset before it is returned, and other status codes
// are errors instead of content.
func ExampleRangeReader() {
	content := "Hello, range requests!"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "hello.txt", time.Time{}, strings.NewReader(content))
	}))
	defer server.Close()

	r := &fsimpl.RangeReader{
		Size: int64(len(content)),
		Open: func(offset, count int64) (io.ReadCloser, error) {
			req, err := http.NewRequest(http.MethodGet, server.URL+"/hello.txt", nil)
			if err != nil {
				return nil, err
			}
			if count < 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			} else {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+count-1))
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			switch resp.StatusCode {
			case http.StatusPartialContent:
				return resp.Body, nil
			case http.StatusOK:
				// The server ignored the range and sends the whole file
				if _, err = io.CopyN(io.Discard, resp.Body, offset); err != nil {
					return nil, errors.Join(err, resp.Body.Close())
				}
				return resp.Body, nil
			default:
				return nil, errors.Join(fmt.Errorf("unexpected status %s", resp.Status), resp.Body.Close())
			}
		},
	}
	defer r.Close()

	word := make([]byte, 5)
	if _, err := r.ReadAt(word, 7); err != nil {
		panic(err)
	}
	fmt.Println(string(word))

	// Output:
	// range
}

// TestRangeReader_ReadAtZeroLength verifies the io.ReaderAt contract for
// an empty buffer: zero bytes requested were zero bytes delivered, so it
// must succeed like bytes.Reader instead of reporting io.EOF.
func TestRangeReader_ReadAtZeroLength(t *testing.T) {
	src := &rangeSource{data: []byte("hello world")}
	r := src.reader()

	n, err := r.ReadAt(nil, 0)
	assert.NoError(t, err, "zero bytes at a valid offset is not an EOF")
	assert.Zero(t, n)

	n, err = r.ReadAt([]byte{}, int64(len(src.data))-1)
	assert.NoError(t, err, "still within the data, so not an EOF")
	assert.Zero(t, n)

	// Past the end stays io.EOF like bytes.Reader, even for an empty buffer
	n, err = r.ReadAt(nil, int64(len(src.data)))
	assert.ErrorIs(t, err, io.EOF)
	assert.Zero(t, n)

	assert.Empty(t, src.opens, "an empty read must not cost a range request")
}
