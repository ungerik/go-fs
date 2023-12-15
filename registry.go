package fs

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

const PrefixSeparator = "://"

var (
	// Local is the local file system
	Local = &LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite,
	}

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
	sort.Slice(registrySorted, func(i, j int) bool { return registrySorted[i].Prefix() < registrySorted[j].Prefix() })
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
	registryMtx.Lock()
	defer registryMtx.Unlock()

	return slices.Clone(registrySorted)
}

// IsRegistered returns true if the file system is registered.
func IsRegistered(fs FileSystem) bool {
	if fs == nil {
		return false
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	_, ok := registry[fs.Prefix()]
	return ok
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

// ParseRawURI returns a FileSystem for the passed URI and the path component within that file system.
// Returns the local file system if no other file system could be identified.
func ParseRawURI(uri string) (fs FileSystem, fsPath string) {
	if uri == "" {
		return Invalid, ""
	}
	registryMtx.RLock()
	defer registryMtx.RUnlock()

	// Find fs with longest matching prefix
	// by iterating in reverse order of sorted registry
	for i := len(registrySorted) - 1; i >= 0; i-- {
		fs = registrySorted[i]
		path, found := strings.CutPrefix(uri, fs.Prefix())
		if found {
			return fs, path
		}
	}

	// No file system found, assume uri is for the local file system
	return Local, uri
}
