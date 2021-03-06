package fsimpl

import (
	"fmt"
	"io"
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
	b := NewReadonlyFileBuffer(nil, nil)
	n, err := b.Read(out)
	assert.Equal(t, io.EOF, err, "Read")
	assert.Equal(t, n, 0, "no bytes read")
}

func ExampleExt() {
	fmt.Println(Ext("image.png", "/"))
	fmt.Println(Ext("image.png", ""))
	fmt.Println(Ext("image.66.png", "/"))
	fmt.Println(Ext("file", "/") == "")
	fmt.Println(Ext("dir.with.ext/file", "/") == "")
	fmt.Println(Ext("dir.with.ext/file.ext", "/"))
	fmt.Println(Ext("dir.with.ext/file", "\\"))
	fmt.Println(Ext("dir.with.ext/file", ""))

	// Output:
	// .png
	// .png
	// .png
	// true
	// true
	// .ext
	// .ext/file
	// .ext/file
}

func ExampleTrimExt() {
	fmt.Println(TrimExt("image.png", "/"))
	fmt.Println(TrimExt("image.png", ""))
	fmt.Println(TrimExt("image.66.png", "/"))
	fmt.Println(TrimExt("file", "/"))
	fmt.Println(TrimExt("dir.with.ext/file", "/"))
	fmt.Println(TrimExt("dir.with.ext/file.ext", "/"))
	fmt.Println(TrimExt("dir.with.ext/file", "\\"))
	fmt.Println(TrimExt("dir.with.ext/file", ""))

	// Output:
	// image
	// image
	// image.66
	// file
	// dir.with.ext/file
	// dir.with.ext/file
	// dir.with
	// dir.with
}
