package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"
)

// writeFiles writes the files of the map from slash separated
// paths to contents below dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
}

// newTree returns a temporary directory with the files
// x.txt, .hidden and sub/y.txt.
func newTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"x.txt":     "hello\n",
		".hidden":   "hidden",
		"sub/y.txt": "deep",
	})
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //#nosec G304 -- test file
	require.NoError(t, err)
	return string(data)
}

func TestLs(t *testing.T) {
	dir := newTree(t)

	code, stdout, stderr := gofs(t, nil, "", "ls", dir)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "sub\nx.txt\n", stdout, "sorted, without hidden files")

	code, stdout, _ = gofs(t, nil, "", "ls", "-a", dir)
	require.Equal(t, 0, code)
	assert.Equal(t, ".hidden\nsub\nx.txt\n", stdout)

	code, stdout, _ = gofs(t, nil, "", "ls", "-r", dir)
	require.Equal(t, 0, code)
	assert.Equal(t, filepath.FromSlash("sub/y.txt")+"\nx.txt\n", stdout, "only files, with their path")

	writeFiles(t, dir, map[string]string{".git/config": "hidden dir"})
	code, stdout, _ = gofs(t, nil, "", "ls", "-r", dir)
	require.Equal(t, 0, code)
	assert.Equal(t, filepath.FromSlash("sub/y.txt")+"\nx.txt\n", stdout, "without the files of hidden directories")
	code, stdout, _ = gofs(t, nil, "", "ls", "-r", "-a", dir)
	require.Equal(t, 0, code)
	assert.Equal(t, ".git"+string(filepath.Separator)+"config\n.hidden\n"+filepath.FromSlash("sub/y.txt")+"\nx.txt\n", stdout)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".git")))

	code, stdout, _ = gofs(t, nil, "", "ls", "-u", dir)
	require.Equal(t, 0, code)
	assert.Equal(t, filepath.Join(dir, "sub")+"\n"+filepath.Join(dir, "x.txt")+"\n", stdout)

	code, stdout, _ = gofs(t, nil, "", "ls", "-l", dir)
	require.Equal(t, 0, code)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, 2, stdout)
	assert.Regexp(t, `^d\S+ +\d+ \d{4}-\d\d-\d\d \d\d:\d\d:\d\d  sub$`, lines[0])
	assert.Regexp(t, `^-rw\S+ +6 \d{4}-\d\d-\d\d \d\d:\d\d:\d\d  x.txt$`, lines[1])

	file := filepath.Join(dir, "x.txt")
	code, stdout, _ = gofs(t, nil, "", "ls", file)
	require.Equal(t, 0, code)
	assert.Equal(t, file+"\n", stdout, "a file is printed like it was passed")

	sub := filepath.Join(dir, "sub")
	code, stdout, _ = gofs(t, nil, "", "ls", sub, file, dir)
	require.Equal(t, 0, code)
	assert.Equal(t, sub+":\ny.txt\n"+file+"\n\n"+dir+":\nsub\nx.txt\n", stdout)

	code, _, stderr = gofs(t, nil, "", "ls", filepath.Join(dir, "missing"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "gofs ls: ")
}

func TestLs_WorkingDir(t *testing.T) {
	t.Chdir(newTree(t))
	code, stdout, _ := gofs(t, nil, "", "ls")
	require.Equal(t, 0, code)
	assert.Equal(t, "sub\nx.txt\n", stdout)
}

func TestStat(t *testing.T) {
	dir := newTree(t)
	code, stdout, stderr := gofs(t, nil, "", "stat", filepath.Join(dir, "x.txt"), dir)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Name:     x.txt\nType:     file\nSize:     6\n")
	assert.Contains(t, stdout, "\n\nURI:      file://")
	assert.Contains(t, stdout, "Type:     directory\n")
}

func TestPutCat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new", "file.txt")
	code, _, stderr := gofs(t, nil, "content", "put", file)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "content", readFile(t, file), "put creates missing parent directories")

	other := filepath.Join(dir, "other.txt")
	writeFiles(t, dir, map[string]string{"other.txt": "+other"})
	code, stdout, _ := gofs(t, nil, "", "cat", file, other)
	require.Equal(t, 0, code)
	assert.Equal(t, "content+other", stdout)

	code, _, stderr = gofs(t, nil, "", "cat", filepath.Join(dir, "missing"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "gofs cat: ")
}

