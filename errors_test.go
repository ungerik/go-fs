package fs

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs/fsimpl"
)

func TestErrDoesNotExist(t *testing.T) {
	notExistingFile := Local.RootDir().Join(fsimpl.RandomString())
	assert.False(t, notExistingFile.Exists(), "file does not exist")

	_, err := notExistingFile.OpenReader()
	assert.Equal(t, NewErrDoesNotExist(notExistingFile), err, "can't open notExistingFile")
	assert.True(t, errors.Is(err, os.ErrNotExist), "ErrDoesNotExist wraps os.ErrNotExist")

	wrapped := fmt.Errorf("wrapped error: %w", err)
	assert.NotEqual(t, NewErrDoesNotExist(notExistingFile), wrapped, "wrapped error is not equal")
	assert.True(t, errors.Is(wrapped, os.ErrNotExist), "ErrDoesNotExist wraps os.ErrNotExist")

	var target ErrDoesNotExist
	ok := errors.As(wrapped, &target)
	assert.True(t, ok, "wrapped as ErrDoesNotExist")
	assert.Equal(t, target, err, "wrapped as ErrDoesNotExist")
}

// TestTypedErrors_Is checks that errors.Is matches any value of a typed
// error with the zero value of its type, and that comparing errors created
// for non-comparable FileReaders like MemFile does not panic.
func TestTypedErrors_Is(t *testing.T) {
	file := File("/some/file.txt")
	memFile := NewMemFile("mem.txt", []byte("data"))

	for _, tc := range []struct {
		name   string
		err    error
		target error
	}{
		{"ErrDoesNotExist", NewErrDoesNotExist(file), ErrDoesNotExist{}},
		{"ErrPermission", NewErrPermission(file), ErrPermission{}},
		{"ErrAlreadyExists", NewErrAlreadyExists(file), ErrAlreadyExists{}},
		{"ErrIsDirectory", NewErrIsDirectory(file), ErrIsDirectory{}},
		{"ErrIsNotDirectory", NewErrIsNotDirectory(file), ErrIsNotDirectory{}},
		{"ErrDirectoryNotEmpty", NewErrDirectoryNotEmpty(file), ErrDirectoryNotEmpty{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, tc.err, tc.target)
			assert.ErrorIs(t, fmt.Errorf("wrapped: %w", tc.err), tc.target)
			assert.NotErrorIs(t, errors.New("other"), tc.target)
		})
	}

	// Both errors hold a MemFile, which contains a byte slice
	// and can't be compared with ==
	assert.NotPanics(t, func() {
		assert.ErrorIs(t, NewErrDoesNotExist(memFile), NewErrDoesNotExist(memFile))
		assert.ErrorIs(t, NewErrIsDirectory(memFile), NewErrIsDirectory(memFile))
	})
}

// TestTypedErrors_FileReader checks that the File accessor only returns
// real Files, never the name of a MemFile, which would be a local path.
func TestTypedErrors_FileReader(t *testing.T) {
	memFile := NewMemFile("local-looking-name.txt", nil)
	err := NewErrIsDirectory(memFile)
	f, ok := err.File()
	assert.False(t, ok, "a MemFile is not a File")
	assert.Equal(t, InvalidFile, f)
	assert.Equal(t, memFile, err.FileReader())
	assert.Contains(t, err.Error(), "local-looking-name.txt")

	file := File("/dir/file.txt")
	err = NewErrIsDirectory(file)
	f, ok = err.File()
	assert.True(t, ok)
	assert.Equal(t, file, f)
	assert.Equal(t, FileReader(file), err.FileReader())

	pathErr := NewErrPathDoesNotExist("document part.pdf")
	_, ok = pathErr.File()
	assert.False(t, ok, "a path is not a File")
	assert.Nil(t, pathErr.FileReader())
	assert.Contains(t, pathErr.Error(), "document part.pdf")
	assert.ErrorIs(t, pathErr, os.ErrNotExist)
}

// TestErrEmptyPath checks that an empty path is not reported as not existing,
// so that ignoring missing files doesn't hide the programming error.
func TestErrEmptyPath(t *testing.T) {
	assert.NotErrorIs(t, ErrEmptyPath, os.ErrNotExist)
	assert.ErrorIs(t, File("").Remove(), ErrEmptyPath)
	assert.Error(t, RemoveErrDoesNotExist(File("").Remove()))
}
