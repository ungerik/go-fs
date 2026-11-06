package gitfs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/gitfs/internal/gittest"
	"github.com/ungerik/go-fs/gitfs/internal/object"
)

var testCommitTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// testFiles are the files of the commit of the fake server: the
// conformance seed below /seed and the special entries below /extra.
func testFiles() map[string]gittest.File {
	files := map[string]gittest.File{
		"extra/run.sh":         {Content: "#!/bin/sh\necho run\n", Mode: object.ModeExecutable},
		"extra/link.txt":       {Content: "../seed/hello.txt", Mode: object.ModeSymlink},
		"extra/submodule":      {Content: "0123456789abcdef0123456789abcdef01234567", Mode: object.ModeSubmodule},
		"extra/deep/a/b/c.txt": {Content: "deep"},
		"README.md":            {Content: "# Test repository\n"},
	}
	for name, content := range fstest.DefaultSeed() {
		files["seed/"+name] = gittest.File{Content: string(content)}
	}
	return files
}

// newTestServer returns a fake server whose repository has testFiles as
// commit of the branch main and a branch "other", an annotated tag "v1"
// of the commit, a lightweight tag "light" and an annotated tag "nested"
// of the tag v1.
func newTestServer(t *testing.T) (*gittest.Server, *httptest.Server) {
	t.Helper()
	repo := gittest.NewRepo()
	commit := repo.Commit(repo.Tree(testFiles()), testCommitTime)
	repo.Refs["refs/heads/main"] = commit
	other := repo.Commit(repo.Tree(map[string]gittest.File{"other.txt": {Content: "other"}}), testCommitTime, commit)
	repo.Refs["refs/heads/other"] = other
	repo.Refs["refs/tags/light"] = commit
	v1 := repo.Tag("v1", commit)
	repo.Tag("nested", v1)
	// A tag with the name of a branch, the branch wins
	repo.Tag("other", commit)

	server := &gittest.Server{Repo: repo}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func repoURL(httpServer *httptest.Server) string {
	return httpServer.URL + "/owner/repo.git"
}

// withGitHub returns options that use the fake GitHub endpoints.
func withGitHub(httpServer *httptest.Server, opts *Options) *Options {
	if opts == nil {
		opts = new(Options)
	}
	opts.github = &github{apiURL: httpServer.URL + "/api", rawURL: httpServer.URL + "/raw", owner: "owner", repo: "repo"}
	return opts
}

func newTestFS(t *testing.T, url, ref string, opts *Options) *FileSystem {
	t.Helper()
	gfs, err := New(t.Context(), url, ref, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gfs.Close() })
	return gfs
}

func TestConformance(t *testing.T) {
	for _, mode := range []string{"git", "github", "nofilter"} {
		t.Run(mode, func(t *testing.T) {
			server, httpServer := newTestServer(t)
			var opts *Options
			switch mode {
			case "github":
				opts = withGitHub(httpServer, nil)
			case "nofilter":
				server.NoFilter = true
			}
			gfs := newTestFS(t, repoURL(httpServer), "main", opts)
			fstest.RunConformance(t, gfs, fstest.Config{
				Name:    "Git filesystem",
				Prefix:  "git+" + repoURL(httpServer) + "@main",
				TestDir: "/seed",
			})
		})
	}
}

