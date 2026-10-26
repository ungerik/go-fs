package fs

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/ungerik/go-fs/fsimpl"
)

// PrefixSeparator separates the scheme of a file system URI
// from the rest of the URI, as in "sftp://host/path".
const PrefixSeparator = "://"

var (
	// Local is the local file system
	Local = &LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite,
	}

	// Invalid is the file system of files with an unknown or
	// unregistered prefix. Every operation on it returns
	// an error wrapping ErrInvalidFileSystem.
	Invalid InvalidFileSystem
)

// registryEntry is a registered file system.
// refs counts the references acquired with EnsureRegistered,
// owned is true if EnsureRegistered created the file system,
// so the last free closes it.
type registryEntry struct {
	fs    FileSystem
	refs  int
	owned bool
}

// registryCall is an in-flight create call of EnsureRegistered
// that concurrent callers for the same prefix wait for.
// ctxEnded is true if create failed after the ctx of the calling
// goroutine ended, then err is that caller's own cancellation
// and the waiters try again with their ctx.
type registryCall struct {
	done     chan struct{}
	err      error
	ctxEnded bool
}

var (
	registry       = make(map[string]*registryEntry, 2) // key is the prefix or a prefix alias
	registrySorted = make([]FileSystem, 0, 2)
	registryCalls  = make(map[string]*registryCall)
	registryMtx    sync.RWMutex
)

func init() {
	_ = Register(Local)
	_ = Register(Invalid)
}

// fsPrefixes returns the prefix and the prefix aliases of fileSystem.
func fsPrefixes(fileSystem FileSystem) []string {
	prefixes := []string{fileSystem.Prefix()}
	if aliasFS, ok := fileSystem.(PrefixAliasFileSystem); ok {
		for _, alias := range aliasFS.PrefixAliases() {
			if alias != "" && !slices.Contains(prefixes, alias) {
				prefixes = append(prefixes, alias)
			}
		}
	}
	return prefixes
}

