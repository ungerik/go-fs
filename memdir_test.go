package fs

import (
	"bytes"
	"encoding/gob"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemDir_Name(t *testing.T) {
	tests := []struct {
		dir  MemDir
		want string
	}{
		{dir: "", want: ""},
		{dir: "dir", want: "dir"},
		{dir: "some/path/dir", want: "dir"},
		{dir: "some\\path\\dir", want: "some\\path\\dir"}, // backslash is a literal character
		{dir: "some/path/dir/", want: "dir"},
		{dir: "some/path/dir///", want: "dir"},
		{dir: "/", want: ""},
		{dir: "/dir", want: "dir"},
		{dir: "/some/path/dir", want: "dir"},
		{dir: "/some/path/dir/", want: "dir"},
		{dir: "/some/path/dir///", want: "dir"},
	}
	for _, tt := range tests {
		t.Run(string(tt.dir), func(t *testing.T) {
			if got := tt.dir.Name(); got != tt.want {
				t.Errorf("MemDir.Name() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemDir_Ext(t *testing.T) {
	tests := []struct {
		dir  MemDir
		want string
	}{
		{dir: "", want: ""},
		{dir: "dir", want: ""},
		{dir: "some/path/dir", want: ""},
		{dir: "some.ext/dir", want: ""},
		{dir: "some.ext/dir/", want: ""},
		{dir: "some/my.dir", want: ".dir"},
		{dir: "some/my.dir/", want: ".dir"},
		{dir: "/some/my.dir", want: ".dir"},  // leading slash, ext unaffected
		{dir: "/some/my.dir/", want: ".dir"}, // leading slash kept, trailing ignored
	}
	for _, tt := range tests {
		t.Run(string(tt.dir), func(t *testing.T) {
			if got := tt.dir.Ext(); got != tt.want {
				t.Errorf("MemDir.Ext() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemDir_Dir(t *testing.T) {
	tests := []struct {
		path string
		want MemDir
	}{
		{path: "", want: ""},
		{path: "dir", want: ""},                       // no slash
		{path: "some/path/dir", want: "some/path"},    // parent path
		{path: "some\\path\\dir", want: ""},           // backslash is a literal character, no slash
		{path: "/dir", want: "/"},                     // root
		{path: "/", want: ""},                         // Dir of root "/"
		{path: "/some/path", want: "/some"},           // absolute parent path
		{path: "some/path/dir/", want: "some/path"},   // trailing slash ignored
		{path: "/dir/", want: "/"},                    // leading slash kept, trailing ignored
		{path: "/some/path/dir/", want: "/some/path"}, // leading slash kept, trailing ignored
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, MemDir(tt.path).Dir())
		})
	}
}

func TestMemDir_DirAndName(t *testing.T) {
	tests := []struct {
		path     string
		wantDir  MemDir
		wantName string
	}{
		{path: "", wantDir: "", wantName: ""},
		{path: "dir", wantDir: "", wantName: "dir"},
		{path: "some/path/dir", wantDir: "some/path", wantName: "dir"},
		{path: "some\\path\\dir", wantDir: "", wantName: "some\\path\\dir"}, // backslash is a literal character
		{path: "/dir", wantDir: "/", wantName: "dir"},
		{path: "/", wantDir: "", wantName: ""},
		{path: "some/path/dir/", wantDir: "some/path", wantName: "dir"},   // trailing slash ignored
		{path: "/some/path/dir/", wantDir: "/some/path", wantName: "dir"}, // leading slash kept, trailing ignored
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			dir, name := MemDir(tt.path).DirAndName()
			require.Equal(t, tt.wantDir, dir)
			require.Equal(t, tt.wantName, name)
		})
	}
}

func TestMemDir_CleanPath(t *testing.T) {
	tests := []struct {
		dir  MemDir
		want MemDir
	}{
		{dir: "", want: ""},
		{dir: "dir", want: "dir"},
		{dir: "a/b/../c", want: "a/c"},
		{dir: "a/./b", want: "a/b"},
		{dir: "./a", want: "a"},
		{dir: "a/b/", want: "a/b"},   // trailing separator removed
		{dir: "a/b///", want: "a/b"}, // trailing separators removed
		{dir: "a//b", want: "a/b"},   // redundant separator removed
		{dir: "/a/../b", want: "/b"}, // leading separator kept
		{dir: "a/b/../../c", want: "c"},
		{dir: "/dir", want: "/dir"},
		{dir: "/a/b/../c", want: "/a/c"},
		{dir: "/a/./b", want: "/a/b"},
		{dir: "/./a", want: "/a"},
		{dir: "/a/b/", want: "/a/b"},   // trailing separator removed
		{dir: "/a/b///", want: "/a/b"}, // trailing separators removed
		{dir: "/a//b", want: "/a/b"},   // redundant separator removed
		{dir: "//a/../b", want: "/b"},  // leading separator kept
		{dir: "/a/b/../../c", want: "/c"},
		{dir: "/", want: ""},                        // trailing slash trimmed, even root
		{dir: "a\\b\\..\\c", want: "a\\b\\..\\c"},   // backslash is literal: nothing to resolve
		{dir: "a\\b\\", want: "a\\b\\"},             // backslash is literal: not a trailing separator
		{dir: "a/b\\..\\c", want: "a/b\\..\\c"},     // only '/' splits, so '\' stays literal
		{dir: "/a\\b\\..\\c", want: "/a\\b\\..\\c"}, // backslash is literal: nothing to resolve
		{dir: "/a\\b\\", want: "/a\\b\\"},           // backslash is literal: not a trailing separator
		{dir: "/a/b\\..\\c", want: "/a/b\\..\\c"},   // only '/' splits, so '\' stays literal
	}
	for _, tt := range tests {
		t.Run(string(tt.dir), func(t *testing.T) {
			require.Equal(t, tt.want, tt.dir.CleanPath())
		})
	}
}

func TestMemDir_Join(t *testing.T) {
	tests := []struct {
		dir   MemDir
		parts []string
		want  MemDir
	}{
		{dir: "", parts: nil, want: ""},
		{dir: "dir", parts: nil, want: "dir"},
		{dir: "a/b", parts: []string{"c", "d"}, want: "a/b/c/d"},
		{dir: "a", parts: []string{"b"}, want: "a/b"},
		{dir: "", parts: []string{"a", "b"}, want: "a/b"},
		{dir: "/", parts: []string{"a"}, want: "/a"},
		{dir: "/a", parts: []string{"b", "c"}, want: "/a/b/c"},
		// backslash is a literal character, '/' is always the separator
		{dir: "a\\b", parts: []string{"c", "d"}, want: "a\\b/c/d"},
		{dir: "a", parts: []string{"b\\c"}, want: "a/b\\c"},
		// boundary slashes are collapsed, empty parts skipped
		{dir: "a/b/", parts: []string{"c"}, want: "a/b/c"},
		{dir: "a", parts: []string{"", "b", ""}, want: "a/b"},
		{dir: "a", parts: []string{"/b/", "/c/"}, want: "a/b/c"},
		// leading slash kept, trailing slash on receiver ignored
		{dir: "/a/b", parts: []string{"c", "d"}, want: "/a/b/c/d"},
		{dir: "/a/b/", parts: []string{"c"}, want: "/a/b/c"},
		{dir: "/a/b/", parts: nil, want: "/a/b"},
	}
	for _, tt := range tests {
		t.Run(string(tt.dir), func(t *testing.T) {
			require.Equal(t, tt.want, tt.dir.Join(tt.parts...))
		})
	}
}

func TestMemDir_IsDir(t *testing.T) {
	require.True(t, MemDir("").IsDir())
	require.True(t, MemDir("dir").IsDir())

	// Implements FileReader as a directory
	var fileReader FileReader = MemDir("dir")
	require.True(t, fileReader.IsDir())
	require.NoError(t, fileReader.CheckIsDir())
}

func TestMemDir_Exists(t *testing.T) {
	require.False(t, MemDir("").Exists())
	require.True(t, MemDir("dir").Exists())

	require.ErrorIs(t, MemDir("").CheckExists(), os.ErrNotExist)
	require.NoError(t, MemDir("dir").CheckExists())
}

func TestMemDir_CheckIsDir(t *testing.T) {
	require.ErrorIs(t, MemDir("").CheckIsDir(), os.ErrNotExist)
	require.NoError(t, MemDir("dir").CheckIsDir())
}

func TestMemDir_Size(t *testing.T) {
	require.Equal(t, int64(0), MemDir("dir").Size())
}

func TestMemDir_ContentHash(t *testing.T) {
	// A directory has no content hash, matching File.ContentHash
	hash, err := MemDir("dir").ContentHash()
	require.NoError(t, err)
	require.Equal(t, "", hash)

	hash, err = MemDir("dir").ContentHashContext(t.Context())
	require.NoError(t, err)
	require.Equal(t, "", hash)
}

func TestMemDir_ReadFails(t *testing.T) {
	d := MemDir("dir")
	ctx := t.Context()

	isDirErr := func(err error) {
		t.Helper()
		require.Error(t, err)
		var errIsDir ErrIsDirectory
		require.ErrorAs(t, err, &errIsDir)
	}

	_, err := d.ReadAll()
	isDirErr(err)

	_, err = d.ReadAllContext(ctx)
	isDirErr(err)

	_, _, err = d.ReadAllContentHash(ctx)
	isDirErr(err)

	_, err = d.ReadAllString()
	isDirErr(err)

	_, err = d.ReadAllStringContext(ctx)
	isDirErr(err)

	_, err = d.OpenReader()
	isDirErr(err)

	_, err = d.OpenReadSeeker()
	isDirErr(err)

	_, err = d.WriteTo(new(bytes.Buffer))
	isDirErr(err)

	isDirErr(d.ReadJSON(ctx, new(any)))
	isDirErr(d.ReadXML(ctx, new(any)))
}

func TestMemDir_GobEncodeDecode(t *testing.T) {
	tests := []MemDir{"", "dir", "some/path/dir"}
	for _, dir := range tests {
		t.Run(string(dir), func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, gob.NewEncoder(&buf).Encode(dir))

			var decoded MemDir
			require.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))
			require.Equal(t, dir, decoded)
		})
	}
}

func TestMemDir_Stat(t *testing.T) {
	info, err := MemDir("some/path/dir").Stat()
	require.NoError(t, err)
	require.Equal(t, "dir", info.Name())
	require.Equal(t, int64(0), info.Size())
	require.True(t, info.IsDir())
	require.True(t, info.Mode().IsDir())
}
