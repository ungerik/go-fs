// Package httpfs implements a read only file system
// for HTTP URLs.
// Import it to register FileSystem and FileSystemTLS:
//
//	import _ "github.com/ungerik/go-fs/httpfs"
//
// Requests are made with Client, which can be replaced
// to configure timeouts, proxies or transports.
package httpfs

import (
	"context"
	"fmt"
	"io"
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
	// Prefix is the URI prefix of FileSystem.
	Prefix = "http://"
	// PrefixTLS is the URI prefix of FileSystemTLS.
	PrefixTLS = "https://"
	// Separator is the path separator of the file systems.
	Separator = "/"
)

var (
	// FileSystem is the read-only file system for "http://" URLs.
	FileSystem = &fileSystem{PathHelper: fsimpl.PathHelper{URIPrefix: Prefix}}

	// FileSystemTLS is the read-only file system for "https://" URLs.
	FileSystemTLS = &fileSystem{PathHelper: fsimpl.PathHelper{URIPrefix: PrefixTLS}}

	// Client is used for all HTTP requests of the file systems.
	Client = http.DefaultClient
)

// fileSystem paths are not rooted, they start with the host name:
// Prefix()+path is the URL.
type fileSystem struct {
	fsimpl.PathHelper
}

func (*fileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

func (*fileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

func (f *fileSystem) ID() string {
	return strings.TrimSuffix(f.URIPrefix, "://")
}

func (f *fileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.URIPrefix, "://"))
}

func (f *fileSystem) String() string {
	return f.Name() + " read-only file system"
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
	response, err := Client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
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
			File:        fs.File(url),
			Name:        name,
			Exists:      true,
			IsRegular:   true,
			IsHidden:    strings.HasPrefix(name, "."),
			Size:        response.ContentLength,
			Modified:    modifiedTime(response),
			Permissions: fs.AllRead,
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
	response, err = Client.Get(url) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
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
		File:        fs.File(url),
		Name:        name,
		Exists:      true,
		IsRegular:   true,
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        size,
		Modified:    modifiedTime(response),
		Permissions: fs.AllRead,
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

func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	info, err := f.info(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(f.URL(filePath)))
	}
	return &info, nil
}

func (f *fileSystem) Exists(filePath string) (bool, error) {
	info, err := f.info(filePath)
	if err != nil {
		return false, err
	}
	return info.Exists, nil
}

// get returns the response of a GET request with ctx.
// A 404 or 410 status is reported as an error wrapping os.ErrNotExist.
func (f *fileSystem) get(ctx context.Context, filePath string) (*http.Response, error) {
	url := f.URL(filePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := Client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return nil, err
	}
	switch {
	case isNotExistStatus(response.StatusCode):
		_ = response.Body.Close()
		return nil, fs.NewErrDoesNotExist(fs.File(url))
	case !isSuccessStatus(response.StatusCode):
		_ = response.Body.Close()
		return nil, fmt.Errorf("HTTPFileSystem: unexpected status %s for %s", response.Status, url)
	}
	return response, nil
}

// ReadAll downloads the URL with a GET request using ctx.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	response, err := f.get(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return fs.ReadAllContext(ctx, response.Body)
}

// OpenReader streams the body of a GET request.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	response, err := f.get(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (f *fileSystem) Close() error {
	return nil
}

func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return fs.NewErrUnsupported(f, "ListDir")
}
