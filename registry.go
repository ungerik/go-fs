package fs

import "strings"

var (
	// Local is the local file system
	Local = &LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite + AllExecute,
	}

	Invalid InvalidFileSystem

	// Registry contains all registerred file systems.
	// Contains the local file system by default.
	Registry = map[string]FileSystem{
		Local.Prefix():   Local,
		Invalid.Prefix(): Invalid,
	}
)

// Register adds fs to the Registry of file systems.
func Register(fs FileSystem) {
	Registry[fs.Prefix()] = fs
}

// Unregister removes fs from the Registry of file systems.
func Unregister(fs FileSystem) {
	delete(Registry, fs.Prefix())
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
	for prefix, fs := range Registry {
		if strings.HasPrefix(uri, prefix) {
			return fs, uri[len(prefix):]
		}
	}
	return Local, uri
}
