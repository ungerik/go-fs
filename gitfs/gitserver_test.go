package gitfs

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
)

// The tests in this file run gitfs against real git servers:
// git http-backend of the local git installation as CGI handler of an
// httptest.Server (skipped without git), and nginx with git http-backend
// in a Docker container (skipped without Docker).

const (
	dockerImage    = "gitfs-test-server"
	dockerUsername = "testuser"
	dockerPassword = "testpass"
)

var (
	dockerContainer string
	dockerRepoURL   string // empty if the Docker server is not available
)

// fixtureScript creates the git repository of a work tree written by
// writeFixture, with a submodule entry, an annotated tag v1 and the
// fixed commit time testCommitTime, and clones it to a bare repository
// that allows filters. Arguments: work tree, bare repository.
const fixtureScript = `set -e
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0='*'
export GIT_AUTHOR_NAME=gitfs GIT_AUTHOR_EMAIL=gitfs@example.com
export GIT_COMMITTER_NAME=gitfs GIT_COMMITTER_EMAIL=gitfs@example.com
export GIT_AUTHOR_DATE=2026-01-02T03:04:05Z GIT_COMMITTER_DATE=2026-01-02T03:04:05Z
cd "$1"
git init -q -b main .
git add -A
git update-index --add --cacheinfo 160000,0123456789abcdef0123456789abcdef01234567,extra/submodule
git commit -q -m fixture
git tag -a v1 -m v1
git clone -q --bare . "$2"
git -C "$2" config uploadpack.allowFilter true
`

// writeFixture writes the work tree of the test repository: the
// conformance seed below seed and the special entries below extra.
func writeFixture(dir string) error {
	files := map[string]string{
		"README.md":            "# Test repository\n",
		"extra/deep/a/b/c.txt": "deep",
	}
	for name, content := range fstest.DefaultSeed() {
		files["seed/"+name] = string(content)
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			return err
		}
		err = os.WriteFile(path, []byte(content), 0o644)
		if err != nil {
			return err
		}
	}
	err := os.WriteFile(filepath.Join(dir, "extra", "run.sh"), []byte("#!/bin/sh\necho run\n"), 0o755) //#nosec G306 -- an executable test file
	if err != nil {
		return err
	}
	return os.Symlink("../seed/hello.txt", filepath.Join(dir, "extra", "link.txt"))
}

// checkFixture checks the special entries of the fixture
// and runs the conformance suite on its seed directory.
func checkFixture(t *testing.T, gfs *FileSystem) {
	t.Helper()
	ctx := t.Context()

	data, err := gfs.ReadAll(ctx, "/extra/deep/a/b/c.txt")
	require.NoError(t, err)
	assert.Equal(t, "deep", string(data))

	run, err := gfs.Stat("/extra/run.sh")
	require.NoError(t, err)
	assert.Equal(t, ExecutablePermissions, run.Permissions)
	assert.Equal(t, testCommitTime, run.Modified.UTC())

	for _, notExposed := range []string{"/extra/link.txt", "/extra/submodule"} {
		exists, err := gfs.Exists(notExposed)
		require.NoError(t, err)
		assert.False(t, exists, notExposed)
	}

	var files []string
	err = gfs.ListDirRecursive(ctx, "/extra", nil, func(info *fs.FileInfo) error {
		files = append(files, strings.TrimPrefix(string(info.File), gfs.Prefix()))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/extra/deep/a/b/c.txt", "/extra/run.sh"}, files)

	fstest.RunConformance(t, gfs, fstest.Config{TestDir: "/seed", SkipClose: true})
}

func TestLocalGitHTTPBackend(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fixture script needs a POSIX shell and symbolic links")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	require.NoError(t, writeFixture(work))
	out, err := exec.CommandContext(t.Context(), "sh", "-c", fixtureScript, "sh", work, filepath.Join(root, "repos", "seed.git")).CombinedOutput()
	require.NoError(t, err, string(out))

	server := httptest.NewServer(&cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Join(root, "repos"),
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		},
		InheritEnv: []string{"PATH"},
	})
	t.Cleanup(server.Close)
	repoURL := server.URL + "/seed.git"

	for _, ref := range []string{"", "main", "v1"} {
		t.Run("Ref="+ref, func(t *testing.T) {
			checkFixture(t, newTestFS(t, repoURL, ref, nil))
		})
	}
}

