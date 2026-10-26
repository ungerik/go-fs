// Package fsimpl contains helper functions and types for
// implementing a fs.FileSystem.
package fsimpl

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

// RandomString returns a 120 bit random number
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

// EscapeURIPath returns filePath escaped for the path part of a URI:
// every "%" becomes "%25" and nothing else is changed,
// so UnescapeURIPath returns filePath for the result.
//
// fs.ParseRawURI decodes the percent-encoded characters in the path part of a URI,
// so the URI of a path is the prefix of its file system followed by
// the escaped path. That way a file named "a%20b" keeps its name.
func EscapeURIPath(filePath string) string {
	return strings.ReplaceAll(filePath, "%", "%25")
}

// UnescapeURIPath decodes the percent-encoded characters in the path part of a URI,
// the inverse of EscapeURIPath. If uriPath contains an invalid escape,
// like "50% off", it is returned unchanged.
func UnescapeURIPath(uriPath string) string {
	if filePath, err := url.PathUnescape(uriPath); err == nil {
		return filePath
	}
	return uriPath
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
// Like Ext, it takes a name that begins with its only point, like
// ".bashrc", for an extension: TrimExt("dir/.bashrc", "/") is "dir/".
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
// If filePath is the root of the file system or of a volume, then an empty string will be returned as name.
// If filePath does not contain a separator before the name part, then "." will be returned as dir,
// or the volume name if filePath starts with one.
//
// The first volumeLen bytes of filePath are its volume name, like "C:" on Windows.
// A volume root like `C:\` is returned as dir with an empty name, and the
// parent of an element directly below it is the volume root, like `C:\` for `C:\a`.
func SplitDirAndName(filePath string, volumeLen int, separator string) (dir, name string) {
	if filePath == "" {
		return "", ""
	}

	trimmed := strings.TrimSuffix(filePath, separator)
	if trimmed == "" {
		return separator, ""
	}

	pos := strings.LastIndex(trimmed, separator)
	switch {
	case pos == 0:
		return separator, trimmed[len(separator):]
	case volumeLen > 0 && pos < volumeLen && len(trimmed) <= volumeLen:
		// The root of a volume like `C:\` or "C:"
		return filePath, ""
	case volumeLen > 0 && pos < volumeLen:
		// A name directly after a volume without separator like "C:a"
		return trimmed[:volumeLen], trimmed[volumeLen:]
	case volumeLen > 0 && pos == volumeLen:
		// A name directly below the root of a volume like `C:\a`
		return trimmed[:pos+len(separator)], trimmed[pos+len(separator):]
	case pos == -1:
		return ".", trimmed
	}
	return trimmed[:pos], trimmed[pos+len(separator):]
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

// SplitPath trims any leading or trailing separator from filePath
// and splits the remaining path into its elements using separator.
// It returns nil if nothing is left after trimming.
func SplitPath(filePath, separator string) []string {
	filePath = strings.Trim(filePath, separator)
	if filePath == "" {
		return nil
	}
	return strings.Split(filePath, separator)
}

// CheckNoBackslash returns an error wrapping [os.ErrInvalid] if filePath
// contains a backslash. It is the path check of remote file systems with
// the separator "/" whose servers can take a backslash for a separator,
// like servers on Windows: a name like `..\x` is a single name for the
// separator "/", also for fs.SubFileSystem and fs.CopyRecursive, but such
// a server would split it and resolve "..", so the name would reach a file
// outside of the directory that it is meant to be in. The error message
// names the file system with fsName and ends with reason, which tells
// which servers split at a backslash and how to allow backslashes for
// the servers that store them in names, if the file system can.
// The error quotes at most 200 characters of filePath.
func CheckNoBackslash(fsName, filePath, reason string) error {
	if !strings.Contains(filePath, `\`) {
		return nil
	}
	return fmt.Errorf("%w: %s path %.200q contains a backslash, %s", os.ErrInvalid, fsName, filePath, reason)
}
