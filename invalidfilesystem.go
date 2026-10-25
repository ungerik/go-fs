package fs

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ FileSystem            = InvalidFileSystem("")
	_ WriteFileSystem       = InvalidFileSystem("")
	_ ReadWriterFileSystem  = InvalidFileSystem("")
	_ MakeAllDirsFileSystem = InvalidFileSystem("")
	_ URIFileSystem         = InvalidFileSystem("")
)

// InvalidFileSystem is the file system of the files whose URI has a scheme
// without a registered file system, see ParseRawURI, and of InvalidFile.
// The variable Invalid of this type is registered with the prefix "invalid://".
//
// Every operation returns an error wrapping ErrInvalidFileSystem that names
// the URI with a redacted password, like
// `invalid file system: no file system registered for "s3://bucket/x"`.
// The error does not wrap os.ErrNotExist, because the file is not missing,
// it can't be accessed at all. The operations of the File API on InvalidFile,
// which has an empty path, return ErrEmptyPath.
//
// The paths of the file system are the URIs without a registered file system,
// which keep their scheme and authority when cleaned or joined, so that
// File.Dir and File.Join return URIs of the same unregistered scheme.
//
// The underlying string value is the optional name
// of the file system and will be added to the URI prefix.
// It can be used to register different dummy file systems
// for debugging or testing purposes.
type InvalidFileSystem string

// ID returns the same string as String.
func (fs InvalidFileSystem) ID() string {
	return fs.String()
}

// Prefix returns "invalid://", extended with the name
// of the file system if the underlying string is not empty.
func (fs InvalidFileSystem) Prefix() string {
	if fs == "" {
		return "invalid://"
	}
	return "invalid://" + strings.Trim(string(fs), "/") + "/"
}

// Name returns the underlying string of the file system
// or "invalid file system" if it is empty.
func (fs InvalidFileSystem) Name() string {
	if fs == "" {
		return "invalid file system"
	}
	return string(fs)
}

// String returns "invalid file system", followed by
// the underlying string if it is not empty.
func (fs InvalidFileSystem) String() string {
	if fs == "" {
		return "invalid file system"
	}
	return "invalid file system" + " " + string(fs)
}

// Separator returns "/".
func (InvalidFileSystem) Separator() string {
	return "/"
}

// ReadableWritable returns true, true so that the readable and writable
// gates of the package let every operation through to the methods
// of the invalid file system, which return ErrInvalidFileSystem
// with the URI instead of ErrReadOnlyFileSystem or ErrWriteOnlyFileSystem.
func (InvalidFileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

// RootDir returns InvalidFile.
func (InvalidFileSystem) RootDir() File {
	return InvalidFile
}

// CleanPath joins and cleans the uriParts. The scheme and authority
// of a URI without registered file system, like "s3://bucket",
// are kept and the rest is cleaned as rooted path.
// Other paths are cleaned without a leading separator,
// because the prefix already ends with a slash (like httpfs).
func (fs InvalidFileSystem) CleanPath(uriParts ...string) string {
	helper := fsimpl.PathHelper{URIPrefix: fs.Prefix()}
	if len(uriParts) == 0 {
		return helper.CleanPath()
	}
	first := helper.TrimPrefix(uriParts[0])
	n := uriVolumeLen(first)
	if n == 0 {
		return helper.CleanPath(uriParts...)
	}
	rest := path.Clean("/" + strings.Join(append([]string{first[n:]}, uriParts[1:]...), "/"))
	if rest == "/" {
		return first[:n]
	}
	return first[:n] + rest
}

// PathFromURI returns the clean path of a URI with the prefix
// of the file system, implementing URIFileSystem.
// Percent-encoded characters after the prefix are decoded.
func (fs InvalidFileSystem) PathFromURI(uri string) string {
	return fs.CleanPath(fs.Prefix() + fsimpl.UnescapeURIPath(strings.TrimPrefix(uri, fs.Prefix())))
}

// URIFromPath returns the URI of a clean path, implementing URIFileSystem.
// A path that is a URI without registered file system is returned as is,
// other paths get the prefix of the file system.
func (fs InvalidFileSystem) URIFromPath(cleanPath string) string {
	if uriVolumeLen(cleanPath) > 0 {
		return cleanPath
	}
	return fsimpl.PathHelper{URIPrefix: fs.Prefix()}.URI(cleanPath)
}

// SplitDirAndName returns the parent directory and the name of the last
// element of filePath, implementing URIFileSystem.
// The scheme and authority of a URI are never split,
// "s3://bucket" is the root directory of "s3://bucket/x".
func (InvalidFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, uriVolumeLen(filePath), "/")
}

// uriVolumeLen returns the length of the scheme and authority of uri,
// like "s3://bucket" of "s3://bucket/x", or 0 if uri has no scheme.
func uriVolumeLen(uri string) int {
	i := strings.Index(uri, PrefixSeparator)
	if i <= 0 || strings.Contains(uri[:i], "/") {
		return 0
	}
	start := i + len(PrefixSeparator)
	if j := strings.IndexByte(uri[start:], '/'); j >= 0 {
		return start + j
	}
	return len(uri)
}

// Stat returns ErrInvalidFileSystem.
func (InvalidFileSystem) Stat(filePath string) (*FileInfo, error) {
	return nil, errInvalidFile(filePath)
}

// ListDir returns ErrInvalidFileSystem without calling the callback.
func (InvalidFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	return errInvalidFile(dirPath)
}

// OpenReader returns ErrInvalidFileSystem.
func (InvalidFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return nil, errInvalidFile(filePath)
}

// OpenWriter returns ErrInvalidFileSystem.
func (InvalidFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	return nil, errInvalidFile(filePath)
}

// OpenReadWriter returns ErrInvalidFileSystem,
// the emulation would only fail at the first read.
func (InvalidFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	return nil, errInvalidFile(filePath)
}

// MakeDir returns ErrInvalidFileSystem.
func (InvalidFileSystem) MakeDir(dirPath string, perm Permissions) error {
	return errInvalidFile(dirPath)
}

// MakeAllDirs returns ErrInvalidFileSystem for dirPath,
// the emulation would report the topmost parent directory.
func (InvalidFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	return errInvalidFile(dirPath)
}

// Remove returns ErrInvalidFileSystem.
func (InvalidFileSystem) Remove(filePath string) error {
	return errInvalidFile(filePath)
}

// Close does nothing and returns nil,
// the invalid file system can't be closed.
func (InvalidFileSystem) Close() error {
	return nil
}

// errInvalidFile returns an error wrapping ErrInvalidFileSystem
// for a path or URI of a file without a registered file system.
// The password of a URI with user info is redacted.
func errInvalidFile(filePath string) error {
	if filePath == "" {
		return ErrInvalidFileSystem
	}
	return fmt.Errorf("%w: no file system registered for %q", ErrInvalidFileSystem, fsimpl.RedactURI(filePath))
}