func TestDockerGitServer(t *testing.T) {
	if dockerRepoURL == "" {
		t.Skip("the Docker git server is not available")
	}
	ctx := t.Context()

	_, err := New(ctx, dockerRepoURL, "", nil)
	assert.ErrorIs(t, err, os.ErrPermission)
	_, err = New(ctx, dockerRepoURL, "", &Options{Username: dockerUsername, Password: "wrong"})
	assert.ErrorIs(t, err, os.ErrPermission)

	opts := &Options{Username: dockerUsername, Password: dockerPassword}
	for _, ref := range []string{"main", "v1"} {
		t.Run("Ref="+ref, func(t *testing.T) {
			checkFixture(t, newTestFS(t, dockerRepoURL, ref, opts))
		})
	}
	withUser := strings.Replace(dockerRepoURL, "http://", "http://"+dockerUsername+":"+dockerPassword+"@", 1)
	gfs := newTestFS(t, withUser, "", nil)
	data, err := gfs.ReadAll(ctx, "/README.md")
	require.NoError(t, err)
	assert.Equal(t, "# Test repository\n", string(data))
}

// TestMain starts the Docker git server if Docker is available.
func TestMain(m *testing.M) {
	if !dockertest.Available() {
		log.Println("Docker not available, skipping the Docker git server tests")
		os.Exit(m.Run())
	}
	exitCode := 1
	err := startDockerGitServer()
	if err != nil {
		log.Printf("Docker git server: %v", err)
		removeDockerContainer()
		dockertest.SetupFailed("git")
		exitCode = m.Run()
	} else {
		exitCode = m.Run()
		removeDockerContainer()
	}
	os.Exit(exitCode)
}

func startDockerGitServer() error {
	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()
	run := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker %s: %w\n%s", args[0], err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}

	log.Println("Building the Docker git server image...")
	_, err := run("build", "-q", "-f", "Dockerfile.git-test", "-t", dockerImage, ".")
	if err != nil {
		return err
	}
	dockerContainer = dockertest.ContainerName("gitfs-test")
	_, err = run("run", "-d", "--name", dockerContainer, "-p", "127.0.0.1::80", dockerImage)
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "gitfs-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	err = writeFixture(work)
	if err != nil {
		return err
	}
	_, err = run("cp", work+"/.", dockerContainer+":/srv/work")
	if err != nil {
		return err
	}
	_, err = run("exec", dockerContainer, "sh", "-c", fixtureScript, "sh", "/srv/work", "/srv/git/seed.git")
	if err != nil {
		return err
	}

	hostAddr, err := run("port", dockerContainer, "80/tcp")
	if err != nil {
		return err
	}
	hostAddr, _, _ = strings.Cut(hostAddr, "\n")
	if _, _, err := net.SplitHostPort(hostAddr); err != nil {
		return fmt.Errorf("docker port: %w", err)
	}
	repoURL := "http://" + hostAddr + "/seed.git"
	var lastErr error
	for range 30 {
		gfs, err := New(ctx, repoURL, "", &Options{Username: dockerUsername, Password: dockerPassword})
		if err == nil {
			_ = gfs.Close()
			dockerRepoURL = repoURL
			log.Println("Docker git server is ready")
			return nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("server not ready: %w", lastErr)
}

func removeDockerContainer() {
	if dockerContainer != "" {
		_ = exec.Command("docker", "rm", "--force", "--volumes", dockerContainer).Run()
	}
}