func TestCp(t *testing.T) {
	src := newTree(t)
	dest := t.TempDir()

	// A file to a new file and into a directory
	code, _, stderr := gofs(t, nil, "", "cp", filepath.Join(src, "x.txt"), filepath.Join(dest, "copy.txt"))
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "hello\n", readFile(t, filepath.Join(dest, "copy.txt")))
	code, _, stderr = gofs(t, nil, "", "cp", filepath.Join(src, "x.txt"), dest)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "hello\n", readFile(t, filepath.Join(dest, "x.txt")))

	// A directory needs -r
	code, _, stderr = gofs(t, nil, "", "cp", src, filepath.Join(dest, "tree"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "is a directory, copy it with -r")

	// A directory to a new directory, then into the existing one
	tree := filepath.Join(dest, "tree")
	code, _, stderr = gofs(t, nil, "", "cp", "-r", src, tree)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "deep", readFile(t, filepath.Join(tree, "sub", "y.txt")))
	code, _, stderr = gofs(t, nil, "", "cp", "-r", filepath.Join(src, "sub"), tree)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "deep", readFile(t, filepath.Join(tree, "sub", "y.txt")))
	code, _, _ = gofs(t, nil, "", "cmp", "-r", src, tree)
	assert.Equal(t, 0, code, "the second copy merged into the existing sub-directory")

	// Several sources need a destination directory
	code, _, stderr = gofs(t, nil, "", "cp", filepath.Join(src, "x.txt"), filepath.Join(src, "sub", "y.txt"), filepath.Join(dest, "missing"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "is not a directory")
	code, _, stderr = gofs(t, nil, "", "cp", filepath.Join(src, "x.txt"), filepath.Join(dest, "missing")+"/")
	assert.Equal(t, 1, code, "a trailing slash means a directory")
	assert.Contains(t, stderr, "is not a directory")
	multi := filepath.Join(dest, "multi")
	require.NoError(t, os.Mkdir(multi, 0o750))
	code, _, stderr = gofs(t, nil, "", "cp", "-r", filepath.Join(src, "x.txt"), filepath.Join(src, "sub"), multi)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "hello\n", readFile(t, filepath.Join(multi, "x.txt")))
	assert.Equal(t, "deep", readFile(t, filepath.Join(multi, "sub", "y.txt")))
}

func TestMv(t *testing.T) {
	dir := newTree(t)
	code, _, stderr := gofs(t, nil, "", "mv", filepath.Join(dir, "x.txt"), filepath.Join(dir, "renamed.txt"))
	require.Equal(t, 0, code, stderr)
	assert.NoFileExists(t, filepath.Join(dir, "x.txt"))
	assert.Equal(t, "hello\n", readFile(t, filepath.Join(dir, "renamed.txt")))

	code, _, stderr = gofs(t, nil, "", "mv", filepath.Join(dir, "renamed.txt"), filepath.Join(dir, ".hidden"), filepath.Join(dir, "sub"))
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "hello\n", readFile(t, filepath.Join(dir, "sub", "renamed.txt")))
	assert.Equal(t, "hidden", readFile(t, filepath.Join(dir, "sub", ".hidden")))

	code, _, stderr = gofs(t, nil, "", "mv", filepath.Join(dir, "missing"), filepath.Join(dir, "x"))
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "gofs mv: ")
}