func TestRequestsForASingleFile(t *testing.T) {
	ctx := t.Context()

	t.Run("GitHub", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		gfs := newTestFS(t, repoURL(httpServer), "", withGitHub(httpServer, nil))
		data, err := gfs.ReadAll(ctx, "/README.md")
		require.NoError(t, err)
		assert.Equal(t, "# Test repository\n", string(data))
		// The commit by git, the tree of / by the API and the content by raw
		assert.Equal(t, map[string]int{"info/refs": 1, "ls-refs": 1, "fetch": 1, "api": 1, "raw": 1}, server.Requests())

		server.ResetRequests()
		data, err = gfs.ReadAll(ctx, "/extra/deep/a/b/c.txt")
		require.NoError(t, err)
		assert.Equal(t, "deep", string(data))
		// The trees of extra, deep, a and b
		assert.Equal(t, map[string]int{"api": 4, "raw": 1}, server.Requests())

		// Read again: the path is loaded and the content cached
		server.ResetRequests()
		_, err = gfs.ReadAll(ctx, "/extra/deep/a/b/c.txt")
		require.NoError(t, err)
		assert.Empty(t, server.Requests())
	})

	t.Run("Git", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		gfs := newTestFS(t, repoURL(httpServer), "", nil)
		data, err := gfs.ReadAll(ctx, "/extra/deep/a/b/c.txt")
		require.NoError(t, err)
		assert.Equal(t, "deep", string(data))
		// The commit, the trees of /, extra, deep, a, b and the blob
		assert.Equal(t, map[string]int{"info/refs": 1, "ls-refs": 1, "fetch": 7}, server.Requests())

		// Everything is loaded and cached now
		server.ResetRequests()
		info, err := gfs.Stat("/extra/deep/a/b/c.txt")
		require.NoError(t, err)
		assert.Equal(t, int64(4), info.Size)
		_, err = gfs.ReadAll(ctx, "/extra/deep/a/b/c.txt")
		require.NoError(t, err)
		assert.Empty(t, server.Requests())
	})

	t.Run("CommitID", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		commit := server.Repo.Refs["refs/heads/main"]
		gfs := newTestFS(t, repoURL(httpServer), string(commit), withGitHub(httpServer, nil))
		assert.Equal(t, string(commit), gfs.Commit())
		// No ls-refs for a commit ID, but a fetch that checks the commit
		assert.Equal(t, map[string]int{"info/refs": 1, "fetch": 1}, server.Requests())
	})
}

func TestGitHubShortcuts(t *testing.T) {
	ctx := t.Context()

	t.Run("SizesFromAPI", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		var names []string
		err := gfs.ListDir(ctx, "/seed", nil, func(info *fs.FileInfo) error {
			names = append(names, info.Name)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"sub", ".hidden.txt", "100%25 a%20b.txt", "empty.txt", "hello.txt"}, names)
		// The commit by git, the trees of / and /seed by the API, no blobs
		assert.Equal(t, map[string]int{"info/refs": 1, "ls-refs": 1, "fetch": 1, "api": 2}, server.Requests())

		// A recursive listing loads the sub-directories with one request
		server.ResetRequests()
		var files int
		err = gfs.ListDirRecursive(ctx, "/extra", nil, func(info *fs.FileInfo) error {
			files++
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 2, files) // run.sh and deep/a/b/c.txt
		assert.Equal(t, map[string]int{"api": 1}, server.Requests())

		// The content of a file comes from raw
		server.ResetRequests()
		data, err := gfs.ReadAll(ctx, "/seed/hello.txt")
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", string(data))
		assert.Equal(t, map[string]int{"raw": 1}, server.Requests())
	})

	t.Run("RawDown", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		server.RawDown = true
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		for _, file := range []string{"/seed/hello.txt", "/README.md"} {
			_, err := gfs.ReadAll(ctx, file)
			require.NoError(t, err)
		}
		// Raw is asked once, then git is used
		assert.Equal(t, 1, server.Requests()["raw"])
		assert.Equal(t, 3, server.Requests()["fetch"]) // the commit and the two blobs
	})

	t.Run("RawContentMismatch", func(t *testing.T) {
		// Content that doesn't match the blob, like a Git LFS file, comes from git
		server, httpServer := newTestServer(t)
		server.RawContent = map[string]string{"seed/hello.txt": "version https://git-lfs.github.com/spec/v1\n"}
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		data, err := gfs.ReadAll(ctx, "/seed/hello.txt")
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", string(data))
		// Raw is still used for the other files
		_, err = gfs.ReadAll(ctx, "/seed/empty.txt")
		require.NoError(t, err)
		assert.Equal(t, 2, server.Requests()["raw"])
		assert.Equal(t, 2, server.Requests()["fetch"]) // the commit and hello.txt
	})

	t.Run("APIDown", func(t *testing.T) {
		server, httpServer := newTestServer(t)
		server.GitHubDown = true
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		info, err := gfs.Stat("/seed/hello.txt")
		require.NoError(t, err)
		assert.Equal(t, int64(len("Hello, World!")), info.Size)
		// The API is asked once, then git is used
		_, err = gfs.Stat("/extra/run.sh")
		require.NoError(t, err)
		assert.Equal(t, 1, server.Requests()["api"])
	})

	t.Run("ReadAllErrors", func(t *testing.T) {
		_, httpServer := newTestServer(t)
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		_, err := gfs.ReadAll(ctx, "/seed/sub")
		assert.ErrorIs(t, err, fs.ErrIsDirectory{})
		_, err = gfs.ReadAll(ctx, "/seed/missing.txt")
		assert.ErrorIs(t, err, os.ErrNotExist)
		_, err = gfs.ReadAll(ctx, "/extra/submodule")
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("SymlinksNotExposed", func(t *testing.T) {
		// raw.githubusercontent.com serves the target of a link,
		// ReadAll doesn't read it, whether the directory was listed or not
		server, httpServer := newTestServer(t)
		gfs := newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, nil))
		_, err := gfs.ReadAll(ctx, "/extra/link.txt")
		assert.ErrorIs(t, err, os.ErrNotExist)
		_, err = gfs.Stat("/extra/link.txt")
		assert.ErrorIs(t, err, os.ErrNotExist)
		_, err = gfs.ReadAll(ctx, "/extra/link.txt")
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.Zero(t, server.Requests()["raw"])
	})

	t.Run("DetectedForGitHubOnly", func(t *testing.T) {
		for repoURL, want := range map[string]bool{
			"https://github.com/ungerik/go-fs.git":      true,
			"https://GitHub.com/ungerik/go-fs":          true,
			"https://github.com/ungerik/go-fs/extra":    false,
			"https://github.com:8443/ungerik/go-fs.git": false,
			"http://github.com/ungerik/go-fs.git":       false,
			"https://gitlab.com/ungerik/go-fs.git":      false,
		} {
			u, err := url.Parse(repoURL)
			require.NoError(t, err)
			gh := githubRepo(u)
			assert.Equal(t, want, gh != nil, repoURL)
			if gh != nil {
				assert.Equal(t, "ungerik", gh.owner)
				assert.Equal(t, "go-fs", gh.repo)
			}
		}
	})
}

