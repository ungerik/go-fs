// fsimpl contains helper functions for implementing a fs.FileSystem
package fsimpl

import (
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"path"
	"strings"
)

// RandomString returns a 120 bit randum number
// encoded as URL compatible base64 string with a length of 20 characters.
func RandomString() string {
	var buffer [15]byte
	b := buffer[:]
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Ext returns the extension of filePath including the point, or an empty string.
// Example:
//   Ext("image.png", "/") == ".png"
//   Ext("image.png/file", "/") == ""
func Ext(filePath, separator string) string {
	if separator != "" {
		filePath = filePath[strings.LastIndex(filePath, separator)+1:]
	}
	p := strings.LastIndexByte(filePath, '.')
	if p == -1 {
		return ""
	}
	return filePath[p:]
}

// TrimExt returns a filePath with a path where the extension is removed.
func TrimExt(filePath, separator string) string {
	sep := -1
	if separator != "" {
		sep = strings.LastIndex(filePath, separator)
	}
	p := strings.LastIndexByte(filePath, '.')
	if p == -1 || p < sep {
		return filePath
	}
	return filePath[:p]
}

// DirAndName is a generic helper for FileSystem.DirAndName implementations.
// path.Split or filepath.Split don't have the wanted behaviour when given a path ending in a separator.
// DirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
// If filePath is the root of the file systeme, then an empty string will be returned as name.
// If filePath does not contain a separator before the name part, then "." will be returned as dir.
func DirAndName(filePath string, volumeLen int, separator string) (dir, name string) {
	if filePath == "" {
		return "", ""
	}

	filePath = strings.TrimSuffix(filePath, separator)

	if filePath == "" {
		return separator, ""
	}

	pos := strings.LastIndex(filePath, separator)
	switch {
	case pos == -1:
		return ".", filePath
	case pos == 0:
		return separator, filePath[1:]
	case pos < volumeLen:
		return filePath, ""
	}

	return filePath[:pos], filePath[pos+1:]
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via path.Match.
// FileSystem implementations can use this function to implement
// FileSystem.MatchAnyPattern they use "/" as path separator.
func MatchAnyPattern(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := path.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

func JoinCleanPath(uriParts []string, trimPrefix, separator string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], trimPrefix)
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	if !strings.HasPrefix(cleanPath, separator) {
		cleanPath = separator + cleanPath
	}
	return path.Clean(cleanPath)
}

func SplitPath(filePath, prefix, separator string) []string {
	filePath = strings.TrimPrefix(filePath, prefix)
	filePath = strings.TrimPrefix(filePath, separator)
	filePath = strings.TrimSuffix(filePath, separator)
	return strings.Split(filePath, separator)
}
