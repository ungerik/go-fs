package fsimpl

import (
	"path"
	"strings"
)

// PathHelper implements the path related methods of fs.FileSystem
// for a file system with a URI prefix and a path separator,
// so that implementations can embed it instead of duplicating
// the same path handling code.
//
// A file system path is the output of CleanPath: cleaned, using
// the separator of the file system, without the URI prefix, and
// absolute for the file system (starting with the separator or a
// volume) if Rooted is true. The URI of a path is Prefix() followed
// by the path escaped with EscapeURIPath, see URI.
//
// The zero value is not usable, at least URIPrefix must be set.
//
// Methods added to PathHelper must never complete an optional interface
// of package fs that changes behaviour, like fs.HiddenFileSystem or
// fs.AbsPathFileSystem, because every file system embedding a PathHelper
// would then implement it by accident. PrefixAliases is the exception:
// it implements fs.PrefixAliasFileSystem and is harmless without AltPrefixes.
type PathHelper struct {
	// URIPrefix is the prefix of all URIs of the file system,
	// for example "s3://bucket" or "file://".
	URIPrefix string

	// AltPrefixes are additional prefixes that are stripped from
	// URIs like URIPrefix, for example the same prefix with the
	// default port number appended.
	AltPrefixes []string

	// PathSep is the path separator, "/" if empty.
	PathSep string

	// Rooted file systems have paths that are absolute,
	// starting with the separator or a volume name.
	// Non rooted file systems (like HTTP) have paths without
	// a leading separator, for example starting with a host name.
	Rooted bool

	// VolumeLen returns the length of the volume name at the
	// beginning of a path, or zero if the path has no volume.
	// Optional, file systems without volumes leave it nil.
	VolumeLen func(path string) int
}

// Prefix returns the URIPrefix.
func (h PathHelper) Prefix() string {
	return h.URIPrefix
}

// PrefixAliases returns the AltPrefixes,
// implementing fs.PrefixAliasFileSystem.
func (h PathHelper) PrefixAliases() []string {
	return h.AltPrefixes
}

// Separator returns PathSep or "/" if PathSep is empty.
func (h PathHelper) Separator() string {
	if h.PathSep == "" {
		return "/"
	}
	return h.PathSep
}

// TrimPrefix returns uri without URIPrefix or any of the AltPrefixes.
// The longest matching prefix is stripped.
func (h PathHelper) TrimPrefix(uri string) string {
	longest := -1
	if strings.HasPrefix(uri, h.URIPrefix) {
		longest = len(h.URIPrefix)
	}
	for _, alt := range h.AltPrefixes {
		if len(alt) > longest && strings.HasPrefix(uri, alt) {
			longest = len(alt)
		}
	}
	if longest < 0 {
		return uri
	}
	return uri[longest:]
}

func (h PathHelper) volumeLen(filePath string) int {
	if h.VolumeLen == nil {
		return 0
	}
	return h.VolumeLen(filePath)
}

// CleanPath returns the uriParts joined with the separator
// and cleaned as file system path: the URI prefix is stripped
// from the first part, "." and ".." elements and duplicate
// separators are removed, and the path is made absolute (rooted)
// or relative (not rooted) as configured.
// Percent-encoded characters are not decoded, that is the job of fs.ParseRawURI.
// The passed uriParts slice is not modified.
func (h PathHelper) CleanPath(uriParts ...string) string {
	sep := h.Separator()
	var joined string
	if len(uriParts) > 0 {
		joined = h.TrimPrefix(uriParts[0])
		if len(uriParts) > 1 {
			joined += sep + strings.Join(uriParts[1:], sep)
		}
	}
	volume := joined[:h.volumeLen(joined)]
	rest := joined[len(volume):]
	if sep != "/" {
		rest = strings.ReplaceAll(rest, sep, "/")
	}
	if h.Rooted && !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	rest = path.Clean(rest)
	if !h.Rooted {
		rest = strings.TrimPrefix(rest, "/")
		if rest == "." {
			rest = ""
		}
	}
	if sep != "/" {
		rest = strings.ReplaceAll(rest, "/", sep)
	}
	return volume + rest
}

// URI returns the URI of a file system path: URIPrefix followed by
// cleanPath escaped with EscapeURIPath.
//
// If URIPrefix ends with the separator after a host, like "mem://id/",
// the separator is not doubled. A prefix that is only a scheme
// like "file://" is followed by the whole rooted path,
// so the URI of "/a" is "file:///a".
func (h PathHelper) URI(cleanPath string) string {
	sep := h.Separator()
	if strings.HasSuffix(h.URIPrefix, sep) &&
		!strings.HasSuffix(h.URIPrefix, "://") &&
		strings.HasPrefix(cleanPath, sep) {
		cleanPath = cleanPath[len(sep):]
	}
	return h.URIPrefix + EscapeURIPath(cleanPath)
}

// JoinCleanURI returns the URI of the cleaned and joined uriParts.
func (h PathHelper) JoinCleanURI(uriParts ...string) string {
	return h.URI(h.CleanPath(uriParts...))
}

// SplitPath returns the elements of filePath without prefix, volume,
// and leading or trailing separators, or nil for an empty or root path.
func (h PathHelper) SplitPath(filePath string) []string {
	filePath = h.TrimPrefix(filePath)
	return SplitPath(filePath[h.volumeLen(filePath):], h.Separator())
}

// SplitDirAndName returns the parent directory of filePath
// and the name of the last path element, see the function SplitDirAndName.
// Like SplitPath it accepts a filePath with prefix, the returned dir
// has no prefix.
func (h PathHelper) SplitDirAndName(filePath string) (dir, name string) {
	filePath = h.TrimPrefix(filePath)
	return SplitDirAndName(filePath, h.volumeLen(filePath), h.Separator())
}