func TestRefs(t *testing.T) {
	server, httpServer := newTestServer(t)
	repo := server.Repo
	main := string(repo.Refs["refs/heads/main"])
	for ref, want := range map[string]string{
		"":                                main,
		"HEAD":                            main,
		"main":                            main,
		"refs/heads/main":                 main,
		"v1":                              main,
		"refs/tags/v1":                    main,
		"light":                           main,
		"nested":                          main,
		"other":                           string(repo.Refs["refs/heads/other"]), // the branch wins over the tag
		string(repo.Refs["refs/tags/v1"]): main,                                  // the ID of an annotated tag
		strings.ToUpper(main):             main,
	} {
		t.Run(ref, func(t *testing.T) {
			gfs := newTestFS(t, repoURL(httpServer), ref, nil)
			assert.Equal(t, want, gfs.Commit())
			assert.Equal(t, ref, gfs.Ref())
			exists, err := gfs.Exists("/seed/hello.txt")
			require.NoError(t, err)
			assert.Equal(t, want == main, exists)
		})
	}

	ctx := t.Context()
	_, err := New(ctx, repoURL(httpServer), "missing", nil)
	assert.ErrorIs(t, err, ErrRefNotFound)
	_, err = New(ctx, repoURL(httpServer), "0000000000000000000000000000000000000000", nil)
	assert.Error(t, err)
	tree, _ := repo.Format.ParseCommit(repo.Objects[repo.Refs["refs/heads/main"]].Data)
	_, err = New(ctx, repoURL(httpServer), string(tree.Tree), nil)
	assert.ErrorIs(t, err, os.ErrInvalid, "a tree is not a commit")

	repo.Head = ""
	_, err = New(ctx, repoURL(httpServer), "", nil)
	assert.ErrorIs(t, err, ErrRefNotFound)
}

