package fs

import (
	"cmp"
	"fmt"
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
	// ErrInvalidFileSystem.
	Invalid InvalidFileSystem
)

type fsCount struct {
	fs    FileSystem
	count int
}

var (
	registry       = make(map[string]*fsCount, 2)
	registrySorted = make([]FileSystem, 0, 2)
	registryMtx    sync.RWMutex
)

func init() {
	Register(Local)
	Register(Invalid)
}

// Register adds a file system or increments its reference count
// if it is already registered.
// The function returns the reference file system's reference count.
func Register(fs FileSystem) int {
	prefix := fs.Prefix()
	if prefix == "" {
		panic(fmt.Sprintf("file system with empty prefix: %#v", fs))
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	if regFS, ok := registry[prefix]; ok {
		regFS.count++
		return regFS.count
	}

	registry[prefix] = &fsCount{fs, 1}
	registrySorted = append(registrySorted, fs)
	slices.SortFunc(registrySorted, func(a, b FileSystem) int { return cmp.Compare(a.Prefix(), b.Prefix()) })
	return 1
}

// Unregister a file system decrements its reference count
// and removes it when the reference count reaches 0.
// If the file system is not registered, -1 is returned.
func Unregister(fs FileSystem) int {
	prefix := fs.Prefix()
	if prefix == "" {
		panic(fmt.Sprintf("file system with empty prefix: %#v", fs))
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	regFS, ok := registry[prefix]
	if !ok {
		return -1
	}
	if regFS.count <= 1 {
		delete(registry, prefix)
		registrySorted = slices.DeleteFunc(registrySorted, func(f FileSystem) bool { return f == regFS.fs })
		return 0
	}

	regFS.count--
	return regFS.count
}

// RegisteredFileSystems returns the registered file systems
// sorted by their prefix.
func RegisteredFileSystems() []FileSystem {
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	return slices.Clone(registrySorted)
}

// IsRegistered returns true if the file system is registered
// with its prefix.
func IsRegistered(fs FileSystem) bool {
	if fs == nil {
		return false
	}

	registryMtx.RLock()
	defer registryMtx.RUnlock()

	_, ok := registry[fs.Prefix()]
	return ok
}

// GetFileSystemByPrefixOrNil returns the file system registered
// with the passed prefix, or nil if it can't be found.
func GetFileSystemByPrefixOrNil(prefix string) FileSystem {
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	f, ok := registry[prefix]
	if !ok {
		return nil
	}
	return f.fs
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

// ParseRawURI returns a FileSystem for the passed URI and the clean path within that file system.
// Returns the local file system if no other file system could be identified.
//
// URL escapes in a URI that carries a scheme are decoded, a scheme-less
// local path is used verbatim, so a local file literally named "a%20b" is reachable.
func ParseRawURI(uri string) (fs FileSystem, fsPath string) {
	if uri == "" {
		return Invalid, ""
	}
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	// Find fs with longest matching prefix
	// by iterating in reverse order of sorted registry
	for _, fs = range slices.Backward(registrySorted) {
		if strings.HasPrefix(uri, fs.Prefix()) {
			return fs, uriPath(fs, uri)
		}
	}
	// Then try the prefix aliases of the registered file systems
	for _, fs = range slices.Backward(registrySorted) {
		if aliasFS, ok := fs.(PrefixAliasFileSystem); ok {
			for _, alias := range aliasFS.PrefixAliases() {
				if strings.HasPrefix(uri, alias) {
					return fs, uriPath(fs, uri)
				}
			}
		}
	}

	// No registered file system matched.
	//
	// A URI that carries a scheme (contains the PrefixSeparator "://") but did
	// not match any registered file system must not be treated as a local
	// path: silently resolving e.g. "s3://bucket/key" or "http://host/file"
	// against the local file system would read or write a bogus local path.
	// The only scheme that maps to the local file system is file:// (matched
	// above via the registered Local file system), so any other scheme is
	// reported as the invalid file system instead.
	if strings.Contains(uri, PrefixSeparator) && !strings.HasPrefix(uri, LocalPrefix) {
		return Invalid, uri
	}

	// No scheme: assume uri is a path for the local file system
	return Local, Local.CleanPath(uri)
}

// uriPath returns the clean path of uri within fileSystem,
// the inverse of fsURL: the longest matching prefix of fileSystem
// or one of its prefix aliases is trimmed and the URL escapes
// of the rest are decoded, see [fsimpl.UnescapeURIPath].
// A uri without such a prefix is a path that is only cleaned.
func uriPath(fileSystem FileSystem, uri string) string {
	prefixLen := -1
	if prefix := fileSystem.Prefix(); strings.HasPrefix(uri, prefix) {
		prefixLen = len(prefix)
	}
	if aliasFS, ok := fileSystem.(PrefixAliasFileSystem); ok {
		for _, alias := range aliasFS.PrefixAliases() {
			if len(alias) > prefixLen && strings.HasPrefix(uri, alias) {
				prefixLen = len(alias)
			}
		}
	}
	if prefixLen < 0 {
		return fileSystem.CleanPath(uri)
	}
	return fileSystem.CleanPath(fileSystem.Prefix() + fsimpl.UnescapeURIPath(uri[prefixLen:]))
}
