package fsimpl

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathHelper_CleanPath(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	backslash := PathHelper{URIPrefix: "mem://1", PathSep: `\`, Rooted: true}
	unrooted := PathHelper{URIPrefix: "http://"}
	volume := PathHelper{
		URIPrefix: "mem://2",
		PathSep:   `\`,
		Rooted:    true,
		VolumeLen: func(p string) int {
			if len(p) >= 2 && p[1] == ':' {
				return 2
			}
			return 0
		},
	}
	alt := PathHelper{URIPrefix: "sftp://user@host", AltPrefixes: []string{"sftp://user@host:22"}, Rooted: true}

	tests := []struct {
		name string
		h    PathHelper
		in   []string
		want string
	}{
		{"rooted join", rooted, []string{"a", "b", "c.txt"}, "/a/b/c.txt"},
		{"rooted clean", rooted, []string{"/a/", "skip", "..", "b", "/"}, "/a/b"},
		{"rooted strips prefix", rooted, []string{"s3://bucket/a/b"}, "/a/b"},
		{"rooted prefix and parts", rooted, []string{"s3://bucket/a", "b"}, "/a/b"},
		{"rooted empty", rooted, nil, "/"},
		{"rooted root", rooted, []string{"/"}, "/"},
		{"rooted no unescape", rooted, []string{"a%20b"}, "/a%20b"},
		{"backslash join", backslash, []string{"a", "b"}, `\a\b`},
		{"backslash clean", backslash, []string{`\a\..\b\\c`}, `\b\c`},
		{"backslash prefix", backslash, []string{`mem://1\a\b`}, `\a\b`},
		{"unrooted host", unrooted, []string{"example.com", "a", "..", "b.txt"}, "example.com/b.txt"},
		{"unrooted strips prefix", unrooted, []string{"http://example.com/x"}, "example.com/x"},
		{"unrooted leading slash", unrooted, []string{"/example.com/x"}, "example.com/x"},
		{"unrooted empty", unrooted, nil, ""},
		{"volume kept", volume, []string{`C:\a\..\b`}, `C:\b`},
		{"volume join", volume, []string{"C:", "a", "b"}, `C:\a\b`},
		{"alt prefix", alt, []string{"sftp://user@host:22/a/b"}, "/a/b"},
		{"main prefix", alt, []string{"sftp://user@host/a/b"}, "/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.in)
			got := tt.h.CleanPath(tt.in...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, in, tt.in, "CleanPath must not modify the passed slice")
			assert.Equal(t, got, tt.h.CleanPath(got), "CleanPath must be idempotent")
		})
	}
}

func TestPathHelper_URL(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.Equal(t, "s3://bucket/a/b", rooted.URL("/a/b"))
	assert.Equal(t, "s3://bucket/a/b", rooted.JoinCleanURI("a", "b"))
	assert.Equal(t, "/a/b", rooted.CleanPathFromURI(rooted.URL("/a/b")), "CleanPathFromURI(URL(p)) == p")

	slashPrefix := PathHelper{URIPrefix: "sftp://", Rooted: true}
	assert.Equal(t, "sftp://host/a", slashPrefix.URL("/host/a"), "no doubled separator after a prefix ending with a separator")
	assert.Equal(t, "/host/a", slashPrefix.CleanPathFromURI("sftp://host/a"))

	unrooted := PathHelper{URIPrefix: "http://"}
	assert.Equal(t, "http://example.com/a", unrooted.URL("example.com/a"))
	assert.Equal(t, "http://example.com/a", unrooted.JoinCleanURI("example.com", "a"))
	assert.Equal(t, "example.com/a", unrooted.CleanPathFromURI("http://example.com/a"))
}

func TestPathHelper_Split(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.Equal(t, []string{"a", "b", "c.txt"}, rooted.SplitPath("/a/b/c.txt"))
	assert.Equal(t, []string{"a", "b", "c.txt"}, rooted.SplitPath("s3://bucket/a/b/c.txt"))
	assert.Nil(t, rooted.SplitPath("/"))
	dir, name := rooted.SplitDirAndName("/a/b/c.txt")
	assert.Equal(t, "/a/b", dir)
	assert.Equal(t, "c.txt", name)
	dir, name = rooted.SplitDirAndName("/")
	assert.Equal(t, "/", dir)
	assert.Equal(t, "", name)

	volume := PathHelper{URIPrefix: "mem://2", PathSep: `\`, Rooted: true, VolumeLen: func(string) int { return 2 }}
	assert.Equal(t, []string{"a", "b"}, volume.SplitPath(`C:\a\b`))
	dir, name = volume.SplitDirAndName(`C:\a\b`)
	assert.Equal(t, `C:\a`, dir)
	assert.Equal(t, "b", name)
}

func TestPathHelper_AbsPath(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.True(t, rooted.IsAbsPath("/a"))
	assert.False(t, rooted.IsAbsPath("a"))
	assert.Equal(t, "/a/b", rooted.AbsPath("a/../a/b"))
	assert.Equal(t, "/a/b", rooted.AbsPath(rooted.AbsPath("a/b")), "AbsPath must be idempotent")

	unrooted := PathHelper{URIPrefix: "http://"}
	assert.True(t, unrooted.IsAbsPath("http://example.com/a"))
	assert.False(t, unrooted.IsAbsPath("example.com/a"))
	assert.Equal(t, "http://example.com/a", unrooted.AbsPath("example.com/a"))
	assert.Equal(t, "http://example.com/a", unrooted.AbsPath("http://example.com/a"))
}

func TestPathHelper_IsHidden(t *testing.T) {
	h := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.True(t, h.IsHidden("/a/.hidden"))
	assert.True(t, h.IsHidden(".hidden"))
	assert.False(t, h.IsHidden("/a/visible"))
	assert.False(t, h.IsHidden("/.a/visible"))
	assert.False(t, h.IsHidden("/"))
}

func TestPathHelper_Defaults(t *testing.T) {
	h := PathHelper{URIPrefix: "x://"}
	require.Equal(t, "/", h.Separator())
	require.Equal(t, "x://", h.Prefix())
	matched, err := h.MatchAnyPattern("a.txt", []string{"*.txt"})
	require.NoError(t, err)
	require.True(t, matched)
}
