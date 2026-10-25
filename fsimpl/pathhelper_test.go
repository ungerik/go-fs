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

func TestPathHelper_URI(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.Equal(t, "s3://bucket/a/b", rooted.URI("/a/b"))
	assert.Equal(t, "s3://bucket/a/b", rooted.JoinCleanURI("a", "b"))
	assert.Equal(t, "/a/b", rooted.CleanPath(rooted.URI("/a/b")), "CleanPath(URI(p)) == p")
	assert.Equal(t, "s3://bucket/100%2525%2520a.txt", rooted.URI("/100%25%20a.txt"), "the URI escapes every %")
	assert.Equal(t, "s3://bucket/100%2525%2520a.txt", rooted.JoinCleanURI("100%25%20a.txt"), "URI parts are joined as path elements")

	// A prefix that is only a scheme is followed by the whole rooted path,
	// the URI of "/a" used to be "file://a" with the host "a".
	schemePrefix := PathHelper{URIPrefix: "file://", Rooted: true}
	assert.Equal(t, "file:///a", schemePrefix.URI("/a"))
	assert.Equal(t, "file:///a", schemePrefix.JoinCleanURI("a"))
	assert.Equal(t, "/a", schemePrefix.CleanPath(schemePrefix.URI("/a")), "CleanPath(URI(p)) == p")
	assert.Equal(t, "file:///", schemePrefix.URI("/"))

	slashPrefix := PathHelper{URIPrefix: "mem://id/", Rooted: true}
	assert.Equal(t, "mem://id/a", slashPrefix.URI("/a"), "no doubled separator after a host prefix ending with a separator")
	assert.Equal(t, "/a", slashPrefix.CleanPath(slashPrefix.URI("/a")), "CleanPath(URI(p)) == p")
	assert.Equal(t, "mem://id/a%25b", slashPrefix.URI("/a%b"))

	unrooted := PathHelper{URIPrefix: "http://"}
	assert.Equal(t, "http://example.com/a", unrooted.URI("example.com/a"))
	assert.Equal(t, "http://example.com/a", unrooted.JoinCleanURI("example.com", "a"))
	assert.Equal(t, "example.com/a", unrooted.CleanPath("http://example.com/a"))
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
	dir, name = volume.SplitDirAndName(`C:\a`)
	assert.Equal(t, `C:\`, dir, "the parent of a name below the volume root is the volume root")
	assert.Equal(t, "a", name)
	dir, name = volume.SplitDirAndName(`C:\`)
	assert.Equal(t, `C:\`, dir)
	assert.Equal(t, "", name, "the volume root has no name")
}

// TestPathHelper_SplitWithPrefix verifies that SplitDirAndName strips the
// URI prefix like SplitPath does. It used to keep the prefix, so the
// volume of the path was not found and the prefix ended up in the dir.
func TestPathHelper_SplitWithPrefix(t *testing.T) {
	rooted := PathHelper{URIPrefix: "s3://bucket", Rooted: true}
	assert.Equal(t, []string{"a", "b.txt"}, rooted.SplitPath("s3://bucket/a/b.txt"))
	dir, name := rooted.SplitDirAndName("s3://bucket/a/b.txt")
	assert.Equal(t, "/a", dir)
	assert.Equal(t, "b.txt", name)
	dir, name = rooted.SplitDirAndName("s3://bucket/")
	assert.Equal(t, "/", dir)
	assert.Equal(t, "", name)

	volume := PathHelper{
		URIPrefix: "mem://x",
		PathSep:   `\`,
		Rooted:    true,
		VolumeLen: func(p string) int {
			if len(p) >= 2 && p[1] == ':' {
				return 2
			}
			return 0
		},
	}
	assert.Equal(t, []string{"a", "b"}, volume.SplitPath(`mem://xC:\a\b`))
	dir, name = volume.SplitDirAndName(`mem://xC:\a\b`)
	assert.Equal(t, `C:\a`, dir)
	assert.Equal(t, "b", name)
	dir, name = volume.SplitDirAndName(`mem://xC:\a`)
	assert.Equal(t, `C:\`, dir)
	assert.Equal(t, "a", name)
}

func TestPathHelper_Defaults(t *testing.T) {
	h := PathHelper{URIPrefix: "x://"}
	require.Equal(t, "/", h.Separator())
	require.Equal(t, "x://", h.Prefix())
}
