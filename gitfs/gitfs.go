// Package gitfs implements a read-only file system for one commit of a
// remote git repository, read over the git smart HTTP protocol version 2.
// It only uses the standard library, neither a git binary nor go-git.
//
// New resolves a branch, tag or commit of a repository URL like
// "https://github.com/ungerik/go-fs.git" with two requests to the
// server. Nothing else is downloaded up front: the trees of the
// directories are fetched when a path below them is accessed,
// one directory per request, or all sub-directories with one request
// for a recursive listing. The content of a file is fetched when it is
// read and kept in a cache of Options.CacheSize. The git protocol can't
// return the size of a file without its content, so listing a directory
// fetches the content of its files, all with one request.
//
// The memory used by one response of the server is bounded by
// Options.MaxFetchSize. Listing the sizes of more files than fit into
// it at once splits the request, at the cost of downloading the content
// of some files again.
//
// GitHub shortcuts: for repositories on github.com, the trees are read
// from the GitHub REST API, which returns the sizes of the files without
// their content, and the content of files from raw.githubusercontent.com,
// checked against the object ID of the file in its tree. When the REST API
// fails, for example because the rate limit of 60 requests per hour without
// Options.Token is exceeded, or raw.githubusercontent.com fails, for example
// for a private repository without Options.Token, the git protocol is used
// instead for the rest of the lifetime of the file system. The REST API
// can't represent names that are not valid UTF-8, a directory with an
// entry whose name has a U+FFFD replacement character is read through the
// git protocol, to list the same entries as without the shortcuts.
// So is a tree with more entries than one response of the REST API
// can hold, for a recursive listing of a large repository.
// Options.DisableGitHub turns the shortcuts off.
//
// Reading:
//   - Every file and directory has the time of the commit as modification
//     time, git doesn't store times of files.
//   - Executable files have the permissions 0755, other files 0644 and
//     directories 0755.
//   - Symbolic links and submodules are not exposed. Neither are entries
//     whose names can't be file names of a checkout: names with a slash or
//     backslash, names that are not valid UTF-8, "." and "..", and ".git"
//     in any case.
//   - The FileInfo.Sys of a file or directory is the hex object ID of its
//     blob or tree as string.
//
// The URI prefix of a file system is "git+" followed by the repository
// URL without credentials, "@" and the ref passed to New, or "HEAD" for
// the default branch, like "git+https://github.com/ungerik/go-fs.git@v1.0.0".
// The ref is escaped like a URL path segment, so the slashes of a ref
// like "feature/x" become "%2F" and its prefix can't be confused with
// a directory of the file system of the ref "feature".
package gitfs

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/gitfs/internal/object"
	"github.com/ungerik/go-fs/gitfs/internal/pack"
	"github.com/ungerik/go-fs/gitfs/internal/protocol"
)

const (
	// Separator is the path separator of git file systems.
	Separator = "/"

	// FilePermissions of files that are not executable.
	FilePermissions = fs.UserReadWrite | fs.AllRead
	// ExecutablePermissions of executable files.
	ExecutablePermissions = fs.UserReadWrite | fs.AllRead | fs.AllExecute
	// DirPermissions of directories.
	DirPermissions = fs.UserReadWrite | fs.AllRead | fs.AllExecute

	// maxEntries limits the number of files and directories of a file
	// system, so that a repository whose trees repeat sub-trees can't
	// exhaust the memory.
	maxEntries = 1 << 21

	// maxWantsPerFetch limits the number of objects of one fetch request.
	maxWantsPerFetch = 1000

	// maxRefLength limits the length of a ref passed to New.
	maxRefLength = 4096
)

var (
	// ErrProtocolV2Required is returned by New for servers that
	// don't support git protocol version 2 over smart HTTP.
	ErrProtocolV2Required = protocol.ErrProtocolV2Required

	// ErrRefNotFound is returned by New for a ref that the
	// repository doesn't have.
	ErrRefNotFound = errors.New("git ref not found")
)

var (
	// Compile-time interface checks
	_ fs.FileSystem                 = new(FileSystem)
	_ fs.ExistsFileSystem           = new(FileSystem)
	_ fs.ReadAllFileSystem          = new(FileSystem)
	_ fs.ListDirRecursiveFileSystem = new(FileSystem)
)