func TestNewErrors(t *testing.T) {
	ctx := t.Context()
	for _, invalid := range []string{"ftp://host/repo.git", "https://host/repo.git?x=1", "https://host/repo.git#x", "::"} {
		_, err := New(ctx, invalid, "", nil)
		assert.Error(t, err, invalid)
	}
	for _, invalid := range []string{"a\x00b", "a b", "a\nb", "a\x7fb", strings.Repeat("a", 70000)} {
		// Refused before any request, a long ref would not fit into a pkt-line
		_, err := New(ctx, "https://host/repo.git", invalid, nil)
		assert.ErrorIs(t, err, os.ErrInvalid, "%.20q", invalid)
	}

	server, httpServer := newTestServer(t)
	_, err := New(ctx, httpServer.URL+"/owner/missing.git", "", nil)
	assert.ErrorIs(t, err, os.ErrNotExist)

	server.ProtocolV0 = true
	_, err = New(ctx, repoURL(httpServer), "", nil)
	assert.ErrorIs(t, err, ErrProtocolV2Required)
	server.ProtocolV0 = false

	server.Dumb = true
	_, err = New(ctx, repoURL(httpServer), "", nil)
	assert.ErrorIs(t, err, ErrProtocolV2Required)
	server.Dumb = false

	server.FetchError = "fatal: out of memory"
	gfs := newTestFS(t, repoURL(httpServer), "", nil)
	_, err = gfs.Stat("/README.md")
	assert.ErrorContains(t, err, "fatal: out of memory")
}

func TestAuthentication(t *testing.T) {
	server, httpServer := newTestServer(t)
	server.Username, server.Password = "user", "secret"
	ctx := t.Context()

	_, err := New(ctx, repoURL(httpServer), "", nil)
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.NotContains(t, err.Error(), "secret")

	_, err = New(ctx, repoURL(httpServer), "", &Options{Username: "user", Password: "wrong"})
	assert.ErrorIs(t, err, os.ErrPermission)

	gfs := newTestFS(t, repoURL(httpServer), "", &Options{Username: "user", Password: "secret"})
	_, err = gfs.Stat("/README.md")
	require.NoError(t, err)

	// Credentials in the URL are used and not part of the prefix
	withUser := strings.Replace(repoURL(httpServer), "http://", "http://user:secret@", 1)
	gfs = newTestFS(t, withUser, "", nil)
	assert.NotContains(t, gfs.Prefix(), "secret")
	assert.NotContains(t, gfs.String(), "secret")
	assert.Equal(t, repoURL(httpServer), gfs.RepositoryURL())

	// A token is the password of Basic authentication
	server.Username, server.Password = "x-access-token", "token"
	gfs = newTestFS(t, repoURL(httpServer), "", &Options{Token: "token"})
	_, err = gfs.ReadAll(ctx, "/README.md")
	require.NoError(t, err)
}

