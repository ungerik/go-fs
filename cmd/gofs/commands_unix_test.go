//go:build unix

package main

import (
	"context"
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

// TestPut_DanglingLinkReadOnlyDir checks that put refuses to replace a
// dangling symbolic link in a directory that refuses the temporary file,
// instead of writing through the link, which would create its target.
func TestPut_DanglingLinkReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permissions of the directory")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Symlink(target, link))
	require.NoError(t, os.Chmod(dir, 0o500)) //#nosec G302 -- test directory
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	code, stderr := runStdin(t.Context(), nil, strings.NewReader("new"), "put", link)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "can't replace the dangling symbolic link")
	assert.NoFileExists(t, target, "not written through the link")
	dest, err := os.Readlink(link)
	require.NoError(t, err, "the link is kept")
	assert.Equal(t, target, dest)
}

// TestPut_CanceledWhileStdinIsOpen checks that a canceled put ends while
// it waits for stdin from a pipe that stays open, like a signal does with
// "(printf partial; sleep 60) | gofs put file", and leaves neither a
// partial new file nor a temporary file, and keeps an existing file.
func TestPut_CanceledWhileStdinIsOpen(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	writeFiles(t, dir, map[string]string{"existing.txt": "original"})
	for _, file := range []string{filepath.Join(dir, "new.txt"), existing} {
		stdin, stdinWriter, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { _ = stdin.Close(); _ = stdinWriter.Close() })
		_, err = stdinWriter.WriteString("partial")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan int, 1)
		go func() {
			code, _ := runStdin(ctx, nil, stdin, "put", file)
			done <- code
		}()
		// gstack-timing-ok: the sleep only lets put start reading, the
		// outcome is the same if the cancel comes first
		time.Sleep(100 * time.Millisecond)
		cancel()
		select {
		case code := <-done:
			assert.Equal(t, 1, code, file)
		case <-time.After(10 * time.Second):
			t.Fatalf("put of %s didn't end when canceled while stdin was open", file)
		}
	}
	assert.NoFileExists(t, filepath.Join(dir, "new.txt"), "no partial new file")
	assert.Equal(t, "original", readFile(t, existing))
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
