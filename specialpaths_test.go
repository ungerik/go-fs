package fs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHomeDir(t *testing.T) {
	assert.True(t, HomeDir().IsDir(), "home directory exists")
}

func TestTempDir(t *testing.T) {
	assert.True(t, TempDir().IsDir(), "temp directory exists")
}

func TestExecutable(t *testing.T) {
	assert.True(t, Executable().Exists(), "executable file for current process exists")
}
