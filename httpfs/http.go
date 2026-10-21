// Package httpfs implements a read only file system
// for HTTP URLs.
// Import it to register Default and DefaultTLS
// for all "http://" and "https://" URLs:
//
//	import _ "github.com/ungerik/go-fs/httpfs"
//
// Use NewAndRegister to register a file system with its own
// http.Client for a more specific prefix like "https://api.example.com/",
// URLs starting with that prefix then use it instead of DefaultTLS.
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
	_ = fs.Register(Default)
	_ = fs.Register(DefaultTLS)
}

const (
	// Prefix is the URI prefix of Default.
	Prefix = "http://"
	// PrefixTLS is the URI prefix of DefaultTLS.
	PrefixTLS = "https://"
	// Separator is the path separator of the file systems.
	Separator = "/"
)

var (
	// Default is the read-only file system for "http://" URLs
	// using http.DefaultClient. It is registered by the package.
	Default = mustNew(Prefix, nil)

	// DefaultTLS is the read-only file system for "https://" URLs
	// using http.DefaultClient. It is registered by the package.
	DefaultTLS = mustNew(PrefixTLS, nil)
)

// Options for New and NewAndRegister.
type Options struct {
	// Client is used for all HTTP requests of the file system.
	// http.DefaultClient is used if nil.
	Client *http.Client
}

// FileSystem is a read-only file system for HTTP URLs.
// Its paths are not rooted, they start with the host name.
type FileSystem struct {
	paths  fsimpl.PathHelper
	client *http.Client
}

// New returns a read-only file system for URLs starting with prefix,
// which must start with "http://" or "https://", like "https://api.example.com/".
// The file system is not registered, see NewAndRegister.
func New(prefix string, opts *Options) (*FileSystem, error) {
	if !strings.HasPrefix(prefix, Prefix) && !strings.HasPrefix(prefix, PrefixTLS) {
		return nil, fmt.Errorf("httpfs prefix must start with %s or %s: %q", Prefix, PrefixTLS, prefix)
	}
	client := http.DefaultClient
	if opts != nil && opts.Client != nil {
		client = opts.Client
	}
	return &FileSystem{
		paths:  fsimpl.PathHelper{URIPrefix: prefix},
		client: client,
	}, nil
}

// NewAndRegister registers a read-only file system for URLs starting with prefix,
// see New. URLs with that prefix then use this file system instead of
// Default or DefaultTLS, for example with a client that authenticates requests.
func NewAndRegister(prefix string, opts *Options) error {
	f, err := New(prefix, opts)
	if err != nil {
		return err
	}
	return fs.Register(f)
}

func mustNew(prefix string, opts *Options) *FileSystem {
	f, err := New(prefix, opts)
	if err != nil {
		panic(err)
	}
	return f
}

// Prefix returns the URI prefix of the file system.
func (f *FileSystem) Prefix() string {
	return f.paths.URIPrefix
}

// Separator returns "/".
func (f *FileSystem) Separator() string {
	return f.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned path of the file system, starting with the host name.
func (f *FileSystem) CleanPath(uriParts ...string) string {
	return f.paths.CleanPath(uriParts...)
}

func (*FileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

func (*FileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

func (f *FileSystem) ID() string {
	return strings.TrimSuffix(f.paths.URIPrefix, "://")
}

func (f *FileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.paths.URIPrefix, "://"))
}

func (f *FileSystem) String() string {
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
func (f *FileSystem) info(filePath string) (fs.FileInfo, error) {
	url := f.paths.URL(filePath)

	// First try a fast HEAD request.
	request, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return fs.FileInfo{}, err
	}
	name := path.Base(request.URL.Path)
	response, err := f.client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
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
	response, err = f.client.Get(url) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
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

func (f *FileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	info, err := f.info(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(f.paths.URL(filePath)))
	}
	return &info, nil
}

func (f *FileSystem) Exists(filePath string) (bool, error) {
	info, err := f.info(filePath)
	if err != nil {
		return false, err
	}
	return info.Exists, nil
}

// get returns the response of a GET request with ctx.
// A 404 or 410 status is reported as an error wrapping os.ErrNotExist.
func (f *FileSystem) get(ctx context.Context, filePath string) (*http.Response, error) {
	url := f.paths.URL(filePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := f.client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
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
func (f *FileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	response, err := f.get(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return fs.ReadAll(ctx, response.Body)
}

// OpenReader streams the body of a GET request.
func (f *FileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	response, err := f.get(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (f *FileSystem) Close() error {
	return nil
}

func (f *FileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return fs.NewErrUnsupported(f, "ListDir")
}
