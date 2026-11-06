package gitfs

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// The online tests read github.com/ungerik/go-fs itself at release tags,
// whose content never changes. They need public internet access and run
// only with GOFS_ONLINE_TESTS=1. GITHUB_TOKEN is used if set, it raises
// the rate limit of the GitHub REST API.

const goFSRepo = "https://github.com/ungerik/go-fs.git"

func skipUnlessOnline(t *testing.T) {
	t.Helper()
	if os.Getenv("GOFS_ONLINE_TESTS") != "1" {
		t.Skip("set GOFS_ONLINE_TESTS=1 to run tests against github.com")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func onlineOptions(disableGitHub bool) *Options {
	return &Options{Token: os.Getenv("GITHUB_TOKEN"), DisableGitHub: disableGitHub}
}

// knownVersions are release tags of go-fs with values checked with
// git rev-parse and git ls-tree -r on a clone of the repository.
var knownVersions = []struct {
	tag    string
	commit string
	files  int // regular files, symbolic links and submodules excluded
}{
	{tag: "v1.0.0-beta.1", commit: "ac648b577177f7a24b3dae3202d18809b7a70109", files: 187},
	{tag: "v1.0.0-rc.1", commit: "fb5aa79f5782af8e99ce5c69b04208e9da14f222", files: 248},
	{tag: "v1.0.0-rc.6", commit: "64e9625c74cec658bb1fc0cb64e34a58cdddb9cf", files: 255},
}

func TestOnlineKnownVersions(t *testing.T) {
	skipUnlessOnline(t)

	for _, shortcuts := range []string{"github", "git"} {
		t.Run(shortcuts, func(t *testing.T) {
			for _, v := range knownVersions {
				t.Run(v.tag, func(t *testing.T) {
					ctx := t.Context()
					gfs, err := New(ctx, goFSRepo, v.tag, onlineOptions(shortcuts == "git"))
					require.NoError(t, err)
					t.Cleanup(func() { _ = gfs.Close() })
					require.NoError(t, fs.Register(gfs))

					assert.Equal(t, v.commit, gfs.Commit())
					assert.Equal(t, "git+"+goFSRepo+"@"+v.tag, gfs.Prefix())

					// The typical use: read a single file
					version, err := gfs.RootDir().Join("VERSION").ReadAllString(ctx)
					require.NoError(t, err)
					assert.Equal(t, v.tag+"\n", version)

					info, err := gfs.Stat("/VERSION")
					require.NoError(t, err)
					assert.Equal(t, int64(len(v.tag)+1), info.Size)
					assert.Equal(t, FilePermissions, info.Permissions)
					assert.False(t, info.Modified.IsZero())

					script, err := gfs.Stat("/test-workspace.sh")
					require.NoError(t, err)
					assert.Equal(t, ExecutablePermissions, script.Permissions)

					var files int
					err = gfs.ListDirRecursive(ctx, "/", nil, func(info *fs.FileInfo) error {
						files++
						assert.GreaterOrEqual(t, info.Size, int64(0), info.File)
						return nil
					})
					require.NoError(t, err)
					assert.Equal(t, v.files, files)

					_, err = gfs.Stat("/does-not-exist")
					assert.ErrorIs(t, err, os.ErrNotExist)
					_, err = gfs.ReadAll(ctx, "/fsimpl")
					assert.ErrorIs(t, err, fs.ErrIsDirectory{})
				})
			}
		})
	}
}

func TestOnlineRefs(t *testing.T) {
	skipUnlessOnline(t)
	ctx := t.Context()
	rc6 := knownVersions[2]

	t.Run("CommitID", func(t *testing.T) {
		gfs, err := New(ctx, goFSRepo, rc6.commit, onlineOptions(false))
		require.NoError(t, err)
		defer gfs.Close()
		assert.Equal(t, rc6.commit, gfs.Commit())
		data, err := gfs.ReadAll(ctx, "/VERSION")
		require.NoError(t, err)
		assert.Equal(t, rc6.tag+"\n", string(data))
	})

	t.Run("FullTagRef", func(t *testing.T) {
		gfs, err := New(ctx, goFSRepo, "refs/tags/"+rc6.tag, onlineOptions(false))
		require.NoError(t, err)
		defer gfs.Close()
		assert.Equal(t, rc6.commit, gfs.Commit())
	})

	t.Run("LatestVersion", func(t *testing.T) {
		// Reading a single file of the latest version is the typical use,
		// on github.com it costs five requests
		var requests []string
		opts := onlineOptions(false)
		opts.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, r.Method+" "+r.URL.Host+r.URL.Path)
			return http.DefaultTransport.RoundTrip(r)
		})}
		start := time.Now()
		gfs, err := New(ctx, goFSRepo, "", opts)
		require.NoError(t, err)
		defer gfs.Close()
		assert.Len(t, gfs.Commit(), 40)
		readme, err := gfs.ReadAll(ctx, "/README.md")
		require.NoError(t, err)
		t.Logf("New and ReadAll took %s", time.Since(start))
		assert.Contains(t, string(readme), "go-fs")
		// The capabilities, ls-refs, the commit, the
		// tree of the root directory and the content
		require.Len(t, requests, 5)
		assert.Equal(t, []string{
			"GET github.com/ungerik/go-fs.git/info/refs",
			"POST github.com/ungerik/go-fs.git/git-upload-pack",
			"POST github.com/ungerik/go-fs.git/git-upload-pack",
		}, requests[:3])
		assert.True(t, strings.HasPrefix(requests[3], "GET api.github.com/repos/ungerik/go-fs/git/trees/"), requests[3])
		assert.Equal(t, "GET raw.githubusercontent.com/ungerik/go-fs/"+gfs.Commit()+"/README.md", requests[4])
	})

	t.Run("MissingRef", func(t *testing.T) {
		_, err := New(ctx, goFSRepo, "no-such-branch-or-tag", onlineOptions(false))
		assert.ErrorIs(t, err, ErrRefNotFound)
	})

	t.Run("MissingRepository", func(t *testing.T) {
		_, err := New(ctx, "https://github.com/ungerik/no-such-repository-for-gitfs.git", "", onlineOptions(false))
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission), err)
	})
}