// sameFileSystem compares two file systems by identity
// without panicking for values of non-comparable types.
func sameFileSystem(a, b FileSystem) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// Register adds a file system to the registry under its prefix
// and its prefix aliases (see PrefixAliasFileSystem), so that
// Files with URIs starting with one of them use the file system.
//
// Registering the same file system again is a no-op.
// An error wrapping ErrAlreadyRegistered is returned
// if another file system is registered with the same
// prefix or one of the prefix aliases.
func Register(fileSystem FileSystem) error {
	if fileSystem == nil {
		return fmt.Errorf("can't register nil file system")
	}
	prefixes := fsPrefixes(fileSystem)
	if prefixes[0] == "" {
		return fmt.Errorf("can't register file system with empty prefix: %s", fileSystem)
	}
	for _, prefix := range prefixes {
		if strings.Contains(prefix, PrefixSeparator) && !hasURIScheme(prefix) {
			// After Unregister its URIs would be local paths, see hasURIScheme
			return fmt.Errorf("%w: can't register the prefix %s with a separator before %q", os.ErrInvalid, fsimpl.RedactURI(prefix), PrefixSeparator)
		}
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	return registerLocked(&registryEntry{fs: fileSystem}, prefixes)
}

func registerLocked(entry *registryEntry, prefixes []string) error {
	for _, prefix := range prefixes {
		if existing, ok := registry[prefix]; ok {
			if sameFileSystem(existing.fs, entry.fs) {
				return nil // Already registered
			}
			return fmt.Errorf("%w: %s by %s", ErrAlreadyRegistered, fsimpl.RedactURI(prefix), existing.fs)
		}
	}
	for _, prefix := range prefixes {
		registry[prefix] = entry
	}
	registrySorted = append(registrySorted, entry.fs)
	slices.SortFunc(registrySorted, func(a, b FileSystem) int { return cmp.Compare(a.Prefix(), b.Prefix()) })
	return nil
}

// Unregister removes a file system from the registry
// and reports if it was registered.
//
// Only the passed file system instance is removed,
// another file system registered with the same prefix stays registered.
// File systems usually unregister themselves in their Close method.
func Unregister(fileSystem FileSystem) bool {
	if fileSystem == nil {
		return false
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	return unregisterLocked(fileSystem)
}

func unregisterLocked(fileSystem FileSystem) bool {
	found := false
	for prefix, entry := range registry {
		if sameFileSystem(entry.fs, fileSystem) {
			delete(registry, prefix)
			found = true
		}
	}
	if found {
		registrySorted = slices.DeleteFunc(registrySorted, func(f FileSystem) bool { return sameFileSystem(f, fileSystem) })
	}
	return found
}

// EnsureRegistered returns the file system registered with prefix,
// or calls create to create it and registers it.
//
// Concurrent calls for the same prefix share one call of create,
// which runs without blocking the resolution of other URIs.
// While waiting for another call of create, the passed ctx is honored.
// If that call of create fails, the waiting calls return its error,
// unless it failed after the ctx of its caller ended: then a waiting
// call whose ctx is still alive calls create itself.
// The file system returned by create must have the passed prefix.
//
// Every successful call must be paired with a call of the returned free function,
// which is safe to call more than once. When the last reference is freed,
// a file system created by EnsureRegistered is unregistered and closed.
// A file system that was registered with Register is never closed by free.
// Closing the file system directly unregisters it,
// later calls of free don't do anything then.
func EnsureRegistered(ctx context.Context, prefix string, create func(context.Context) (FileSystem, error)) (fileSystem FileSystem, free func() error, err error) {
	if prefix == "" {
		return nil, nil, fmt.Errorf("EnsureRegistered: empty prefix")
	}
	for {
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		registryMtx.Lock()
		if entry, ok := registry[prefix]; ok {
			entry.refs++
			registryMtx.Unlock()
			return entry.fs, entryFreeFunc(entry), nil
		}
		call, inFlight := registryCalls[prefix]
		if !inFlight {
			call = &registryCall{done: make(chan struct{})}
			registryCalls[prefix] = call
			registryMtx.Unlock()
			return ensureRegisteredCreate(ctx, prefix, call, create)
		}
		registryMtx.Unlock()

		// Wait for the in-flight create call of another goroutine,
		// then try again to get its registered file system,
		// or to create it if the ctx of the other goroutine ended
		select {
		case <-call.done:
			if call.err != nil && !call.ctxEnded {
				return nil, nil, call.err
			}
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func ensureRegisteredCreate(ctx context.Context, prefix string, call *registryCall, create func(context.Context) (FileSystem, error)) (fileSystem FileSystem, free func() error, err error) {
	defer func() {
		registryMtx.Lock()
		delete(registryCalls, prefix)
		registryMtx.Unlock()
		call.err = err
		close(call.done)
	}()

	fileSystem, err = create(ctx)
	if err != nil {
		call.ctxEnded = ctx.Err() != nil
		return nil, nil, err
	}
	if fileSystem.Prefix() != prefix {
		err = fmt.Errorf("EnsureRegistered: created file system has prefix %s instead of %s", fsimpl.RedactURI(fileSystem.Prefix()), fsimpl.RedactURI(prefix))
		return nil, nil, joinCloseErr(err, fileSystem)
	}

	entry := &registryEntry{fs: fileSystem, refs: 1, owned: true}
	registryMtx.Lock()
	err = registerLocked(entry, fsPrefixes(fileSystem))
	registryMtx.Unlock()
	if err != nil {
		return nil, nil, joinCloseErr(err, fileSystem)
	}
	return fileSystem, entryFreeFunc(entry), nil
}

func joinCloseErr(err error, fileSystem FileSystem) error {
	if closeErr := fileSystem.Close(); closeErr != nil {
		return fmt.Errorf("%w (close error: %w)", err, closeErr)
	}
	return err
}

// entryFreeFunc returns the free function of a reference
// to entry acquired by EnsureRegistered.
func entryFreeFunc(entry *registryEntry) func() error {
	var once sync.Once
	return func() (err error) {
		once.Do(func() {
			registryMtx.Lock()
			if registry[entry.fs.Prefix()] != entry {
				// Unregistered meanwhile, for example by Close
				registryMtx.Unlock()
				return
			}
			entry.refs--
			closeFS := entry.refs <= 0 && entry.owned
			if closeFS {
				unregisterLocked(entry.fs)
			}
			registryMtx.Unlock()
			if closeFS {
				err = entry.fs.Close()
			}
		})
		return err
	}
}

// RegisteredFileSystems returns the registered file systems
// sorted by their prefix.
func RegisteredFileSystems() []FileSystem {
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	return slices.Clone(registrySorted)
}

// IsRegistered returns true if the passed file system instance is registered.
func IsRegistered(fileSystem FileSystem) bool {
	if fileSystem == nil {
		return false
	}

	registryMtx.RLock()
	defer registryMtx.RUnlock()

	entry, ok := registry[fileSystem.Prefix()]
	return ok && sameFileSystem(entry.fs, fileSystem)
}

// GetFileSystemByPrefixOrNil returns the file system registered
// with the passed prefix or prefix alias, or nil if it can't be found.
func GetFileSystemByPrefixOrNil(prefix string) FileSystem {
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	entry, ok := registry[prefix]
	if !ok {
		return nil
	}
	return entry.fs
}

// GetFileSystem returns a FileSystem for the passed URI.
// Returns the local file system if no other file system could be identified.
// The URI can be passed as parts that will be joined according to the file system.
func GetFileSystem(uriParts ...string) FileSystem {
	if len(uriParts) == 0 {
		return Invalid
	}
	fs, _ := ParseRawURI(uriParts[0])
	return fs
}

// prefixMatchLen returns the length of prefix if uri starts with it
// at a boundary, or -1. The prefix matches at a boundary if uri ends after
// the prefix, the prefix ends with a slash (like "scheme://"),
// or the prefix is followed by a slash or the separator of the file system.
// So "s3://logs" matches "s3://logs/key" but not "s3://logs-archive/key".
func prefixMatchLen(uri, prefix, separator string) int {
	if prefix == "" || !strings.HasPrefix(uri, prefix) {
		return -1
	}
	rest := uri[len(prefix):]
	switch {
	case rest == "",
		strings.HasSuffix(prefix, "/"),
		strings.HasPrefix(rest, "/"),
		separator != "" && strings.HasPrefix(rest, separator):
		return len(prefix)
	}
	return -1
}

// fsPrefixMatchLen returns the length of the longest prefix
// or prefix alias of fileSystem that uri starts with at a boundary, or -1.
// It doesn't use fsPrefixes, because it runs for every registered
// file system on every URI lookup and must not allocate.
func fsPrefixMatchLen(fileSystem FileSystem, uri string) int {
	separator := fileSystem.Separator()
	longest := prefixMatchLen(uri, fileSystem.Prefix(), separator)
	if aliasFS, ok := fileSystem.(PrefixAliasFileSystem); ok {
		for _, alias := range aliasFS.PrefixAliases() {
			longest = max(longest, prefixMatchLen(uri, alias, separator))
		}
	}
	return longest
}

// ParseRawURI returns a FileSystem for the passed URI and the clean path within that file system.
// Returns the local file system if no other file system could be identified.
//
// The registered file system with the longest prefix or prefix alias
// that the URI starts with at a boundary is used: the prefix "s3://logs"
// matches "s3://logs/key" but not "s3://logs-archive/key".
//
// Percent-encoded characters in a URI that carries a scheme are decoded, a scheme-less
// local path is used verbatim, so a local file literally named "a%20b" is reachable.
//
// File URIs are converted to local paths as defined by RFC 8089,
// see [LocalFileSystem.CleanPath]. A file URI with another host than
// localhost, like "file://server/share/x", is a UNC path on Windows;
// on other platforms it returns the [InvalidFileSystem]. So on Windows
// a file operation on a File from an untrusted file URI can connect to
// any host over SMB, with the automatic NTLM login of Windows, like one
// on an untrusted UNC path. That includes URIs without a host for
// url.Parse, like "file:////server/share/x" and
// "file:////?/UNC/server/share/x", so check the resolved path, not the
// host of the URI: a local path whose volume, see [filepath.VolumeName],
// begins with `\\` and is no `\\?\` path of a drive is on another host.
func ParseRawURI(uri string) (fs FileSystem, fsPath string) {
	if uri == "" {
		return Invalid, ""
	}
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	var (
		bestFS  FileSystem
		bestLen = -1
	)
	for _, fs = range registrySorted {
		if n := fsPrefixMatchLen(fs, uri); n > bestLen {
			bestFS, bestLen = fs, n
		}
	}
	if bestFS != nil {
		if _, ok := bestFS.(*LocalFileSystem); ok && strings.HasPrefix(uri, LocalPrefix) {
			return parseLocalFileURI(bestFS, uri)
		}
		return bestFS, uriPath(bestFS, uri)
	}

	// No registered file system matched.
	//
	// A URI that carries a scheme (the PrefixSeparator "://" with no "/"
	// before it, see hasURIScheme) but did not match any registered file
	// system must not be treated as a local path: silently resolving e.g.
	// "s3://bucket/key" or "http://host/file" against the local file system
	// would read or write a bogus local path.
	// The only scheme that maps to the local file system is file://, which
	// is converted like above also while Local is not registered, so any
	// other scheme is reported as the invalid file system instead, like
	// a file URI that names no local file, see fileURILocalPath.
	if strings.HasPrefix(uri, LocalPrefix) {
		// A file URI while Local is not registered is converted
		// like with Local registered, decoding its path once
		return parseLocalFileURI(Local, uri)
	}
	if hasURIScheme(uri) {
		return Invalid, uri
	}

	// No scheme: assume uri is a path for the local file system,
	// also one with a "://" after a "/", like "/data/https://x"
	return Local, Local.CleanPath(uri)
}

// hasURIScheme reports if s begins with the scheme of a URI: s contains
// the [PrefixSeparator] "://" and no slash or backslash before it, like
// "s3://bucket", a scheme can't contain them. A "://" after a separator,
// like in "/cache/https://x" or `C:\cache\https://x`, is part of a path.
func hasURIScheme(s string) bool {
	i := strings.Index(s, PrefixSeparator)
	return i >= 0 && !strings.ContainsAny(s[:i], `/\`)
}

// isValidNameIn reports if name is valid for an entry of the directory dir,
// see isValidName, and also for the local file system, see
// isValidLocalName, if the writes to dir go to the local file system.
func isValidNameIn(dir File, name string) bool {
	dirFS, dirPath := dir.ParseRawURI()
	if !isValidName(dirFS, name) {
		return false
	}
	if target, _, _, ok := writeTarget(dirFS, dirPath); ok {
		if _, local := target.(*LocalFileSystem); local {
			return isValidLocalName(name)
		}
	}
	return true
}

// parseLocalFileURI returns local and the clean local path of uri, a URI with
// the [LocalPrefix] whose path is percent-encoded like in a File, like
// [LocalFileSystem.CleanPath] of the decoded uri, decoding and converting uri
// once for ParseRawURI. For a file URI that names no local file, see
// fileURILocalPath, like one with a host like "file://server/share/x", which
// has no local path on platforms without UNC paths, it returns the
// [InvalidFileSystem] and uri.
func parseLocalFileURI(local FileSystem, uri string) (FileSystem, string) {
	filePath, isLocal := decodeLocalFileURI(uri)
	switch {
	case !isLocal:
		return Invalid, uri
	case filePath == "":
		// Not cleaned to ".", the working directory
		return local, ""
	}
	return local, cleanLocalPath(filePath, nil)
}

// uriPath returns the clean path of uri within fileSystem,
// the inverse of fsURI: the longest matching prefix of fileSystem
// or one of its prefix aliases is trimmed and the percent-encoded characters
// of the rest are decoded, see [fsimpl.UnescapeURIPath].
// A uri without such a prefix is a path that is only cleaned.
// File systems implementing URIFileSystem map the URI themselves.
func uriPath(fileSystem FileSystem, uri string) string {
	prefixLen := fsPrefixMatchLen(fileSystem, uri)
	if prefixLen < 0 {
		return fileSystem.CleanPath(uri)
	}
	if u, ok := fileSystem.(URIFileSystem); ok {
		return u.PathFromURI(uri)
	}
	return fileSystem.CleanPath(fileSystem.Prefix() + fsimpl.UnescapeURIPath(uri[prefixLen:]))
}
