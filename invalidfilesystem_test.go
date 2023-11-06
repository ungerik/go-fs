package fs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInvalidFileSystem_Prefix(t *testing.T) {
	assert.Equal(t, "invalid://", InvalidFileSystem("").Prefix())
	assert.Equal(t, "invalid://test/", InvalidFileSystem("test").Prefix())
	assert.Equal(t, "invalid://test/", InvalidFileSystem("/test/").Prefix())
}
