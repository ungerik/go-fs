package fs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseRawURI(t *testing.T) {
	fs, fsPath := ParseRawURI("")
	assert.Equal(t, InvalidFileSystem(""), fs)
	assert.Equal(t, "", fsPath)

	fs, fsPath = ParseRawURI("/")
	assert.Equal(t, Local, fs)
	assert.Equal(t, "/", fsPath)

	fs, fsPath = ParseRawURI(filepath.Join(string(filepath.Separator), "a", "b"))
	assert.Equal(t, Local, fs)
	assert.Equal(t, filepath.Join(string(filepath.Separator), "a", "b"), fsPath)

	testFS := InvalidFileSystem("test")
	longerTestFS := InvalidFileSystem("longerTest")
	Register(testFS)
	Register(longerTestFS)
	t.Cleanup(func() {
		Unregister(testFS)
		Unregister(longerTestFS)
	})
	assert.Equal(t, Invalid, GetFileSystem("invalid://"))
	assert.Equal(t, testFS, GetFileSystem("invalid://test/"))
	assert.Equal(t, longerTestFS, GetFileSystem("invalid://longerTest/"))

	fs, fsPath = ParseRawURI("invalid://file")
	assert.Equal(t, Invalid, fs)
	assert.Equal(t, "file", fsPath)

	fs, fsPath = ParseRawURI("invalid://test/file")
	assert.Equal(t, testFS, fs)
	assert.Equal(t, "file", fsPath)

	fs, fsPath = ParseRawURI("invalid://longerTest/file")
	assert.Equal(t, longerTestFS, fs)
	assert.Equal(t, "file", fsPath)
}
