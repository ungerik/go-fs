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
	assert.Equal(t, string(filepath.Separator), fsPath)

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

// TestParseRawURI_SchemeNotLocal verifies that a URI carrying a scheme
// ("://") which matches no registered file system does NOT silently resolve
// to the local file system, except for the local file:// scheme itself.
func TestParseRawURI_SchemeNotLocal(t *testing.T) {
	// Unregistered remote schemes must not become local paths.
	for _, uri := range []string{
		"s3://bucket/key",
		"http://example.com/file.txt",
		"https://example.com/file.txt",
		"ftp://user@host/path",
		"sftp://user@host/path",
		"dropbox://something/file",
	} {
		fs, _ := ParseRawURI(uri)
		assert.Equalf(t, Invalid, fs, "unregistered scheme %q must resolve to the invalid file system, not local", uri)
	}

	// The local file:// scheme resolves to the local file system.
	fs, fsPath := ParseRawURI(LocalPrefix + "/home/user/file.txt")
	assert.Equal(t, Local, fs, "file:// must resolve to the local file system")
	assert.Equal(t, Local.CleanPath(LocalPrefix+"/home/user/file.txt"), fsPath)

	// Plain paths without a scheme resolve to the local file system.
	for _, uri := range []string{
		"/home/user/file.txt",
		"relative/path.txt",
		"file.txt",
	} {
		fs, fsPath := ParseRawURI(uri)
		assert.Equalf(t, Local, fs, "scheme-less path %q must resolve to the local file system", uri)
		assert.Equal(t, filepath.FromSlash(uri), fsPath)
	}
}

func TestParseRawURI_escapes(t *testing.T) {
	// The prefix can contain escapes itself, like the user name of an
	// smbfs prefix, so only the path after the prefix is decoded.
	escapedFS := InvalidFileSystem("user%40example.com")
	Register(escapedFS)
	t.Cleanup(func() { Unregister(escapedFS) })

	fs, fsPath := ParseRawURI(escapedFS.Prefix() + "a%20b/100%2525.txt")
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "a b/100%25.txt", fsPath, "URL escapes after the prefix are decoded once")

	fs, fsPath = ParseRawURI(escapedFS.Prefix() + "50% off.txt")
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "50% off.txt", fsPath, "a path with an invalid escape is used as it is")

	// The URI of a path is its inverse, so a name that looks
	// URL escaped keeps its name through File methods.
	file := fsFile(escapedFS, "a b/100%25.txt")
	assert.Equal(t, escapedFS.Prefix()+"a b/100%2525.txt", string(file))
	fs, fsPath = file.ParseRawURI()
	assert.Equal(t, escapedFS, fs)
	assert.Equal(t, "a b/100%25.txt", fsPath)
	assert.Equal(t, "100%25.txt", file.Name())
	assert.Equal(t, fsFile(escapedFS, "a b"), file.Dir())
}
