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
		separator  string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: `empty`,
			args: args{uriParts: nil, trimPrefix: ``, separator: ``},
			want: `.`,
		},
		{
			name: `. with sep`,
			args: args{uriParts: []string{`.`}, trimPrefix: ``, separator: `/`},
			want: `/`,
		},
		{
			name: `. without sep`,
			args: args{uriParts: []string{`.`}, trimPrefix: ``, separator: ``},
			want: `.`,
		},
		{
			name: `relative no sep`,
			args: args{uriParts: []string{`relative`}, trimPrefix: ``, separator: ``},
			want: `relative`,
		},
		{
			name: `relative with sep`,
			args: args{uriParts: []string{`relative`}, trimPrefix: ``, separator: `/`},
			want: `/relative`,
		},
		{
			name: `C:`,
			args: args{uriParts: nil, trimPrefix: `C:`, separator: `\`},
			want: `\`,
		},
		{
			name: `C:\`,
			args: args{uriParts: nil, trimPrefix: `C:\`, separator: `\`},
			want: `\`,
		},
		{
			name: `C:\\`,
			args: args{uriParts: nil, trimPrefix: `C:\`, separator: `\`},
			want: `\`,
		},
		{
			name: `weird C:\ with / sep`,
			args: args{uriParts: nil, trimPrefix: `C:\`, separator: `/`},
			want: `/`,
		},
		{
			name: `ftp://example.com/dir/subdir/`,
			args: args{uriParts: []string{`ftp://example.com/dir/`, `./subdir/`}, trimPrefix: `ftp://`, separator: `/`},
			want: `/example.com/dir/subdir`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinCleanPath(tt.args.uriParts, tt.args.trimPrefix, tt.args.separator); got != tt.want {
				t.Errorf("JoinCleanPath(%#v, %#v, %#v) = %#v, want %#v", tt.args.uriParts, tt.args.trimPrefix, tt.args.separator, got, tt.want)
			}
		})
	}
}