// FileSystem is a read-only file system for one commit
// of a remote git repository, see the package documentation.
type FileSystem struct {
	paths         fsimpl.PathHelper
	repoURL       string // without credentials
	ref           string
	client        *protocol.Client
	httpClient    *http.Client
	token         string
	github        *github // nil without the GitHub shortcuts
	cache         *blobCache
	maxObjectSize int64
	maxFetchSize  int64

	// ctx is canceled by Close, it is used by the methods without context
	ctx    context.Context
	cancel context.CancelFunc

	// loadSem serializes the requests that load the commit and the
	// trees, a channel instead of a mutex so that waiting for it ends
	// with the context of the caller, see lockLoad
	loadSem chan struct{}

	// mtx protects the fields below and the nodes
	mtx             sync.Mutex
	closed          bool
	commit          object.OID
	modified        time.Time
	root            *node // nil until the commit is loaded
	numEntries      int
	githubAPIFailed bool
	githubRawFailed bool
	inflight        map[object.OID]*blobFetch
}

// node is a file or directory of the commit.
type node struct {
	path     string // slash separated without leading slash, "" for the root
	name     string
	isDir    bool
	exec     bool
	oid      object.OID
	size     int64            // of a file, -1 until known
	children map[string]*node // of a directory, nil until loaded
}

// treeEntry is an entry of a tree with the size of its blob or -1.
type treeEntry struct {
	object.TreeEntry
	size int64
}

// blobFetch is a fetch of a blob that concurrent readers wait for.
type blobFetch struct {
	done    chan struct{} // closed when data and err are set
	cancel  context.CancelFunc
	waiters int // protected by FileSystem.mtx
	data    []byte
	err     error
}