func TestEntries(t *testing.T) {
	server, httpServer := newTestServer(t)
	repo := server.Repo
	blob := repo.Add(object.TypeBlob, []byte("x"))
	invalid := []string{".git", ".GIT", "..", ".", `a\b`, "a/b", "\xff.txt", ""}
	var entries []object.TreeEntry
	for _, name := range invalid {
		entries = append(entries, object.TreeEntry{Mode: object.ModeFile, Name: name, OID: blob})
	}
	entries = append(entries,
		object.TreeEntry{Mode: object.ModeFile, Name: "valid.txt", OID: blob},
		// Valid UTF-8, the GitHub API sends it like the invalid "\xff.txt"
		object.TreeEntry{Mode: object.ModeFile, Name: "report\uFFFD.txt", OID: blob},
		object.TreeEntry{Mode: 0o100664, Name: "group-writable.txt", OID: blob},
		object.TreeEntry{Mode: object.ModeDir, Name: ".github", OID: repo.Tree(map[string]gittest.File{"ci.yml": {Content: "ci"}})},
	)
	repo.Refs["refs/heads/names"] = repo.Commit(repo.RawTree(entries...), testCommitTime)

	ctx := t.Context()
	for _, github := range []bool{false, true} {
		t.Run(fmt.Sprintf("GitHub=%t", github), func(t *testing.T) {
			var opts *Options
			if github {
				opts = withGitHub(httpServer, nil)
			}
			gfs := newTestFS(t, repoURL(httpServer), "names", opts)
			visible := []string{"valid.txt", "group-writable.txt", "report\uFFFD.txt"}
			var names []string
			err := gfs.ListDir(ctx, "/", nil, func(info *fs.FileInfo) error {
				names = append(names, info.Name)
				assert.Equal(t, testCommitTime, info.Modified.UTC())
				assert.Equal(t, !slices.Contains(visible, info.Name), info.IsHidden || info.IsDir, info.Name)
				return nil
			})
			require.NoError(t, err)
			// The same names with and without the GitHub shortcuts
			assert.Equal(t, []string{".github", "group-writable.txt", "report\uFFFD.txt", "valid.txt"}, names)

			if github {
				// Only the directory with the lossy name was read through
				// git, the API is still used for the other directories
				server.ResetRequests()
				_, err = gfs.Stat("/.github/ci.yml")
				require.NoError(t, err)
				assert.Equal(t, map[string]int{"api": 1}, server.Requests())
			}

			_, err = gfs.ReadAll(ctx, "/.git")
			assert.ErrorIs(t, err, os.ErrNotExist)

			info, err := gfs.Stat("/")
			require.NoError(t, err)
			assert.True(t, info.IsDir)
			assert.Equal(t, DirPermissions, info.Permissions)
			assert.Equal(t, gfs.RootDir(), info.File)
		})
	}

	gfs := newTestFS(t, repoURL(httpServer), "main", nil)
	run, err := gfs.Stat("/extra/run.sh")
	require.NoError(t, err)
	assert.Equal(t, ExecutablePermissions, run.Permissions)
	assert.Equal(t, string(repo.Format.Hash(object.TypeBlob, []byte("#!/bin/sh\necho run\n"))), run.Sys)
	for _, notExposed := range []string{"/extra/link.txt", "/extra/submodule", "/seed/hello.txt/child"} {
		_, err := gfs.Stat(notExposed)
		assert.ErrorIs(t, err, os.ErrNotExist, notExposed)
	}
	_, err = gfs.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)

	// The readers seek, see the "Seeking reads" row of the support matrix
	r, err := gfs.OpenReader("/extra/run.sh")
	require.NoError(t, err)
	defer r.Close()
	assert.Implements(t, (*fs.ReadAtSeekCloser)(nil), r)
}

func TestReadAllReturnsACopy(t *testing.T) {
	ctx := t.Context()
	for _, github := range []bool{false, true} {
		t.Run(fmt.Sprintf("GitHub=%t", github), func(t *testing.T) {
			_, httpServer := newTestServer(t)
			var opts *Options
			if github {
				opts = withGitHub(httpServer, nil)
			}
			gfs := newTestFS(t, repoURL(httpServer), "main", opts)
			data, err := gfs.ReadAll(ctx, "/seed/hello.txt")
			require.NoError(t, err)
			data[0] = 'X'
			// The cached content is not modified
			data, err = gfs.ReadAll(ctx, "/seed/hello.txt")
			require.NoError(t, err)
			assert.Equal(t, "Hello, World!", string(data))
		})
	}
}

