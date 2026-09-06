package fsimpl

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rangeSource records the ranges opened by a RangeReader,
// so the tests can verify that seeks and ReadAt translate
// into the expected requests.
type rangeSource struct {
	data  []byte
	opens [][2]int64
}

func (s *rangeSource) reader() *RangeReader {
	return &RangeReader{
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