func TestRm(t *testing.T) {
	dir := newTree(t)
	missing := filepath.Join(dir, "missing")

	code, _, stderr := gofs(t, nil, "", "rm", filepath.Join(dir, "x.txt"))
	require.Equal(t, 0, code, stderr)
	assert.NoFileExists(t, filepath.Join(dir, "x.txt"))

	code, _, _ = gofs(t, nil, "", "rm", missing)
	assert.Equal(t, 1, code)
	code, _, _ = gofs(t, nil, "", "rm", "-f", missing)
	assert.Equal(t, 0, code)

	code, _, _ = gofs(t, nil, "", "rm", filepath.Join(dir, "sub"))
	assert.Equal(t, 1, code, "a directory that is not empty needs -r")
	assert.DirExists(t, filepath.Join(dir, "sub"))

	code, _, _ = gofs(t, nil, "", "rm", "-r", missing)
	assert.Equal(t, 1, code, "rm -r of a missing file fails without -f")

	// Not filepath.Join, which cleans the dots away.
	// The file systems decode the path of a URI before they clean it.
	sub := filepath.Join(dir, "sub")
	subURI := "file://" + filepath.ToSlash(sub)
	if !strings.HasPrefix(subURI, "file:///") {
		subURI = "file:///" + strings.TrimPrefix(subURI, "file://") // C:/... on Windows
	}
	for _, arg := range []string{".", sub + "/..", sub + "/./", subURI + "/%2E%2E", subURI + "/.%2e", subURI + "%2F..", subURI + "/%2e/"} {
		code, _, stderr = gofs(t, nil, "", "rm", "-rf", arg)
		assert.Equal(t, 1, code, arg)
		assert.Contains(t, stderr, "refusing to remove", arg)
	}
	assert.FileExists(t, filepath.Join(sub, "y.txt"), "nothing removed")
	code, _, stderr = gofs(t, nil, "", "rm", "-rf", filepath.Join(dir, "sub"), missing)
	require.Equal(t, 0, code, stderr)
	assert.NoDirExists(t, filepath.Join(dir, "sub"))
}

// failingReader returns data, then err.
type failingReader struct {
	data string
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// runStdin runs the command line args with ctx and stdin and returns
// the exit code and stderr.
func runStdin(ctx context.Context, env map[string]string, stdin io.Reader, args ...string) (code int, stderr string) {
	var out, errOut strings.Builder
	code = run(ctx, args, stdin, &out, &errOut, func(name string) string { return env[name] })
	return code, errOut.String()
}

// TestPut_Failure checks that put keeps an existing file when stdin fails
// or the command is canceled, also on the local file system, whose writer
// truncates the file and can't abort, and leaves no partial new file.
func TestPut_Failure(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	writeFiles(t, dir, map[string]string{"existing.txt": "original"})
	readErr := errors.New("stdin broke")

	code, stderr := runStdin(t.Context(), nil, &failingReader{"partial", readErr}, "put", existing)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "stdin broke")
	assert.Equal(t, "original", readFile(t, existing), "stdin failed")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	code, _ = runStdin(ctx, nil, strings.NewReader("new"), "put", existing)
	assert.Equal(t, 1, code)
	assert.Equal(t, "original", readFile(t, existing), "canceled")

	require.NoError(t, os.Chmod(existing, 0o640))
	code, stderr = runStdin(t.Context(), nil, strings.NewReader("replaced"), "put", existing)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "replaced", readFile(t, existing), "an existing file is replaced")
	if runtime.GOOS != "windows" {
		info, err := os.Stat(existing)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "the permissions are kept")
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file left behind")

	// A directory fails before stdin is read
	stdin := &failingReader{"never read", readErr}
	code, stderr = runStdin(t.Context(), nil, stdin, "put", dir)
	assert.Equal(t, 1, code)
	assert.NotContains(t, stderr, "stdin broke")
	assert.Equal(t, "never read", stdin.data)

	newFile := filepath.Join(dir, "new.txt")
	code, _ = runStdin(t.Context(), nil, &failingReader{"partial", readErr}, "put", newFile)
	assert.Equal(t, 1, code)
	assert.NoFileExists(t, newFile, "no partial new file")

	// WebDAV writers abort instead
	env := map[string]string{
		"GOFS_WEBDAV_URL":      newWebDAVServer(t).URL,
		"GOFS_WEBDAV_USER":     "alice",
		"GOFS_WEBDAV_PASSWORD": "secret",
	}
	code, _ = runStdin(t.Context(), env, &failingReader{"partial", readErr}, "put", "webdav:///new.txt")
	assert.Equal(t, 1, code)
	code, _, _ = gofs(t, env, "", "stat", "webdav:///new.txt")
	assert.Equal(t, 1, code, "the upload was aborted")

	code, stderr = runStdin(t.Context(), env, strings.NewReader("original"), "put", "webdav:///existing.txt")
	require.Equal(t, 0, code, stderr)
	code, _ = runStdin(t.Context(), env, &failingReader{"partial", readErr}, "put", "webdav:///existing.txt")
	assert.Equal(t, 1, code)
	code, stdout, _ := gofs(t, env, "", "cat", "webdav:///existing.txt")
	require.Equal(t, 0, code)
	assert.Equal(t, "original", stdout, "an aborted upload keeps the existing file")
	code, stdout, _ = gofs(t, env, "", "ls", "-a", "webdav:///")
	require.Equal(t, 0, code)
	assert.Equal(t, "existing.txt\n", stdout, "no temporary file left behind")
}

