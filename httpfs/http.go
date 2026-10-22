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
//
// URLs are used verbatim, see FileSystem.
package httpfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
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

	_ fs.URIFileSystem     = new(FileSystem)
	_ fs.ExistsFileSystem  = new(FileSystem)
	_ fs.ReadAllFileSystem = new(FileSystem)
)

// Options for New and NewAndRegister.
type Options struct {
	// Client is used for all HTTP requests of the file system.
	// http.DefaultClient is used if nil.
	Client *http.Client
}

// FileSystem is a read-only file system for HTTP URLs.
//
// It implements fs.URIFileSystem: its paths are URLs in their escaped
// form without the prefix, so an URL is requested exactly as it was
// passed. The query string and the fragment are never decoded or
// cleaned, so "?q=a%26b", "?to=https://example.com//x" and the
// signature of a presigned URL arrive unchanged, escapes like "%2F" or
// "%3F" in the path stay escaped, and a trailing slash is kept. Only
// "." and ".." elements and duplicate slashes of the path are cleaned.
//
// The paths of Default and DefaultTLS start with the host, which is
// treated like a volume: no ".." element leads to another host, so
// File("http://example.com/files").Join("..", "..", "evil.example")
// is "http://example.com/evil.example". The paths of a file system
// with a prefix that contains a host, like "https://api.example.com/v1/",
// are relative to that prefix and can't leave it.
//
// File.Name is the last path element in its escaped form, without the
// query and the fragment, and empty for the URL of a host without path.
// File.Dir and File.Join drop the query and the fragment, and the
// joined parts are path elements: a "?" or "#" in them is escaped.
type FileSystem struct {
	prefix string
	// hostInPath is true for a prefix that is only a scheme,
	// then the host is the first element of the paths.
	hostInPath bool
	client     *http.Client
}

// New returns a read-only file system for URLs starting with prefix,
// which must start with "http://" or "https://", like "https://api.example.com/".
// The file system is not registered, see NewAndRegister.
func New(prefix string, opts *Options) (*FileSystem, error) {
	if !strings.HasPrefix(prefix, Prefix) && !strings.HasPrefix(prefix, PrefixTLS) {
		return nil, fmt.Errorf("httpfs prefix must start with %s or %s: %q", Prefix, PrefixTLS, prefix)
	}
	if strings.ContainsAny(prefix, "?#") {
		return nil, fmt.Errorf("httpfs prefix must not contain a query or fragment: %q", prefix)
	}
	client := http.DefaultClient
	if opts != nil && opts.Client != nil {
		client = opts.Client
	}
	return &FileSystem{
		prefix:     prefix,
		hostInPath: prefix == Prefix || prefix == PrefixTLS,
		client:     client,
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
	return f.prefix
}

// Separator returns "/".
func (f *FileSystem) Separator() string {
	return Separator
}

// splitQuery splits an URL or path at the first "?" or "#"
// into the path and the verbatim query and fragment.
func splitQuery(uri string) (path, queryAndFragment string) {
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		return uri[:i], uri[i:]
	}
	return uri, ""
}

// pathElementEscaper escapes the characters of a joined path element
// that would start a query or fragment of the URL.
var pathElementEscaper = strings.NewReplacer("?", "%3F", "#", "%23")

// CleanPath joins the uriParts with the separator and returns the
// cleaned path of the file system, see FileSystem for the rules.
// The prefix is stripped from the first part. Further parts are path
// elements in escaped form; joining them drops the query and the
// fragment of the first part.
func (f *FileSystem) CleanPath(uriParts ...string) string {
	if len(uriParts) == 0 {
		return ""
	}
	filePath, query := splitQuery(strings.TrimPrefix(uriParts[0], f.prefix))
	if len(uriParts) > 1 {
		query = ""
		var b strings.Builder
		b.WriteString(filePath)
		for _, part := range uriParts[1:] {
			b.WriteString(Separator)
			b.WriteString(pathElementEscaper.Replace(part))
		}
		filePath = b.String()
	}
	return f.cleanPath(filePath) + query
}

