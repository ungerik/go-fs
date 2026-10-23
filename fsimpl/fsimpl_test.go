package fsimpl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitDirAndName(t *testing.T) {
	refTable := map[string][2]string{
		"/":                             {"/", ""},
		"./":                            {".", "."},
		".":                             {".", "."},
		"/.":                            {"/", "."},
		"hello":                         {".", "hello"},
		"./hello":                       {".", "hello"},
		"hello/":                        {".", "hello"},
		"./hello/":                      {".", "hello"},
		"/hello/world":                  {"/hello", "world"},
		"hello/world":                   {"hello", "world"},
		"/hello/world/":                 {"/hello", "world"},
		"hello/world/":                  {"hello", "world"},
		"http://example.com/dir":        {"http://example.com", "dir"},
		"sftp://example.com/dir/subdir": {"sftp://example.com/dir", "subdir"},
	}

	for filePath, dirAndName := range refTable {
		dir, name := SplitDirAndName(filePath, 0, "/")
		assert.Equalf(t, dirAndName[0], dir, "SplitDirAndName(%#v) = %#v, %#v", filePath, dir, name)
		assert.Equalf(t, dirAndName[1], name, "SplitDirAndName(%#v) = %#v, %#v", filePath, dir, name)
	}
}

// TestSplitDirAndName_Volume verifies that the root of a volume has an
// empty name and is the parent of the names directly below it. The root
// `C:\` used to be split into (".", "C:") and `C:\a` into ("C:", "a"),
// which is the drive relative current directory on Windows.
func TestSplitDirAndName_Volume(t *testing.T) {
	tests := []struct {
		path      string
		volumeLen int
		dir, name string
	}{
		{`C:\`, 2, `C:\`, ""},
		{`C:`, 2, `C:`, ""},
		{`C:\a`, 2, `C:\`, "a"},
		{`C:\a\`, 2, `C:\`, "a"},
		{`C:\a\b`, 2, `C:\a`, "b"},
		{`C:a`, 2, `C:`, "a"},
		{`\\server\share`, 14, `\\server\share`, ""},
		{`\\server\share\`, 14, `\\server\share\`, ""},
		{`\\server\share\a`, 14, `\\server\share\`, "a"},
		{`\\server\share\a\b`, 14, `\\server\share\a`, "b"},
		{`\a`, 0, `\`, "a"},
		{`\`, 0, `\`, ""},
	}
	for _, tt := range tests {
		dir, name := SplitDirAndName(tt.path, tt.volumeLen, `\`)
		assert.Equalf(t, tt.dir, dir, "dir of SplitDirAndName(%#v, %d)", tt.path, tt.volumeLen)
		assert.Equalf(t, tt.name, name, "name of SplitDirAndName(%#v, %d)", tt.path, tt.volumeLen)
	}
}

func TestRandomString(t *testing.T) {
	for range 100 {
		s := RandomString()
		require.Len(t, s, 20, "RandomString length should be 20")
		require.False(t, strings.HasPrefix(s, "-"), "RandomString never starts with a dash '-'")
	}
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

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name      string
		filePath  string
		separator string
		want      []string
	}{
		{name: `empty`, filePath: ``, separator: `/`, want: nil},
		{name: `only separators`, filePath: `///`, separator: `/`, want: nil},
		{name: `single element`, filePath: `dir`, separator: `/`, want: []string{`dir`}},
		{name: `multiple elements`, filePath: `a/b/c`, separator: `/`, want: []string{`a`, `b`, `c`}},
		{name: `leading and trailing separators trimmed`, filePath: `/a/b/`, separator: `/`, want: []string{`a`, `b`}},
		{name: `backslash separator`, filePath: `\a\b\`, separator: `\`, want: []string{`a`, `b`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SplitPath(tt.filePath, tt.separator))
		})
	}
}

func TestMatchAnyPattern(t *testing.T) {
	tests := []struct {
		name     string
		match    string
		patterns []string
		want     bool
	}{
		{name: `no patterns matches anything`, match: `anything.txt`, patterns: nil, want: true},
		{name: `exact match`, match: `file.txt`, patterns: []string{`file.txt`}, want: true},
		{name: `wildcard match`, match: `file.txt`, patterns: []string{`*.txt`}, want: true},
		{name: `no match`, match: `file.txt`, patterns: []string{`*.go`}, want: false},
		{name: `second pattern matches`, match: `file.go`, patterns: []string{`*.txt`, `*.go`}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchAnyPattern(tt.match, tt.patterns)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	// An invalid pattern returns the path.Match error
	_, err := MatchAnyPattern(`file.txt`, []string{`[`})
	require.Error(t, err)
}

func TestEscapeURIPath(t *testing.T) {
	// Every path has to survive the round trip through a URI,
	// especially paths that already look URL escaped.
	for _, filePath := range []string{
		"",
		"/",
		"/a/b.txt",
		"/report 1.pdf",
		"/report%201.pdf",
		"/100%25.txt",
		"/50% off.pdf",
		"/%",
		"/%%",
		"/%zz",
		"/a%2Fb",
		`/say "hi"`,
		"/Ünïcödé 文件",
	} {
		assert.Equal(t, filePath, UnescapeURIPath(EscapeURIPath(filePath)), "round trip of %q", filePath)
	}

	assert.Equal(t, "/100%2525 a b.txt", EscapeURIPath("/100%25 a b.txt"), "only the escape character is escaped")
	assert.Equal(t, "/a b/c", UnescapeURIPath("/a%20b%2Fc"))
	assert.Equal(t, "/50% off", UnescapeURIPath("/50% off"), "a path with an invalid escape is returned unchanged")
}
