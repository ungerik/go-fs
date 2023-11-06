package fs

import (
	"fmt"
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

	// Registry contains all registerred file systems.
	// Contains the local file system by default.
	Registry = map[string]FileSystem{
		Local.Prefix():   Local,   // file://
		Invalid.Prefix(): Invalid, // invalid://
	}

	registryMtx sync.RWMutex
)

// Register adds fs to the Registry of file systems.
func Register(fs FileSystem) {
	prefix := fs.Prefix()
	if prefix == "" {
		panic(fmt.Sprintf("file system with empty prefix: %#v", fs))
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	Registry[prefix] = fs
}

// Unregister removes fs from the Registry of file systems.
func Unregister(fs FileSystem) {
	prefix := fs.Prefix()
	if prefix == "" {
		panic(fmt.Sprintf("file system with empty prefix: %#v", fs))
	}

	registryMtx.Lock()
	defer registryMtx.Unlock()

	delete(Registry, prefix)
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
	for prefix, regFS := range Registry {
		if strings.HasPrefix(uri, prefix) {
			path := uri[len(prefix):]
			if fs == nil || len(prefix) > len(fs.Prefix()) {
				fs = regFS
				fsPath = path
			}
		}
	}

	if fs == nil {
		// No file system found, assume uri is for the local file system
		return Local, uri
	}
	return fs, fsPath
}