// TestPut_Replace checks that put keeps the permissions of an existing
// local file that it replaces, refuses a read-only file and writes a file
// directly when no temporary file can be created next to it.
func TestPut_Replace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permissions")
	}
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.txt")
	writeFiles(t, dir, map[string]string{"shared.txt": "old"})
	// A mode that the usual umask 022 would change
	require.NoError(t, os.Chmod(shared, 0o666))
	code, stderr := runStdin(t.Context(), nil, strings.NewReader("new"), "put", shared)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "new", readFile(t, shared))
	info, err := os.Stat(shared)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o666), info.Mode().Perm(), "the permissions are kept")

	readOnly := filepath.Join(dir, "read-only.txt")
	writeFiles(t, dir, map[string]string{"read-only.txt": "keep"})
	require.NoError(t, os.Chmod(readOnly, 0o444))
	code, stderr = runStdin(t.Context(), nil, strings.NewReader("new"), "put", readOnly)
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "permission")
	assert.Equal(t, "keep", readFile(t, readOnly), "a read-only file is not replaced")

	// A writable file in a directory where no file can be created
	locked := filepath.Join(dir, "locked")
	writeFiles(t, locked, map[string]string{"file.txt": "old"})
	require.NoError(t, os.Chmod(locked, 0o555))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	code, stderr = runStdin(t.Context(), nil, strings.NewReader("new"), "put", filepath.Join(locked, "file.txt"))
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "new", readFile(t, filepath.Join(locked, "file.txt")), "written directly")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".gofs-put-"), "temporary file left behind: %s", entry.Name())
	}
}

// TestPut_StatError checks that put writes nothing when it can't tell
// if the file exists, so it never truncates or removes an existing file.
func TestPut_StatError(t *testing.T) {
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken.txt" {
			if r.Method != "PROPFIND" {
				writes.Add(1)
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	code, stderr := runStdin(t.Context(), map[string]string{"GOFS_WEBDAV_URL": srv.URL}, strings.NewReader("new"), "put", "webdav:///broken.txt")
	assert.Equal(t, 1, code, stderr)
	assert.Zero(t, writes.Load(), "no PUT or DELETE after the failed stat")
}

func TestMkdirTouch(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b")

	code, _, _ := gofs(t, nil, "", "mkdir", deep)
	assert.Equal(t, 1, code, "the parent is missing")
	code, _, stderr := gofs(t, nil, "", "mkdir", "-p", deep)
	require.Equal(t, 0, code, stderr)
	assert.DirExists(t, deep)
	code, _, _ = gofs(t, nil, "", "mkdir", "-p", deep)
	assert.Equal(t, 0, code, "-p accepts an existing directory")
	code, _, _ = gofs(t, nil, "", "mkdir", deep)
	assert.Equal(t, 1, code)

	file := filepath.Join(deep, "empty")
	code, _, stderr = gofs(t, nil, "", "touch", file)
	require.Equal(t, 0, code, stderr)
	assert.Empty(t, readFile(t, file))

	// touch of an existing file updates the time and keeps the content
	existing := filepath.Join(deep, "existing")
	writeFiles(t, deep, map[string]string{"existing": "content"})
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(existing, past, past))
	code, _, stderr = gofs(t, nil, "", "touch", existing)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "content", readFile(t, existing))
	info, err := os.Stat(existing)
	require.NoError(t, err)
	assert.True(t, info.ModTime().After(past), "modification time updated")
}

