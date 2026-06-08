// Package fsimpl contains helper functions and types for
// implementing a fs.FileSystem.
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
// The first character will never be a dash '-' to avoid issues with
// file names being interpreted as a command line options.
func RandomString() string {
	var data [15]byte
	b := data[:]
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	if s[0] == '-' || s[0] == '_' {
		s = "X" + s[1:]
	}
	return s
}

// Ext returns the extension of filePath including the point, or an empty string.
//
// In contrast to path.Ext (which is fixed to "/") and filepath.Ext (which
// uses the OS specific separator), the path separator is passed as an argument.
// With a non-empty separator only the part after the last separator is
// considered, matching the behavior of path.Ext / filepath.Ext for that separator.
// With an empty separator the whole filePath is searched for the last point,
// so points in directory elements are also considered, unlike path.Ext and
// filepath.Ext.
//
// Example:
//
//	Ext("image.png", "/") == ".png"
//	Ext("image.png/file", "/") == ""          // like path.Ext: last element has no point
//	Ext("image.png/file", "") == ".png/file"  // empty separator searches the whole path
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

// SplitDirAndName is a generic helper for FileSystem.SplitDirAndName implementations.
// path.Split or filepath.Split don't have the wanted behavior when given a path ending in a separator.
// SplitDirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
// If filePath is the root of the file system, then an empty string will be returned as name.
// If filePath does not contain a separator before the name part, then "." will be returned as dir.
func SplitDirAndName(filePath string, volumeLen int, separator string) (dir, name string) {
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

// JoinCleanPath trims trimPrefix from the first of the uriParts,
// joins all parts with path.Join, URL-unescapes the result,
// ensures it begins with a slash and returns it cleaned by path.Clean.
//
// The slash "/" is always used as path separator because the joining
// and cleaning is done by the path package of the standard library.
func JoinCleanPath(uriParts []string, trimPrefix string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], trimPrefix)
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	if !strings.HasPrefix(cleanPath, "/") {
		cleanPath = "/" + cleanPath
	}
	return path.Clean(cleanPath)
}

// SplitPath trims prefix and any leading or trailing separator from filePath
// and splits the remaining path into its elements using separator.
// It returns nil if nothing is left after trimming.
func SplitPath(filePath, prefix, separator string) []string {
	filePath = strings.TrimPrefix(filePath, prefix)
	filePath = strings.Trim(filePath, separator)
	if filePath == "" {
		return nil
	}
	return strings.Split(filePath, separator)
}
