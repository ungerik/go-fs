//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
)

// TestPut_NamedPipe checks that put writes into a named pipe
// instead of replacing it with a regular file.
func TestPut_NamedPipe(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	received := make(chan string, 1)
	go func() {
		f, err := os.Open(fifo) //#nosec G304 -- test file
		if err != nil {
			received <- err.Error()
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		received <- string(data)
	}()

	code, stderr := runStdin(t.Context(), nil, strings.NewReader("hello"), "put", fifo)
	require.Equal(t, 0, code, stderr)
	select {
	case data := <-received:
		assert.Equal(t, "hello", data)
	case <-time.After(10 * time.Second):
		// The reader waits forever for a writer of the replaced pipe
		t.Fatal("put didn't write into the named pipe")
	}
	info, err := os.Lstat(fifo)
	require.NoError(t, err)
	assert.Equal(t, os.ModeNamedPipe, info.Mode().Type(), "still a named pipe")
}

// TestPut_DanglingLink checks that put replaces a dangling symbolic link
// with a file, like an existing file, instead of creating the target of the
// link, and keeps the link if stdin fails.
func TestPut_DanglingLink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Symlink(target, link))

	code, stderr := runStdin(t.Context(), nil, &failingReader{"partial", errors.New("stdin broke")}, "put", link)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "stdin broke")
	assert.NoFileExists(t, target, "no partial target")
	dest, err := os.Readlink(link)
	require.NoError(t, err, "the link is kept")
	assert.Equal(t, target, dest)

	code, stderr = runStdin(t.Context(), nil, strings.NewReader("new"), "put", link)
	require.Equal(t, 0, code, stderr)
	assert.NoFileExists(t, target, "not written through the link")
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the link is replaced with a file")
	assert.Equal(t, "new", readFile(t, link))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file left behind")
}

func TestIsSpecialFile_Unix(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	for _, file := range []fs.File{fs.File(fifo), fs.File("/dev/null")} {
		info, err := file.Stat()
		require.NoError(t, err)
		assert.True(t, isSpecialFile(file, info), file)
	}
}