func TestHash(t *testing.T) {
	dir := newTree(t)
	file := filepath.Join(dir, "x.txt")

	code, stdout, stderr := gofs(t, nil, "", "hash", "-a", "sha256", file)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  "+file+"\n", stdout)

	code, stdout, stderr = gofs(t, nil, "", "hash", "-a", "md5", file)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "b1946ac92492d2347c6235b4d2611184  "+file+"\n", stdout)

	code, stdout, stderr = gofs(t, nil, "", "hash", file)
	require.Equal(t, 0, code, stderr)
	assert.Regexp(t, `^[0-9a-f]{64}  `, stdout, "the Dropbox content hash")
	assert.NotContains(t, stdout, "5891b5b5")

	code, _, stderr = gofs(t, nil, "", "hash", "-a", "md4", file)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, `unknown algorithm "md4"`)

	code, _, _ = gofs(t, nil, "", "hash", dir)
	assert.Equal(t, 1, code, "a directory has no content hash")
}

func TestCmp(t *testing.T) {
	a := newTree(t)
	b := newTree(t)

	code, stdout, stderr := gofs(t, nil, "", "cmp", filepath.Join(a, "x.txt"), filepath.Join(b, "x.txt"))
	assert.Equal(t, 0, code, stderr)
	assert.Empty(t, stdout)
	code, _, _ = gofs(t, nil, "", "cmp", "-r", a, b)
	assert.Equal(t, 0, code)

	writeFiles(t, b, map[string]string{"sub/y.txt": "changed"})
	code, _, _ = gofs(t, nil, "", "cmp", a, b)
	assert.Equal(t, 0, code, "without -r the sub-directories are not compared")
	code, stdout, _ = gofs(t, nil, "", "cmp", "-r", a, b)
	assert.Equal(t, 1, code)
	assert.Equal(t, a+" and "+b+" differ\n", stdout)
	code, _, _ = gofs(t, nil, "", "cmp", filepath.Join(a, "x.txt"), filepath.Join(b, "sub", "y.txt"))
	assert.Equal(t, 1, code)
}

func TestEnv(t *testing.T) {
	code, stdout, _ := gofs(t, map[string]string{"GOFS_SFTP_PASSWORD": "S3cret"}, "", "env")
	require.Equal(t, 0, code)
	assert.NotContains(t, stdout, "S3cret", "values are never printed")
	assert.Regexp(t, `\n  GOFS_SFTP_PASSWORD +set +password`, stdout)
	assert.Regexp(t, `\n  GOFS_SFTP_USER +username`, stdout, "not set")
	for _, v := range envVars {
		assert.Contains(t, stdout, "\n  "+v.name+" ")
	}
}

// newWebDAVServer returns a WebDAV server that requires
// the basic authentication of alice with the password secret.
func newWebDAVServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, _ := r.BasicAuth(); user != "alice" || password != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWebDAV runs commands between the local file system and WebDAV,
// a file system that the environment configures.
func TestWebDAV(t *testing.T) {
	srv := newWebDAVServer(t)
	env := map[string]string{
		"GOFS_WEBDAV_URL":      srv.URL,
		"GOFS_WEBDAV_USER":     "alice",
		"GOFS_WEBDAV_PASSWORD": "secret",
	}
	prefix := "webdav://alice@" + srv.Listener.Addr().String()
	local := newTree(t)

	code, _, stderr := gofs(t, env, "hello", "put", "webdav:///hello.txt")
	require.Equal(t, 0, code, stderr)
	code, stdout, _ := gofs(t, env, "", "cat", "webdav:///hello.txt")
	require.Equal(t, 0, code)
	assert.Equal(t, "hello", stdout)
	code, stdout, _ = gofs(t, env, "", "cat", prefix+"/hello.txt")
	require.Equal(t, 0, code, "the URI with the prefix of the file system")
	assert.Equal(t, "hello", stdout)

	code, _, stderr = gofs(t, env, "", "cp", "-r", local, "webdav:///copy")
	require.Equal(t, 0, code, stderr)
	code, stdout, _ = gofs(t, env, "", "ls", "-r", "-a", "webdav:///copy")
	require.Equal(t, 0, code)
	assert.Equal(t, ".hidden\nsub/y.txt\nx.txt\n", stdout)
	code, stdout, _ = gofs(t, env, "", "ls", "-u", "webdav:///copy")
	require.Equal(t, 0, code)
	assert.Equal(t, prefix+"/copy/sub\n"+prefix+"/copy/x.txt\n", stdout)
	code, _, stderr = gofs(t, env, "", "cmp", "-r", local, "webdav:///copy")
	assert.Equal(t, 0, code, stderr)

	code, _, stderr = gofs(t, env, "", "mv", "webdav:///hello.txt", local)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "hello", readFile(t, filepath.Join(local, "hello.txt")))
	code, _, _ = gofs(t, env, "", "stat", "webdav:///hello.txt")
	assert.Equal(t, 1, code, "moved away")

	code, _, stderr = gofs(t, env, "", "rm", "-r", "webdav:///copy")
	require.Equal(t, 0, code, stderr)
	code, stdout, _ = gofs(t, env, "", "ls", "webdav:///")
	require.Equal(t, 0, code)
	assert.Empty(t, stdout)

	code, _, stderr = gofs(t, env, "", "ls", "webdav://bob@other.example.com/dav")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "is not on "+prefix)
}