// hostLen returns the length of the host at the start of filePath,
// or zero if the paths of the file system don't start with a host.
func (f *FileSystem) hostLen(filePath string) int {
	if !f.hostInPath {
		return 0
	}
	if i := strings.Index(filePath, Separator); i >= 0 {
		return i
	}
	return len(filePath)
}

// cleanPath cleans a path without query below its host,
// keeping a trailing slash. Elements that clean up to
// the root are removed, so "h/a/.." is "h".
func (f *FileSystem) cleanPath(filePath string) string {
	host := filePath[:f.hostLen(filePath)]
	rest := filePath[len(host):]
	leadingSlash := strings.HasPrefix(rest, Separator)
	trailingSlash := strings.HasSuffix(rest, Separator)
	cleaned := path.Clean(Separator + rest)
	if cleaned == Separator {
		if leadingSlash && trailingSlash {
			return host + Separator
		}
		return host
	}
	if !leadingSlash {
		cleaned = cleaned[len(Separator):]
	}
	if trailingSlash {
		cleaned += Separator
	}
	return host + cleaned
}

// PathFromURI returns the clean path of an URL starting with the prefix,
// implementing fs.URIFileSystem. URL escapes are not decoded.
func (f *FileSystem) PathFromURI(uri string) string {
	return f.CleanPath(uri)
}

// URIFromPath returns the URL of a clean path, which is the prefix
// followed by the path, implementing fs.URIFileSystem.
func (f *FileSystem) URIFromPath(cleanPath string) string {
	return f.prefix + cleanPath
}

// SplitDirAndName returns the parent directory of filePath and the name
// of its last element, without the query and the fragment, implementing
// fs.URIFileSystem. The name is empty for the URL of a host without path,
// which is the parent of the elements directly below the host.
func (f *FileSystem) SplitDirAndName(filePath string) (dir, name string) {
	filePath, _ = splitQuery(filePath)
	hostLen := f.hostLen(filePath)
	dir, name = fsimpl.SplitDirAndName(filePath, hostLen, Separator)
	switch {
	case dir == ".":
		dir = "" // the root of a prefix that contains the host
	case hostLen > 0 && name != "" && dir == filePath[:hostLen]+Separator:
		dir = filePath[:hostLen]
	}
	return dir, name
}

// ReadableWritable returns true for readable and false for writable.
func (*FileSystem) ReadableWritable() (readable, writable bool) {
	return true, false
}

// RootDir returns fs.InvalidFile, HTTP has no root directory to list.
func (*FileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

// ID returns the prefix without a trailing "://",
// so "http" or "https" for Default and DefaultTLS
// and the whole prefix for a prefix with a host.
func (f *FileSystem) ID() string {
	return strings.TrimSuffix(f.prefix, "://")
}

// Name returns "HTTP" or "HTTPS".
func (f *FileSystem) Name() string {
	if strings.HasPrefix(f.prefix, PrefixTLS) {
		return "HTTPS"
	}
	return "HTTP"
}

// String returns the name and the prefix of the file system
// with the password of the prefix redacted.
func (f *FileSystem) String() string {
	return f.Name() + " " + redactURL(f.prefix)
}

// redactURL returns rawURL for error messages and logs: without the
// query and fragment, which can carry secrets like the signature of a
// presigned URL, and with a password replaced by "xxxxx".
func redactURL(rawURL string) string {
	withoutQuery, query := splitQuery(rawURL)
	if u, err := url.Parse(withoutQuery); err == nil && u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			withoutQuery = u.Redacted()
		}
	}
	if query != "" {
		withoutQuery += "?…"
	}
	return withoutQuery
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
//
// A HEAD request is tried first. If it reports no Content-Length or the
// server doesn't support HEAD, a GET for the first byte is sent and the
// size is taken from the Content-Range of the answer, so the content is
// never downloaded to determine the size. The Size is -1 if the server
// reports no size at all.
func (f *FileSystem) info(filePath string) (fs.FileInfo, error) {
	uri := f.URIFromPath(filePath)
	request, err := http.NewRequest(http.MethodHead, uri, nil)
	if err != nil {
		return fs.FileInfo{}, fmt.Errorf("httpfs: %w", err)
	}
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
		return f.fileInfo(filePath, response.ContentLength, response), nil

	case isSuccessStatus(response.StatusCode),
		response.StatusCode == http.StatusMethodNotAllowed,
		response.StatusCode == http.StatusNotImplemented:
		// No Content-Length or no HEAD support: ask for the first byte
		return f.rangeInfo(filePath)

	default:
		// 401, 403, 429, 5xx, ...: existence is unknown.
		return fs.FileInfo{}, statusError(response, uri)
	}
}