func TestLimits(t *testing.T) {
	ctx := t.Context()
	server, httpServer := newTestServer(t)

	gfs := newTestFS(t, repoURL(httpServer), "main", &Options{MaxObjectSize: 10})
	_, err := gfs.ReadAll(ctx, "/seed/hello.txt")
	assert.ErrorContains(t, err, "above the limit")
	gfs = newTestFS(t, repoURL(httpServer), "main", withGitHub(httpServer, &Options{MaxObjectSize: 10}))
	_, err = gfs.ReadAll(ctx, "/seed/hello.txt")
	assert.ErrorContains(t, err, "above the limit", "raw refuses the content")

	// A cache smaller than the files still works, without caching
	gfs = newTestFS(t, repoURL(httpServer), "main", &Options{CacheSize: 5})
	for range 2 {
		data, err := gfs.ReadAll(ctx, "/seed/hello.txt")
		require.NoError(t, err)
		assert.Equal(t, "Hello, World!", string(data))
	}

	t.Run("SizesAboveMaxFetchSize", func(t *testing.T) {
		// Four files of 400 bytes don't fit into one response of 1000
		// bytes, a file of 2000 bytes is above MaxObjectSize
		files := map[string]gittest.File{"big.bin": {Content: strings.Repeat("b", 2000)}}
		for i := range 4 {
			files[fmt.Sprintf("file%d.txt", i)] = gittest.File{Content: strings.Repeat(strconv.Itoa(i), 400)}
		}
		repo := server.Repo
		repo.Refs["refs/heads/sizes"] = repo.Commit(repo.Tree(files), testCommitTime)
		gfs := newTestFS(t, repoURL(httpServer), "sizes", &Options{MaxObjectSize: 1000, MaxFetchSize: 1000})

		sizes := make(map[string]int64)
		err := gfs.ListDir(ctx, "/", nil, func(info *fs.FileInfo) error {
			sizes[info.Name] = info.Size
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]int64{"big.bin": 2000, "file0.txt": 400, "file1.txt": 400, "file2.txt": 400, "file3.txt": 400}, sizes)

		info, err := gfs.Stat("/big.bin")
		require.NoError(t, err)
		assert.Equal(t, int64(2000), info.Size)
		_, err = gfs.ReadAll(ctx, "/big.bin")
		assert.ErrorContains(t, err, "above the limit")
		data, err := gfs.ReadAll(ctx, "/file3.txt")
		require.NoError(t, err)
		assert.Equal(t, strings.Repeat("3", 400), string(data))
	})
}

func TestConcurrentReads(t *testing.T) {
	server, httpServer := newTestServer(t)
	gfs := newTestFS(t, repoURL(httpServer), "main", nil)
	_, err := gfs.Stat("/seed")
	require.NoError(t, err)
	server.ResetRequests()

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			r, err := gfs.OpenReader("/seed/sub/nested.txt")
			if !assert.NoError(t, err) {
				return
			}
			defer r.Close()
			buf := make([]byte, 100)
			n, _ := r.Read(buf)
			assert.Equal(t, "nested content", string(buf[:n]))
		})
	}
	wg.Wait()
	// The tree of sub once, the blob at most once per waiting reader
	assert.LessOrEqual(t, server.Requests()["fetch"], 21)
}

func TestSharedFetchContext(t *testing.T) {
	server, httpServer := newTestServer(t)
	gfs := newTestFS(t, repoURL(httpServer), "main", nil)
	// Load the trees, not the blobs
	for _, file := range []string{"/seed/hello.txt", "/README.md"} {
		exists, err := gfs.Exists(file)
		require.NoError(t, err)
		require.True(t, exists)
	}

	// blockFetches makes the fetches wait until release is closed
	// or their request is canceled, which they send to canceled
	blockFetches := func() (started, canceled, release chan struct{}) {
		started = make(chan struct{}, 10)
		canceled = make(chan struct{}, 10)
		release = make(chan struct{})
		server.BeforeFetch = func(ctx context.Context) {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				canceled <- struct{}{}
			}
		}
		return started, canceled, release
	}
	waiters := func(file string) int {
		n, err := gfs.lookupFile(t.Context(), file)
		require.NoError(t, err)
		gfs.mtx.Lock()
		defer gfs.mtx.Unlock()
		if fetch := gfs.inflight[n.oid]; fetch != nil {
			return fetch.waiters
		}
		return 0
	}

	t.Run("CanceledReaderDoesNotFailOthers", func(t *testing.T) {
		started, canceled, release := blockFetches()
		ctxA, cancelA := context.WithCancel(t.Context())
		errA := make(chan error, 1)
		go func() {
			_, err := gfs.ReadAll(ctxA, "/seed/hello.txt")
			errA <- err
		}()
		<-started
		type result struct {
			data []byte
			err  error
		}
		resultB := make(chan result, 1)
		go func() {
			data, err := gfs.ReadAll(t.Context(), "/seed/hello.txt")
			resultB <- result{data, err}
		}()
		require.Eventually(t, func() bool { return waiters("/seed/hello.txt") == 2 }, 5*time.Second, time.Millisecond)

		cancelA()
		assert.ErrorIs(t, <-errA, context.Canceled)
		close(release)
		b := <-resultB
		require.NoError(t, b.err, "the fetch is not canceled with the context of the first reader")
		assert.Equal(t, "Hello, World!", string(b.data))
		assert.Empty(t, canceled)
	})

	t.Run("LastReaderCancelsTheFetch", func(t *testing.T) {
		started, canceled, release := blockFetches()
		defer close(release)
		ctx, cancel := context.WithCancel(t.Context())
		errC := make(chan error, 1)
		go func() {
			_, err := gfs.ReadAll(ctx, "/README.md")
			errC <- err
		}()
		<-started
		cancel()
		assert.ErrorIs(t, <-errC, context.Canceled)
		select {
		case <-canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("the fetch was not canceled")
		}
		assert.Zero(t, waiters("/README.md"))
	})
}

