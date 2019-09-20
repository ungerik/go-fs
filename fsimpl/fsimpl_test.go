package fsimpl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_DirAndName(t *testing.T) {
	refTable := map[string][2]string{
		"/":  {"/", ""},
		"./": {".", "."},
		".":  {".", "."},
		// "/.":            {"/", ""},
		"hello":         {".", "hello"},
		"./hello":       {".", "hello"},
		"hello/":        {".", "hello"},
		"./hello/":      {".", "hello"},
		"/hello/world":  {"/hello", "world"},
		"hello/world":   {"hello", "world"},
		"/hello/world/": {"/hello", "world"},
		"hello/world/":  {"hello", "world"},
	}

	for filePath, dirAndName := range refTable {
		dir, name := DirAndName(filePath, 0, "/")
		assert.Equal(t, dir, dirAndName[0], "filePath(%#v): %#v, %#v", filePath, dir, name)
		assert.Equal(t, name, dirAndName[1], "filePath(%#v): %#v, %#v", filePath, dir, name)
	}
}

func Test_RandomString(t *testing.T) {
	str := RandomString()
	assert.Equal(t, 20, len(str))
}

func Test_ReadonlyFileBuffer(t *testing.T) {
	out := make([]byte, 0)
	b := NewReadonlyFileBuffer(nil)
	n, err := b.Read(out)
	assert.NoError(t, err, "Read")
	assert.Equal(t, n, 0, "no bytes read")
}
