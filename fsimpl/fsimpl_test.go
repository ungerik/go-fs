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

func TestJoinCleanPath(t *testing.T) {
	type args struct {
		uriParts   []string
		trimPrefix string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: `empty`,
			args: args{uriParts: nil, trimPrefix: ``},
			want: `/`,
		},
		{
			name: `dot`,
			args: args{uriParts: []string{`.`}, trimPrefix: ``},
			want: `/`,
		},
		{
			name: `relative is made absolute`,
			args: args{uriParts: []string{`relative`}, trimPrefix: ``},
			want: `/relative`,
		},
		{
			name: `already absolute`,
			args: args{uriParts: []string{`/a/b`}, trimPrefix: ``},
			want: `/a/b`,
		},
		{
			name: `joins parts and collapses trailing slash`,
			args: args{uriParts: []string{`a/b/`, `./c/`}, trimPrefix: ``},
			want: `/a/b/c`,
		},
		{
			name: `resolves dot dot`,
			args: args{uriParts: []string{`a/b`, `../c`}, trimPrefix: ``},
			want: `/a/c`,
		},
		{
			name: `trims prefix from first part`,
			args: args{uriParts: []string{`myprefix/a`, `b`}, trimPrefix: `myprefix`},
			want: `/a/b`,
		},
		{
			name: `url unescapes`,
			args: args{uriParts: []string{`a%20b/c`}, trimPrefix: ``},
			want: `/a b/c`,
		},
		{
			name: `ftp scheme prefix`,
			args: args{uriParts: []string{`ftp://example.com/dir/`, `./subdir/`}, trimPrefix: `ftp://`},
			want: `/example.com/dir/subdir`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinCleanPath(tt.args.uriParts, tt.args.trimPrefix); got != tt.want {
				t.Errorf("JoinCleanPath(%#v, %#v) = %#v, want %#v", tt.args.uriParts, tt.args.trimPrefix, got, tt.want)
			}
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name      string
		filePath  string
		prefix    string
		separator string
		want      []string
	}{
		{name: `empty`, filePath: ``, prefix: ``, separator: `/`, want: nil},
		{name: `only separators`, filePath: `///`, prefix: ``, separator: `/`, want: nil},
		{name: `single element`, filePath: `dir`, prefix: ``, separator: `/`, want: []string{`dir`}},
		{name: `multiple elements`, filePath: `a/b/c`, prefix: ``, separator: `/`, want: []string{`a`, `b`, `c`}},
		{name: `leading and trailing separators trimmed`, filePath: `/a/b/`, prefix: ``, separator: `/`, want: []string{`a`, `b`}},
		{name: `prefix trimmed`, filePath: `file:///a/b`, prefix: `file://`, separator: `/`, want: []string{`a`, `b`}},
		{name: `backslash separator`, filePath: `\a\b\`, prefix: ``, separator: `\`, want: []string{`a`, `b`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, SplitPath(tt.filePath, tt.prefix, tt.separator))
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