func TestContextAndClose(t *testing.T) {
	_, httpServer := newTestServer(t)
	gfs := newTestFS(t, repoURL(httpServer), "main", nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := gfs.ReadAll(ctx, "/seed/hello.txt")
	assert.ErrorIs(t, err, context.Canceled)
	err = gfs.ListDir(ctx, "/seed", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, context.Canceled)
	_, err = New(ctx, repoURL(httpServer), "main", nil)
	assert.ErrorIs(t, err, context.Canceled)

	require.NoError(t, gfs.Close())
	require.NoError(t, gfs.Close())
	_, err = gfs.Stat("/seed")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = gfs.ReadAll(t.Context(), "/seed/hello.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = gfs.Exists("/seed")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

func TestCrossHostRedirectDropsCredentials(t *testing.T) {
	var gotAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		http.NotFound(w, r)
	}))
	t.Cleanup(other.Close)
	// 127.0.0.1 and localhost are different hosts for the redirect policy
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	_, err := New(t.Context(), redirect.URL+"/repo.git", "", &Options{Username: "user", Password: "secret"})
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, gotAuth)
}

func TestRegisteredURIs(t *testing.T) {
	server, httpServer := newTestServer(t)
	gfs := newTestFS(t, repoURL(httpServer), "v1", nil)
	require.NoError(t, fs.Register(gfs))
	assert.Equal(t, "git+"+repoURL(httpServer)+"@v1", gfs.ID())
	assert.Equal(t, "Git filesystem "+repoURL(httpServer)+"@v1", gfs.String())

	content, err := fs.File(gfs.Prefix() + "/seed/sub/deeper/leaf.md").ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "# leaf", content)

	// The same repository and ref can't be registered twice
	again := newTestFS(t, repoURL(httpServer), "v1", nil)
	assert.ErrorIs(t, fs.Register(again), fs.ErrAlreadyRegistered)

	head := newTestFS(t, repoURL(httpServer), "", nil)
	assert.Equal(t, "git+"+repoURL(httpServer)+"@HEAD", head.Prefix())

	err = fs.File(gfs.Prefix()+"/new.txt").WriteAllString(t.Context(), "x")
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)

	// The tag v1 and the branch v1/seed can exist together, the slash
	// of the ref is escaped so that the file /seed/hello.txt of v1 is
	// not the file /hello.txt of v1/seed
	repo := server.Repo
	repo.Refs["refs/heads/v1/seed"] = repo.Commit(repo.Tree(map[string]gittest.File{"hello.txt": {Content: "branch"}}), testCommitTime)
	branch := newTestFS(t, repoURL(httpServer), "v1/seed", nil)
	require.NoError(t, fs.Register(branch))
	assert.Equal(t, "git+"+repoURL(httpServer)+"@v1%2Fseed", branch.Prefix())
	content, err = fs.File(gfs.Prefix() + "/seed/hello.txt").ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Hello, World!", content)
	content, err = fs.File(branch.Prefix() + "/hello.txt").ReadAllString(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "branch", content)
}
