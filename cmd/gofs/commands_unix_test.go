//go:build unix

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
