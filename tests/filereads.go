package tests

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"testing"

	"github.com/ungerik/go-fs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileReads(t *testing.T, expectedContent []byte, file fs.File) {
	t.Helper()

	assert.GreaterOrEqual(t, file.Size(), int64(4), "file size is at least 4 bytes")

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()

	// IsReadable
	require.True(t, file.IsReadable(), "file is readable")

	// ReadAll
	{
		data, err := file.ReadAll()
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)
	}

	// ReadAllContext
	{
		data, err := file.ReadAllContext(context.Background())
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)
		_, err = file.ReadAllContext(canceledContext)
		require.Error(t, err)
	}

	// ReadAllString
	{
		str, err := file.ReadAllString()
		require.NoError(t, err)
		require.Equal(t, string(expectedContent), str)
	}

	// ReadAllStringContext
	{
		str, err := file.ReadAllStringContext(context.Background())
		require.NoError(t, err)
		require.Equal(t, string(expectedContent), str)
		_, err = file.ReadAllStringContext(canceledContext)
		require.Error(t, err)
	}

	// ContentHash
	{
		hash, err := file.ContentHash()
		require.NoError(t, err)
		require.NotEmpty(t, hash)
	}

	// ContentHashContext
	{
		hash, err := file.ContentHashContext(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, hash)
		_, err = file.ContentHashContext(canceledContext)
		require.Error(t, err)
	}

	// ReadAllContentHash
	{
		data, hash, err := file.ReadAllContentHash(context.Background())
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)
		require.NotEmpty(t, hash)
		_, _, err = file.ReadAllContentHash(canceledContext)
		require.Error(t, err)
	}

	// OpenReader
	{
		r, err := file.OpenReader()
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)
		require.NoError(t, r.Close())
	}

	// OpenReadSeeker
	{
		rs, err := file.OpenReadSeeker()
		require.NoError(t, err)
		data, err := io.ReadAll(rs)
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)

		// seek to start and read all again
		pos, err := rs.Seek(0, io.SeekStart)
		require.NoError(t, err)
		require.Equal(t, int64(0), pos)
		data, err = io.ReadAll(rs)
		require.NoError(t, err)
		require.Equal(t, expectedContent, data)

		// seek 2 bytes back and read 1 byte
		pos, err = rs.Seek(-2, io.SeekCurrent)
		require.NoError(t, err)
		require.Equal(t, int64(len(expectedContent)-2), pos)
		data = make([]byte, 1)
		n, err := rs.Read(data)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, expectedContent[len(expectedContent)-2:len(expectedContent)-1], data)

		// seek to last byte and read 1 byte
		pos, err = rs.Seek(-1, io.SeekEnd)
		require.NoError(t, err)
		require.Equal(t, int64(len(expectedContent)-1), pos)
		data = make([]byte, 1)
		n, err = rs.Read(data)
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, expectedContent[len(expectedContent)-1:], data)

		require.NoError(t, rs.Close())
	}

	// GobEncode
	{
		encoded, err := file.GobEncode()
		require.NoError(t, err)

		expected := bytes.NewBuffer(nil)
		enc := gob.NewEncoder(expected)
		err = errors.Join(enc.Encode(file.Name()), enc.Encode(expectedContent))
		require.NoError(t, err)

		require.Equal(t, expected.Bytes(), encoded)
	}
}
