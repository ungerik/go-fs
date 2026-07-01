// Package httpfs implements a read only file system
// for HTTP URLs.
// Import it to register FileSystem and FileSystemTLS:
//
//	import _ "github.com/ungerik/go-fs/httpfs"
package httpfs

import (
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

func init() {
	fs.Register(FileSystem)
	fs.Register(FileSystemTLS)
}

const (
	Prefix    = "http://"
	PrefixTLS = "https://"
	Separator = "/"
)

var (
	FileSystem    = &fileSystem{prefix: Prefix}
	FileSystemTLS = &fileSystem{prefix: PrefixTLS}
)

type fileSystem struct {
	fs.ReadOnlyBase

	prefix string
}

func (*fileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

func (f *fileSystem) ID() (string, error) {
	return strings.TrimSuffix(f.prefix, "://"), nil
}

func (f *fileSystem) Prefix() string {
	return f.prefix
}

func (f *fileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.prefix, "://"))
}

func (f *fileSystem) String() string {
	return f.Name() + " read-only file system"
}

func (f *fileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *fileSystem) CleanPathFromURI(uri string) string {
	return path.Clean(strings.TrimPrefix(uri, f.prefix))
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

func (f *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, f.prefix)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), f.Separator())
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, f.prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return f.prefix + strings.TrimPrefix(filePath, Separator)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

// info determines whether filePath exists and, if so, returns its FileInfo.
//
// The returned error distinguishes "could not determine existence" from
// "definitely does not exist":
//   - A 2xx response yields a FileInfo with Exists==true and a nil error.
//   - A 404 or 410 response yields a zero FileInfo (Exists==false) and a nil
//     error, because the resource definitively does not exist.
//   - A transport failure or any other non-2xx status (401, 403, 429, 5xx, ...)
//     yields a non-nil error, because existence is unknown. Such errors must
//     not be reported as "does not exist", otherwise a flaky network or an
//     auth failure would make existing files appear to vanish.
func (f *fileSystem) info(filePath string) (fs.FileInfo, error) {
	url := f.URL(filePath)

	// First try a fast HEAD request.
	request, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return fs.FileInfo{}, err
	}
	name := path.Base(request.URL.Path)
	response, err := http.DefaultClient.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return fs.FileInfo{}, err
	}
	_ = response.Body.Close() // HEAD response body is empty; close error is irrelevant

	switch {
	case isNotExistStatus(response.StatusCode):
		// The resource definitively does not exist.
		return fs.FileInfo{}, nil

	case isSuccessStatus(response.StatusCode) && response.ContentLength >= 0:
		return fs.FileInfo{
			Exists:   true,
			Name:     name,
			Size:     response.ContentLength,
			Modified: modifiedTime(response),
		}, nil

	case isSuccessStatus(response.StatusCode):
		// 2xx but no Content-Length from HEAD: fall through to a GET request.

	case response.StatusCode == http.StatusMethodNotAllowed,
		response.StatusCode == http.StatusNotImplemented:
		// The server does not support HEAD: fall through to a GET request.

	default:
		// 401, 403, 429, 5xx, ...: existence is unknown.
		return fs.FileInfo{}, fmt.Errorf("HTTPFileSystem.info: unexpected status %s for %s", response.Status, url)
	}

	// Fall back to a full GET request.
	response, err = http.DefaultClient.Get(url) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return fs.FileInfo{}, err
	}
	defer response.Body.Close()

	switch {
	case isNotExistStatus(response.StatusCode):
		return fs.FileInfo{}, nil
	case !isSuccessStatus(response.StatusCode):
		return fs.FileInfo{}, fmt.Errorf("HTTPFileSystem.info: unexpected status %s for %s", response.Status, url)
	}

	size := response.ContentLength
	if size < 0 {
		// Read full body if still no ContentLength available
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return fs.FileInfo{}, err
		}
		size = int64(len(body))
	}

	return fs.FileInfo{
		Exists:   true,
		Name:     name,
		Size:     size,
		Modified: modifiedTime(response),
	}, nil
}

// isSuccessStatus reports whether statusCode is a 2xx HTTP status.
func isSuccessStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode <= 299
}

// isNotExistStatus reports whether statusCode means the resource does not exist.
func isNotExistStatus(statusCode int) bool {
	return statusCode == http.StatusNotFound || statusCode == http.StatusGone
}

// modifiedTime returns the modification time of a response from its
// Last-Modified header, falling back to the Date header, or the zero time
// if neither is a valid HTTP time.
func modifiedTime(response *http.Response) time.Time {
	modified, err := http.ParseTime(response.Header.Get("Last-Modified"))
	if err != nil {
		modified, err = http.ParseTime(response.Header.Get("Date"))
		if err != nil {
			return time.Time{}
		}
	}
	return modified
}

func (f *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info, err := f.info(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

func (f *fileSystem) Exists(filePath string) bool {
	info, err := f.info(filePath)
	return err == nil && info.Exists
}

func (f *fileSystem) IsHidden(filePath string) bool       { return false }
func (f *fileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	return fs.NewErrUnsupported(f, "ListDirInfo")
}

func (f *fileSystem) ReadAll(ctx context.Context, filePath string) (data []byte, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// TODO use HTTP GET with context
	reader, err := f.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.ReadAll: %w", err)
	}

	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.ReadAll: %w", err)
	}

	return data, nil
}

func (f *fileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Get(f.URL(filePath)) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %d: %s", response.StatusCode, response.Status)
	}
	defer response.Body.Close()
	return fsimpl.NewReadonlyFileBufferReadAll(response.Body, info)
}

func (f *fileSystem) Close() error {
	return nil
}