// New returns a file system for the commit of a repository that ref
// resolves to: a branch, a tag, a full ref name like "refs/heads/main",
// a full hex commit ID, or the default branch of the repository
// (its HEAD) if ref is empty. A branch is preferred over a tag of the
// same name. The commit stays the same for the lifetime of the file
// system, also when the branch moves on.
//
// The repository URL is the HTTP or HTTPS URL that git clone accepts.
// See Options for authentication. New returns ErrRefNotFound for a
// missing ref, ErrProtocolV2Required for a server that doesn't support
// git protocol version 2, and an error wrapping os.ErrNotExist or
// os.ErrPermission if the server answers 404 or 401 and 403.
//
// The file system is not registered, pass it to fs.Register to access
// its files via their URIs and Close it after use. Registering a second
// file system with the same repository URL and ref fails with
// fs.ErrAlreadyRegistered while the first one is registered.
func New(ctx context.Context, repoURL, ref string, opts *Options) (*FileSystem, error) {
	if opts == nil {
		opts = new(Options)
	}
	u, err := url.Parse(repoURL)
	if err != nil {
		return nil, fmt.Errorf("gitfs: invalid repository URL %q: %w", fsimpl.RedactURI(repoURL), err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("gitfs: repository URL %q must use https or http: %w", fsimpl.RedactURI(repoURL), os.ErrInvalid)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("gitfs: repository URL %q must not have a query or fragment: %w", fsimpl.RedactURI(repoURL), os.ErrInvalid)
	}
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	username, password := opts.Username, opts.Password
	if username == "" && u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	u.User = nil
	cleanURL := strings.TrimSuffix(u.String(), "/")

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	token := opts.Token // copied, the closure must not see later changes of opts
	authorize := func(*http.Request) {}
	switch {
	case token != "":
		if username == "" {
			username = "x-access-token"
		}
		authorize = func(r *http.Request) { r.SetBasicAuth(username, token) }
	case username != "":
		authorize = func(r *http.Request) { r.SetBasicAuth(username, password) }
	}

	refLabel := ref
	if refLabel == "" {
		refLabel = "HEAD"
	}
	maxObjectSize := positiveOr(opts.MaxObjectSize, DefaultMaxObjectSize)
	f := &FileSystem{
		paths:         fsimpl.PathHelper{URIPrefix: "git+" + cleanURL + "@" + url.PathEscape(refLabel), Rooted: true},
		repoURL:       cleanURL,
		ref:           ref,
		client:        &protocol.Client{URL: cleanURL, HTTP: httpClient, Authorize: authorize},
		httpClient:    httpClient,
		token:         token,
		cache:         newBlobCache(positiveOr(opts.CacheSize, DefaultCacheSize)),
		maxObjectSize: maxObjectSize,
		maxFetchSize:  max(positiveOr(opts.MaxFetchSize, DefaultMaxFetchSize), maxObjectSize),
		loadSem:       make(chan struct{}, 1),
		inflight:      make(map[object.OID]*blobFetch),
	}
	switch {
	case opts.github != nil:
		f.github = opts.github
	case !opts.DisableGitHub:
		f.github = githubRepo(u)
	}

	err = f.client.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitfs: %s: %w", cleanURL, err)
	}
	isOID := f.client.Format.IsOID(ref)
	if isOID {
		f.commit, _ = f.client.Format.ParseOID(ref)
	} else {
		// A branch wins over a tag of the same name
		var candidates []string
		switch {
		case ref == "" || ref == "HEAD":
			candidates = []string{"HEAD"}
		case strings.HasPrefix(ref, "refs/"):
			candidates = []string{ref}
		default:
			candidates = []string{"refs/heads/" + ref, "refs/tags/" + ref}
		}
		refs, err := f.client.LsRefs(ctx, candidates...)
		if err != nil {
			return nil, fmt.Errorf("gitfs: %s: listing refs: %w", cleanURL, err)
		}
	resolve:
		for _, candidate := range candidates {
			for _, r := range refs {
				if r.Name == candidate {
					f.commit = cmp.Or(r.Peeled, r.OID)
					break resolve
				}
			}
		}
		switch {
		case f.commit != "":
		case ref == "":
			return nil, fmt.Errorf("gitfs: %s: %w: the repository has no HEAD", cleanURL, ErrRefNotFound)
		default:
			return nil, fmt.Errorf("gitfs: %s: %w: %q", cleanURL, ErrRefNotFound, ref)
		}
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	if isOID {
		// Check that the object exists and is a commit
		_, err = f.rootNode(ctx)
		if err != nil {
			f.cancel()
			return nil, err
		}
	}
	return f, nil
}

// validateRef returns an error wrapping os.ErrInvalid for a ref that
// can't be the name of a git ref: one with a control character or
// space, see git-check-ref-format(1), or longer than maxRefLength.
func validateRef(ref string) error {
	if len(ref) > maxRefLength {
		return fmt.Errorf("gitfs: ref of %d bytes is longer than %d bytes: %w", len(ref), maxRefLength, os.ErrInvalid)
	}
	for _, c := range []byte(ref) {
		if c <= ' ' || c == 0x7f {
			return fmt.Errorf("gitfs: ref %q has a control character or space: %w", ref, os.ErrInvalid)
		}
	}
	return nil
}

func positiveOr(value, defaultValue int64) int64 {
	if value <= 0 {
		return defaultValue
	}
	return value
}

// rootNode returns the root directory of the commit,
// fetching the commit if it was not loaded yet.
func (f *FileSystem) rootNode(ctx context.Context) (*node, error) {
	f.mtx.Lock()
	root := f.root
	f.mtx.Unlock()
	if root != nil {
		return root, nil
	}

	err := f.lockLoad(ctx)
	if err != nil {
		return nil, err
	}
	defer f.unlockLoad()
	f.mtx.Lock()
	root, oid := f.root, f.commit
	f.mtx.Unlock()
	if root != nil {
		return root, nil
	}
	// Peel annotated tags until the commit, which is
	// fetched without its trees and without its parents
	for range 10 {
		request := protocol.FetchRequest{Wants: []object.OID{oid}}
		if f.client.FetchFeature("shallow") {
			request.Deepen = 1
		}
		if f.client.FetchFeature("filter") {
			request.Filter = "tree:0"
		}
		objects, err := f.fetch(ctx, request)
		if err != nil {
			return nil, err
		}
		obj := objects[oid]
		switch {
		case obj == nil:
			return nil, fmt.Errorf("%s: the server did not send the object %s", f, oid)
		case obj.Type == object.TypeTag:
			tag, err := f.client.Format.ParseTag(obj.Data)
			if err != nil {
				return nil, fmt.Errorf("%s: tag %s: %w", f, oid, err)
			}
			oid = tag.Object
			continue
		case obj.Type != object.TypeCommit:
			return nil, fmt.Errorf("%s: object %s is a %s, not a commit: %w", f, oid, obj.Type, os.ErrInvalid)
		}
		commit, err := f.client.Format.ParseCommit(obj.Data)
		if err != nil {
			return nil, fmt.Errorf("%s: commit %s: %w", f, oid, err)
		}
		root = &node{isDir: true, oid: commit.Tree}
		f.mtx.Lock()
		// A server without filter support sent all trees and blobs
		err = f.populateFromPack(root, objects)
		if err == nil {
			f.commit, f.modified, f.root = oid, commit.Committed, root
		}
		f.mtx.Unlock()
		if err != nil {
			return nil, err
		}
		return root, nil
	}
	return nil, fmt.Errorf("%s: too many nested tags", f)
}

// lockLoad waits until no other request loads the commit or trees,
// or until ctx is done. Call unlockLoad after a nil error.
func (f *FileSystem) lockLoad(ctx context.Context) error {
	select {
	case f.loadSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *FileSystem) unlockLoad() {
	<-f.loadSem
}

// fetch requests objects from the server and returns them by OID.
func (f *FileSystem) fetch(ctx context.Context, request protocol.FetchRequest) (map[object.OID]*pack.Object, error) {
	var objects map[object.OID]*pack.Object
	err := f.client.Fetch(ctx, request, func(r io.Reader) (err error) {
		objects, err = pack.Read(r, f.client.Format, pack.Limits{MaxObjectSize: f.maxObjectSize, MaxTotalSize: f.maxFetchSize})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%s: fetch: %w", f, err)
	}
	return objects, nil
}

// populateFromPack adds the entries of the trees and the sizes of the
// blobs in objects to dir and its sub-directories, see populate.
// The blobs are cached. f.mtx must be locked.
func (f *FileSystem) populateFromPack(dir *node, objects map[object.OID]*pack.Object) error {
	format := f.client.Format
	err := f.populate(dir, func(n *node) ([]treeEntry, bool, error) {
		obj := objects[n.oid]
		if obj == nil {
			return nil, false, nil
		}
		if obj.Type != object.TypeTree {
			return nil, false, fmt.Errorf("%s: object %s of directory %q is a %s, not a tree", f, n.oid, n.path, obj.Type)
		}
		entries, err := format.ParseTree(obj.Data)
		if err != nil {
			return nil, false, fmt.Errorf("%s: tree %s of directory %q: %w", f, n.oid, n.path, err)
		}
		withSizes := make([]treeEntry, len(entries))
		for i, e := range entries {
			withSizes[i] = treeEntry{TreeEntry: e, size: -1}
			if blob := objects[e.OID]; blob != nil && blob.Type == object.TypeBlob {
				withSizes[i].size = int64(len(blob.Data))
			}
		}
		return withSizes, true, nil
	})
	if err != nil {
		return err
	}
	for oid, obj := range objects {
		if obj.Type == object.TypeBlob {
			f.cache.put(oid, obj.Data)
		}
	}
	return nil
}

// populate adds the entries that entriesOf returns to the directory
// n if it was not loaded yet, then descends into its sub-directories.
// entriesOf returns false for a directory it has no entries for.
// f.mtx must be locked.
func (f *FileSystem) populate(n *node, entriesOf func(*node) ([]treeEntry, bool, error)) error {
	if n.children == nil {
		entries, ok, err := entriesOf(n)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		// The children are only set when all entries were added, so
		// that an error doesn't leave a partially loaded directory
		children := make(map[string]*node, len(entries))
		for _, e := range entries {
			if !isValidName(e.Name) {
				continue
			}
			child := &node{
				path: strings.TrimPrefix(n.path+"/"+e.Name, "/"),
				name: e.Name,
				oid:  e.OID,
				size: -1,
			}
			switch {
			case e.Mode == object.ModeDir:
				child.isDir = true
			case e.Mode&0o170000 == 0o100000:
				child.exec = e.Mode&0o111 != 0
				child.size = e.size
			default:
				continue // symbolic links and submodules
			}
			children[e.Name] = child
			if f.numEntries+len(children) > maxEntries {
				return fmt.Errorf("%s: the commit has more than %d files and directories", f, maxEntries)
			}
		}
		f.numEntries += len(children)
		n.children = children
	}
	for _, child := range n.children {
		if child.isDir {
			err := f.populate(child, entriesOf)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// isValidName reports whether a tree entry name can be the name of
// a file of a checkout, see the package documentation.
func isValidName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.EqualFold(name, ".git") &&
		!strings.ContainsAny(name, `/\`) &&
		utf8.ValidString(name)
}

// isLoaded reports whether the directory n is loaded,
// with recursive also all its sub-directories.
// f.mtx must be locked.
func isLoaded(n *node, recursive bool) bool {
	if n.children == nil {
		return false
	}
	if recursive {
		for _, child := range n.children {
			if child.isDir && !isLoaded(child, true) {
				return false
			}
		}
	}
	return true
}

// loadDir loads the entries of the directory n,
// with recursive also those of all its sub-directories.
func (f *FileSystem) loadDir(ctx context.Context, n *node, recursive bool) error {
	f.mtx.Lock()
	loaded := isLoaded(n, recursive)
	f.mtx.Unlock()
	if loaded {
		return nil
	}

	err := f.lockLoad(ctx)
	if err != nil {
		return err
	}
	defer f.unlockLoad()
	f.mtx.Lock()
	loaded, githubAPI := isLoaded(n, recursive), f.github != nil && !f.githubAPIFailed
	f.mtx.Unlock()
	if loaded {
		return nil
	}

	if githubAPI {
		entries, err := f.fetchGitHubTree(ctx, n.oid, recursive)
		if err == nil {
			f.mtx.Lock()
			defer f.mtx.Unlock()
			// The response has the entries of n and, with
			// recursive, of all its sub-directories
			return f.populate(n, func(d *node) ([]treeEntry, bool, error) {
				if !recursive && d != n {
					return nil, false, nil
				}
				return entries[strings.TrimPrefix(strings.TrimPrefix(d.path, n.path), "/")], true, nil
			})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, errGitHubLossyName) && !errors.Is(err, errGitHubTruncated) {
			// Rate limited, private repository without token, ...: use
			// git for the rest of the lifetime. A tree too large for one
			// response is read through git, the API still serves the
			// smaller trees and their sizes without the content
			f.mtx.Lock()
			f.githubAPIFailed = true
			f.mtx.Unlock()
		}
	}

	request := protocol.FetchRequest{Wants: []object.OID{n.oid}}
	if f.client.FetchFeature("filter") {
		// A wanted tree is always sent, tree:0 omits all others,
		// blob:none sends all its sub-trees
		request.Filter = "tree:0"
		if recursive {
			request.Filter = "blob:none"
		}
	}
	objects, err := f.fetch(ctx, request)
	if err != nil {
		return err
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	err = f.populateFromPack(n, objects)
	if err != nil {
		return err
	}
	if !isLoaded(n, recursive) {
		return fmt.Errorf("%s: the server did not send the tree %s of directory %q", f, n.oid, n.path)
	}
	return nil
}

// loadSizes fetches the blobs of the files
// whose sizes are unknown and sets their sizes.
func (f *FileSystem) loadSizes(ctx context.Context, files []*node) error {
	f.mtx.Lock()
	var oids []object.OID
	for _, file := range files {
		if file.size < 0 {
			if data, ok := f.cache.get(file.oid); ok {
				file.size = int64(len(data))
			} else {
				oids = append(oids, file.oid)
			}
		}
	}
	f.mtx.Unlock()
	if len(oids) == 0 {
		return nil
	}
	slices.Sort(oids)
	oids = slices.Compact(oids)

	sizes := make(map[object.OID]int64, len(oids))
	for chunk := range slices.Chunk(oids, maxWantsPerFetch) {
		err := f.fetchBlobSizes(ctx, chunk, sizes)
		if err != nil {
			return err
		}
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	for _, file := range files {
		if size, ok := sizes[file.oid]; ok {
			file.size = size
		}
	}
	return nil
}

// fetchBlobSizes fetches the blobs, caches them and adds their sizes to
// sizes. A response above Options.MaxFetchSize is fetched again in two
// halves. The size of a single blob above Options.MaxObjectSize is taken
// from its header in the packfile, without its content.
func (f *FileSystem) fetchBlobSizes(ctx context.Context, oids []object.OID, sizes map[object.OID]int64) error {
	objects, err := f.fetch(ctx, protocol.FetchRequest{Wants: oids})
	var tooLarge *pack.ObjectTooLargeError
	switch {
	case err == nil:
	case len(oids) > 1 && errors.Is(err, pack.ErrTooLarge):
		half := len(oids) / 2
		err = f.fetchBlobSizes(ctx, oids[:half], sizes)
		if err != nil {
			return err
		}
		return f.fetchBlobSizes(ctx, oids[half:], sizes)
	case len(oids) == 1 && errors.As(err, &tooLarge) && tooLarge.Type == object.TypeBlob:
		// The packfile of a single blob without haves has
		// only that blob, and never as delta
		sizes[oids[0]] = tooLarge.Size
		return nil
	default:
		return err
	}
	for _, oid := range oids {
		blob := objects[oid]
		if blob == nil || blob.Type != object.TypeBlob {
			return fmt.Errorf("%s: the server did not send the blob %s", f, oid)
		}
		f.cache.put(oid, blob.Data)
		sizes[oid] = int64(len(blob.Data))
	}
	return nil
}

// readBlob returns the content of a file, which must not be modified.
// Concurrent reads of the same blob share one request, which is not
// canceled with the context of one reader but when all readers stopped
// waiting for it or the file system is closed.
func (f *FileSystem) readBlob(ctx context.Context, file *node) ([]byte, error) {
	if data, ok := f.cache.get(file.oid); ok {
		return data, nil
	}
	f.mtx.Lock()
	fetch, ok := f.inflight[file.oid]
	if !ok {
		fetchCtx, cancel := context.WithCancel(f.ctx)
		fetch = &blobFetch{done: make(chan struct{}), cancel: cancel}
		f.inflight[file.oid] = fetch
		go f.fetchBlob(fetchCtx, file, fetch)
	}
	fetch.waiters++
	f.mtx.Unlock()

	select {
	case <-fetch.done:
		return fetch.data, fetch.err
	case <-ctx.Done():
		f.mtx.Lock()
		fetch.waiters--
		if fetch.waiters == 0 {
			// A later reader starts a new fetch
			fetch.cancel()
			if f.inflight[file.oid] == fetch {
				delete(f.inflight, file.oid)
			}
		}
		f.mtx.Unlock()
		return nil, ctx.Err()
	}
}

// fetchBlob downloads the content of a file for the readers of fetch.
func (f *FileSystem) fetchBlob(ctx context.Context, file *node, fetch *blobFetch) {
	defer fetch.cancel()
	data, err := f.downloadBlob(ctx, file)
	if err == nil {
		// Cached before the fetch is removed from
		// inflight, so that later readers find it
		f.cache.put(file.oid, data)
	}
	f.mtx.Lock()
	if f.inflight[file.oid] == fetch {
		delete(f.inflight, file.oid)
	}
	if err == nil {
		file.size = int64(len(data))
	}
	f.mtx.Unlock()
	fetch.data, fetch.err = data, err
	close(fetch.done)
}

// downloadBlob returns the content of a file from raw.githubusercontent.com
// on github.com, else or if that fails through the git protocol.
func (f *FileSystem) downloadBlob(ctx context.Context, file *node) ([]byte, error) {
	f.mtx.Lock()
	size, useRaw := file.size, f.github != nil && !f.githubRawFailed
	f.mtx.Unlock()
	if size > f.maxObjectSize {
		return nil, fmt.Errorf("%s: file %q has the size %d above the limit %d", f, file.path, size, f.maxObjectSize)
	}
	if useRaw {
		data, err := f.fetchGitHubRaw(ctx, file)
		switch {
		case err == nil:
			return data, nil
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, pack.ErrTooLarge):
			return nil, fmt.Errorf("%s: %w", f, err)
		case errors.Is(err, errGitHubRawMismatch):
			// Like a file of Git LFS, git returns the blob
		default:
			// Private repository without token, ...:
			// use git for the rest of the lifetime
			f.mtx.Lock()
			f.githubRawFailed = true
			f.mtx.Unlock()
		}
	}
	objects, err := f.fetch(ctx, protocol.FetchRequest{Wants: []object.OID{file.oid}})
	if err != nil {
		return nil, err
	}
	blob := objects[file.oid]
	if blob == nil || blob.Type != object.TypeBlob {
		return nil, fmt.Errorf("%s: the server did not send the blob %s of %q", f, file.oid, file.path)
	}
	return blob.Data, nil
}

// splitPath returns the elements of a cleaned file system path.
func splitPath(filePath string) []string {
	var elements []string
	for element := range strings.SplitSeq(filePath, Separator) {
		if element != "" && element != "." {
			elements = append(elements, element)
		}
	}
	return elements
}

// lookup returns the node of a path, loading the trees along it.
func (f *FileSystem) lookup(ctx context.Context, filePath string) (*node, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	current, err := f.rootNode(ctx)
	if err != nil {
		return nil, err
	}
	for _, element := range splitPath(filePath) {
		if !current.isDir {
			return nil, fs.NewErrDoesNotExist(f.file(filePath))
		}
		err := f.loadDir(ctx, current, false)
		if err != nil {
			return nil, err
		}
		f.mtx.Lock()
		child := current.children[element]
		f.mtx.Unlock()
		if child == nil {
			return nil, fs.NewErrDoesNotExist(f.file(filePath))
		}
		current = child
	}
	return current, nil
}

// lookupFile returns the node of a file or an error
// wrapping os.ErrNotExist or fs.ErrIsDirectory.
func (f *FileSystem) lookupFile(ctx context.Context, filePath string) (*node, error) {
	n, err := f.lookup(ctx, filePath)
	if err != nil {
		return nil, err
	}
	if n.isDir {
		return nil, fs.NewErrIsDirectory(f.file(filePath))
	}
	return n, nil
}

// info returns the FileInfo of a node. f.mtx must be locked.
func (f *FileSystem) info(n *node) *fs.FileInfo {
	perm := FilePermissions
	switch {
	case n.isDir:
		perm = DirPermissions
	case n.exec:
		perm = ExecutablePermissions
	}
	info := &fs.FileInfo{
		File:        fs.File(f.paths.JoinCleanURI(n.path)),
		Name:        n.name,
		Exists:      true,
		IsDir:       n.isDir,
		IsRegular:   !n.isDir,
		IsHidden:    strings.HasPrefix(n.name, "."),
		Modified:    f.modified,
		Permissions: perm,
		Sys:         string(n.oid),
	}
	if !n.isDir {
		info.Size = n.size
	}
	if n.path == "" {
		info.File = f.RootDir()
		info.Name = Separator
	}
	return info
}

// Commit returns the hex ID of the commit of the file system.
func (f *FileSystem) Commit() string {
	return string(f.commitOID())
}

func (f *FileSystem) commitOID() object.OID {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.commit
}

// Ref returns the ref that was passed to New.
func (f *FileSystem) Ref() string {
	return f.ref
}

// RepositoryURL returns the URL of the repository without credentials.
func (f *FileSystem) RepositoryURL() string {
	return f.repoURL
}

// ReadableWritable returns true for readable and false for writable.
func (f *FileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

// RootDir returns the root directory of the commit.
func (f *FileSystem) RootDir() fs.File {
	return fs.File(f.paths.URIPrefix + Separator)
}

// ID returns the URI prefix of the file system.
func (f *FileSystem) ID() string {
	return f.paths.URIPrefix
}

// Prefix returns the URI prefix of the file system, see the package documentation.
func (f *FileSystem) Prefix() string {
	return f.paths.URIPrefix
}

// Name returns "Git filesystem".
func (f *FileSystem) Name() string {
	return "Git filesystem"
}

// String returns the name of the file system, the repository URL and the ref.
func (f *FileSystem) String() string {
	return f.Name() + " " + strings.TrimPrefix(f.paths.URIPrefix, "git+")
}

// Separator returns "/".
func (f *FileSystem) Separator() string {
	return Separator
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (f *FileSystem) CleanPath(uriParts ...string) string {
	return f.paths.CleanPath(uriParts...)
}

func (f *FileSystem) file(filePath string) fs.File {
	return fs.File(f.paths.JoinCleanURI(filePath))
}

func (f *FileSystem) checkClosed() error {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.closed {
		return fmt.Errorf("%s: %w", f, fs.ErrFileSystemClosed)
	}
	return nil
}

// Stat returns the FileInfo of a file or directory.
// For a file whose size is not known yet, its content is fetched.
func (f *FileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	n, err := f.lookup(f.ctx, filePath)
	if err != nil {
		return nil, err
	}
	if !n.isDir {
		err = f.loadSizes(f.ctx, []*node{n})
		if err != nil {
			return nil, err
		}
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.info(n), nil
}

// Exists reports if the commit contains the path.
func (f *FileSystem) Exists(filePath string) (bool, error) {
	if err := f.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	_, err := f.lookup(f.ctx, filePath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, err
}

// ListDir calls the callback for every file and directory in dirPath
// whose name matches any of the patterns, or for all of them if no
// patterns are passed. Directories are listed first, then files,
// each sorted by name.
func (f *FileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive calls the callback for every file (not directory)
// below dirPath whose name matches any of the patterns, or for all of
// them if no patterns are passed. All sub-directories are loaded with
// one request.
func (f *FileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return f.listDir(ctx, dirPath, patterns, callback, true)
}

func (f *FileSystem) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error, recursive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := f.lookup(ctx, dirPath)
	if err != nil {
		return err
	}
	if !dir.isDir {
		return fs.NewErrIsNotDirectory(f.file(dirPath))
	}
	err = f.loadDir(ctx, dir, recursive)
	if err != nil {
		return err
	}

	// Collect the matching entries, the files first
	// to load their sizes, then their FileInfos
	var matches []*node
	f.mtx.Lock()
	var collect func(d *node) error
	collect = func(d *node) error {
		children := slices.SortedFunc(maps.Values(d.children), func(a, b *node) int {
			if a.isDir != b.isDir {
				if a.isDir {
					return -1
				}
				return 1
			}
			return strings.Compare(a.name, b.name)
		})
		for _, child := range children {
			if recursive && child.isDir {
				err := collect(child)
				if err != nil {
					return err
				}
				continue
			}
			match, err := fsimpl.MatchAnyPattern(child.name, patterns)
			if err != nil {
				return err
			}
			if match {
				matches = append(matches, child)
			}
		}
		return nil
	}
	err = collect(dir)
	f.mtx.Unlock()
	if err != nil {
		return err
	}

	var files []*node
	for _, n := range matches {
		if !n.isDir {
			files = append(files, n)
		}
	}
	err = f.loadSizes(ctx, files)
	if err != nil {
		return err
	}
	infos := make([]*fs.FileInfo, len(matches))
	f.mtx.Lock()
	for i, n := range matches {
		infos[i] = f.info(n)
	}
	f.mtx.Unlock()
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = callback(info)
		if err != nil {
			return err
		}
	}
	return nil
}

// OpenReader returns a reader for the content of a file,
// which is fetched before OpenReader returns. The reader
// implements io/fs.File, io.ReaderAt and io.Seeker.
func (f *FileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	n, err := f.lookupFile(f.ctx, filePath)
	if err != nil {
		return nil, err
	}
	data, err := f.readBlob(f.ctx, n)
	if err != nil {
		return nil, err
	}
	f.mtx.Lock()
	info := f.info(n)
	f.mtx.Unlock()
	return fsimpl.NewReadOnlyFileBuffer(bytes.Clone(data), info.StdFileInfo()), nil
}

// ReadAll returns the content of a file.
func (f *FileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := f.lookupFile(ctx, filePath)
	if err != nil {
		return nil, err
	}
	data, err := f.readBlob(ctx, n)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(data), nil
}

// Close frees the cache and unregisters the file system.
// Close is idempotent.
func (f *FileSystem) Close() error {
	f.mtx.Lock()
	if f.closed {
		f.mtx.Unlock()
		return nil
	}
	f.closed = true
	f.mtx.Unlock()
	f.cancel()
	fs.Unregister(f)
	f.cache.close()
	return nil
}