// TestRm_Root checks that rm -r never removes the root directory of a
// file system, which the file systems refuse, on WebDAV and not on the
// local root, which the test would remove if the refusal failed.
func TestRm_Root(t *testing.T) {
	env := map[string]string{
		"GOFS_WEBDAV_URL":      newWebDAVServer(t).URL,
		"GOFS_WEBDAV_USER":     "alice",
		"GOFS_WEBDAV_PASSWORD": "secret",
	}
	code, _, stderr := gofs(t, env, "x", "put", "webdav:///dir/x.txt")
	require.Equal(t, 0, code, stderr)

	for _, uri := range []string{"webdav:///", "webdav://"} {
		code, _, stderr = gofs(t, env, "", "rm", "-rf", uri)
		assert.Equal(t, 1, code, uri)
		assert.Contains(t, stderr, "root directory", uri)
	}
	code, stdout, _ := gofs(t, env, "", "ls", "-r", "webdav:///")
	require.Equal(t, 0, code)
	assert.Equal(t, "dir/x.txt\n", stdout, "nothing removed")
}

func TestWebDAV_Credentials(t *testing.T) {
	srv := newWebDAVServer(t)
	userURL := strings.Replace(srv.URL, "://", "://alice@", 1)

	code, _, stderr := gofs(t, map[string]string{"GOFS_WEBDAV_URL": userURL, "GOFS_WEBDAV_PASSWORD": "secret"}, "", "ls", "webdav:///")
	assert.Equal(t, 0, code, "the username of the URL with the password of the environment: %s", stderr)

	code, _, stderr = gofs(t, map[string]string{"GOFS_WEBDAV_URL": userURL, "GOFS_WEBDAV_PASSWORD": "wrong"}, "", "ls", "webdav:///")
	assert.Equal(t, 1, code)
	assert.NotContains(t, stderr, "wrong")

	code, _, stderr = gofs(t, nil, "", "ls", "webdav:///")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "GOFS_WEBDAV_URL")
}

func TestWebDAV_Token(t *testing.T) {
	handler := &webdav.Handler{FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	code, _, stderr := gofs(t, map[string]string{"GOFS_WEBDAV_URL": srv.URL, "GOFS_WEBDAV_TOKEN": "t0ken"}, "x", "put", "webdav:///x")
	assert.Equal(t, 0, code, stderr)
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file.txt" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("from http"))
	}))
	t.Cleanup(srv.Close)

	code, stdout, stderr := gofs(t, nil, "", "cat", srv.URL+"/file.txt")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "from http", stdout)

	dest := filepath.Join(t.TempDir(), "file.txt")
	code, _, stderr = gofs(t, nil, "", "cp", srv.URL+"/file.txt", dest)
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "from http", readFile(t, dest))

	code, _, _ = gofs(t, nil, "", "put", srv.URL+"/file.txt")
	assert.Equal(t, 1, code, "HTTP is read only")
}
