package fsimpl

import (
	"net/url"
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
// volume) if Rooted is true. Prefix()+path is the URI of a path.
//
// The zero value is not usable, at least URIPrefix must be set.
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
// from the first part, URL escapes are decoded, "." and ".."
// elements and duplicate separators are removed, and the path
// is made absolute (rooted) or relative (not rooted) as configured.
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
	if unescaped, err := url.PathUnescape(joined); err == nil {
		joined = unescaped
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

// JoinCleanPath is an alias for CleanPath
// implementing the fs.FileSystem method of that name.
func (h PathHelper) JoinCleanPath(uriParts ...string) string {
	return h.CleanPath(uriParts...)
}

// CleanPathFromURI returns the cleaned file system path of a URI.
func (h PathHelper) CleanPathFromURI(uri string) string {
	return h.CleanPath(uri)
}

// URL returns the URI of a file system path: URIPrefix + cleanPath,
// without doubling the separator if URIPrefix ends with one.
func (h PathHelper) URL(cleanPath string) string {
	sep := h.Separator()
	if strings.HasSuffix(h.URIPrefix, sep) && strings.HasPrefix(cleanPath, sep) {
		return h.URIPrefix + cleanPath[len(sep):]
	}
	return h.URIPrefix + cleanPath
}

// JoinCleanURI returns the URI of the cleaned and joined uriParts.
// Implementations of fs.FileSystem return it as fs.File
// from their JoinCleanFile method.
func (h PathHelper) JoinCleanURI(uriParts ...string) string {
	return h.URL(h.CleanPath(uriParts...))
}

// SplitPath returns the elements of filePath without prefix, volume,
// and leading or trailing separators, or nil for an empty or root path.
func (h PathHelper) SplitPath(filePath string) []string {
	filePath = h.TrimPrefix(filePath)
	return SplitPath(filePath[h.volumeLen(filePath):], "", h.Separator())
}

// SplitDirAndName returns the parent directory of filePath
// and the name of the last path element.
func (h PathHelper) SplitDirAndName(filePath string) (dir, name string) {
	return SplitDirAndName(filePath, h.volumeLen(filePath), h.Separator())
}

// IsAbsPath returns whether filePath is absolute for the file system.
// For rooted file systems that means starting with the separator or a
// volume, for non rooted file systems starting with the URI prefix.
func (h PathHelper) IsAbsPath(filePath string) bool {
	if !h.Rooted {
		return strings.HasPrefix(filePath, h.URIPrefix)
	}
	return strings.HasPrefix(filePath, h.Separator()) || h.volumeLen(filePath) > 0
}

// AbsPath returns filePath in absolute form.
// For rooted file systems that is the cleaned path starting with
// the separator, for non rooted file systems the URI.
func (h PathHelper) AbsPath(filePath string) string {
	if !h.Rooted {
		if h.IsAbsPath(filePath) {
			return filePath
		}
		return h.URL(h.CleanPath(filePath))
	}
	return h.CleanPath(filePath)
}

// IsHidden returns whether the last element of filePath
// begins with a dot.
func (h PathHelper) IsHidden(filePath string) bool {
	_, name := h.SplitDirAndName(filePath)
	return strings.HasPrefix(name, ".")
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match.
func (PathHelper) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return MatchAnyPattern(name, patterns)
}
