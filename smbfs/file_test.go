package smbfs

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestFile verifies the io semantics of file handles: ReadAt returned
// nil instead of io.EOF at the end of the file, and Seek accepted a
// negative offset.
func TestFile(t *testing.T) {
	f, _ := dialTestFS(t)
	ctx := t.Context()
	name := fmt.Sprintf("/file-%d.txt", time.Now().UnixNano())
	require.NoError(t, f.WriteAll(ctx, name, []byte("0123456789"), 0))
	t.Cleanup(func() { _ = f.Remove(name) })

	r, err := f.OpenReader(name)
	require.NoError(t, err)
	handle := r.(fs.ReadAtSeekCloser)

	buf := make([]byte, 4)
	n, err := handle.ReadAt(buf, 8)
	assert.Equal(t, 2, n)
	assert.ErrorIs(t, err, io.EOF, "ReadAt across the end")
	n, err = handle.ReadAt(buf, 20)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF, "ReadAt past the end")
	n, err = handle.ReadAt(buf, 2)
	require.NoError(t, err)
	assert.Equal(t, "2345", string(buf[:n]))

	_, err = handle.Seek(-1, io.SeekStart)
	assert.Error(t, err, "Seek before the start")
	_, err = handle.Seek(-11, io.SeekEnd)
	assert.Error(t, err, "Seek before the start from the end")
	_, err = handle.Seek(0, 42)
	assert.Error(t, err, "invalid whence")
	pos, err := handle.Seek(-3, io.SeekEnd)
	require.NoError(t, err)
	assert.Equal(t, int64(7), pos)
	pos, err = handle.Seek(1, io.SeekCurrent)
	require.NoError(t, err)
	assert.Equal(t, int64(8), pos)
	_, err = handle.Seek(-9, io.SeekCurrent)
	assert.Error(t, err, "Seek before the start from the current offset")
	rest, err := io.ReadAll(handle)
	require.NoError(t, err)
	assert.Equal(t, "89", string(rest), "a failed Seek must not move the offset")
	require.NoError(t, handle.Close())

	w, err := f.OpenReadWriter(name, 0)
	require.NoError(t, err)
	_, err = w.(io.WriterAt).WriteAt([]byte("ab"), 1)
	require.NoError(t, err)
	_, err = w.Write([]byte("X"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	data, err := f.ReadAll(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, "Xab3456789", string(data))
}

// TestFile_AfterClose verifies that an open handle reports
// fs.ErrFileSystemClosed after Close of the file system.
func TestFile_AfterClose(t *testing.T) {
	f, _ := dialTestFS(t)
	name := fmt.Sprintf("/after-close-%d.txt", time.Now().UnixNano())
	require.NoError(t, f.WriteAll(t.Context(), name, []byte("content"), 0))
	t.Cleanup(func() {
		cleanup, err := Dial(context.Background(), testSMBAddress, &Options{Username: testUsername, Password: testPassword})
		if err == nil {
			_ = cleanup.(*fileSystem).Remove(name)
			_ = cleanup.Close()
		}
	})

	r, err := f.OpenReader(name)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = r.Read(make([]byte, 4))
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.NoError(t, r.Close(), "closing a handle of a closed file system")
}