// rangeInfo returns the FileInfo of filePath from a GET request
// for the first byte, see info.
func (f *FileSystem) rangeInfo(filePath string) (fs.FileInfo, error) {
	uri := f.URIFromPath(filePath)
	request, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return fs.FileInfo{}, fmt.Errorf("httpfs: %w", err)
	}
	request.Header.Set("Range", "bytes=0-0")
	response, err := f.client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return fs.FileInfo{}, err
	}
	// Never read more than a few bytes, a server that ignores the
	// range sends the whole content. Draining a short body lets the
	// client reuse the connection.
	_, _ = io.CopyN(io.Discard, response.Body, 512)
	_ = response.Body.Close()

	switch {
	case isNotExistStatus(response.StatusCode):
		return fs.FileInfo{}, nil
	case response.StatusCode == http.StatusPartialContent:
		return f.fileInfo(filePath, contentRangeSize(response), response), nil
	case response.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// The first byte doesn't exist, so the resource is empty
		size := contentRangeSize(response)
		if size < 0 {
			size = 0
		}
		return f.fileInfo(filePath, size, response), nil
	case isSuccessStatus(response.StatusCode):
		// The server ignored the range, ContentLength is -1 if unknown
		return f.fileInfo(filePath, response.ContentLength, response), nil
	default:
		return fs.FileInfo{}, statusError(response, uri)
	}
}

// contentRangeSize returns the complete length of a Content-Range header
// like "bytes 0-0/1234" or "bytes */1234", or -1 if it is unknown.
func contentRangeSize(response *http.Response) int64 {
	contentRange := response.Header.Get("Content-Range")
	i := strings.LastIndexByte(contentRange, '/')
	if !strings.HasPrefix(contentRange, "bytes ") || i < 0 {
		return -1
	}
	size, err := strconv.ParseInt(contentRange[i+1:], 10, 64)
	if err != nil || size < 0 {
		return -1 // "*" for an unknown length
	}
	return size
}

// fileInfo returns the FileInfo of an existing file.
func (f *FileSystem) fileInfo(filePath string, size int64, response *http.Response) fs.FileInfo {
	_, name := f.SplitDirAndName(filePath)
	if name == "" {
		// The URL of a host without path, its name is the host
		name = response.Request.URL.Host
	}
	return fs.FileInfo{
		File:        fs.File(f.URIFromPath(filePath)),
		Name:        name,
		Exists:      true,
		IsRegular:   true,
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        size,
		Modified:    modifiedTime(response),
		Permissions: fs.AllRead,
	}
}

// statusError returns the error for an unexpected status of a response.
func statusError(response *http.Response, uri string) error {
	return fmt.Errorf("httpfs: unexpected status %s for %s", response.Status, redactURL(uri))
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

// Stat returns the FileInfo of the URL, see FileSystem.
// The size comes from the Content-Length of a HEAD request,
// or from the Content-Range of a GET request for the first byte
// if the server reports no Content-Length for HEAD.
// It is -1 if the server reports no size at all.
func (f *FileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	info, err := f.info(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(f.URIFromPath(filePath)))
	}
	return &info, nil
}

// Exists reports if the URL exists, see Stat.
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
	uri := f.URIFromPath(filePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("httpfs: %w", err)
	}
	response, err := f.client.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return nil, err
	}
	switch {
	case isNotExistStatus(response.StatusCode):
		_ = response.Body.Close()
		return nil, fs.NewErrDoesNotExist(fs.File(uri))
	case !isSuccessStatus(response.StatusCode):
		_ = response.Body.Close()
		return nil, statusError(response, uri)
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

// Close does nothing, the file system has no resources to release.
func (f *FileSystem) Close() error {
	return nil
}

// ListDir returns an error wrapping errors.ErrUnsupported,
// because HTTP has no directory listing.
func (f *FileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return fs.NewErrUnsupported(f, "ListDir")
}
