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
	notExistingFile := File(Separator + fsimpl.RandomString())
	assert.False(t, notExistingFile.Exists(), "file does not exist")

	_, err := notExistingFile.OpenReader()
	assert.Equal(t, NewErrDoesNotExist(notExistingFile), err, "can't open notExistingFile")
	assert.True(t, errors.Is(err, NewErrDoesNotExist(InvalidFile)), "ErrDoesNotExist always Is any other ErrDoesNotExist")
	assert.True(t, errors.Is(err, os.ErrNotExist), "ErrDoesNotExist wraps os.ErrNotExist")

	wrapped := fmt.Errorf("wrapped error: %w", err)
	assert.NotEqual(t, NewErrDoesNotExist(notExistingFile), wrapped, "wrapped error is not equal")
	assert.True(t, errors.Is(wrapped, NewErrDoesNotExist(InvalidFile)), "ErrDoesNotExist always Is any other ErrDoesNotExist")
	assert.True(t, errors.Is(wrapped, os.ErrNotExist), "ErrDoesNotExist wraps os.ErrNotExist")

	var target *ErrDoesNotExist
	ok := errors.As(wrapped, &target)
	assert.True(t, ok, "wrapped as ErrDoesNotExist")
	assert.Equal(t, target, err, "wrapped as ErrDoesNotExist")
}
