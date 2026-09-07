// Package webdavfs implements a WebDAV client file system
// with the standard library only.
//
// Paths map directly to URL paths below the base URL. Stat and ListDir
// use PROPFIND, reads use GET with Range requests for seeking, writes
// PUT, directories MKCOL, and Move and CopyFile the native MOVE and
// COPY methods. One module covers every WebDAV server: Nextcloud,
// ownCloud, Apache, nginx, SharePoint, or the golang.org/x/net/webdav
// handler.
package webdavfs

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of webdavfs URIs, followed by the host and base path of the server
	Prefix = "webdav://"

	// Separator used in webdavfs paths
	Separator = "/"
)

var (
	// DefaultPermissions reported for files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute

	// Compile-time interface checks
	_ fs.FileSystem          = new(fileSystem)
	_ fs.WriteFileSystem     = new(fileSystem)
	_ fs.ReadAllFileSystem   = new(fileSystem)
	_ fs.WriteAllFileSystem  = new(fileSystem)
	_ fs.MoveFileSystem      = new(fileSystem)
	_ fs.CopyFileSystem      = new(fileSystem)
	_ fs.RemoveAllFileSystem = new(fileSystem)
)

// Options for a WebDAV file system. A nil *Options uses the defaults.
type Options struct {
	// Username and Password for HTTP basic authentication.
	// They override credentials in the base URL.
	Username string
	Password string

	// Header is added to every request, for bearer tokens for example.
	Header http.Header

	// Client is used for all requests, nil uses http.DefaultClient.
	Client *http.Client
}

type fileSystem struct {
	fsimpl.PathHelper

	client   *http.Client
	baseURL  url.URL // scheme, host and base path without trailing slash
	username string
	password string
	header   http.Header
	closed   atomic.Bool
}

// New returns a file system for the WebDAV server at baseURL
// (http or https) without registering it. The base path of the
// URL is the root of the file system. ctx is used for a PROPFIND
// request that verifies the server and the credentials.
//
// The URI prefix is "webdav://" + host + base path, so a file
// below "https://cloud.example.com/remote.php/dav/files/alice" is
// "webdav://cloud.example.com/remote.php/dav/files/alice/notes.txt".
func New(ctx context.Context, baseURL string, opts *Options) (fs.FileSystem, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webdavfs: base URL must use http or https: %s", baseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("webdavfs: base URL without host: %s", baseURL)
	}
	f := &fileSystem{
		client: http.DefaultClient,
		baseURL: url.URL{
			Scheme: u.Scheme,
			Host:   u.Host,
			Path:   strings.TrimSuffix(path.Clean("/"+u.Path), "/"),
		},
	}
	if u.User != nil {
		f.username = u.User.Username()
		f.password, _ = u.User.Password()
	}
	if opts != nil {
		if opts.Username != "" {
			f.username, f.password = opts.Username, opts.Password
		}
		if opts.Client != nil {
			f.client = opts.Client
		}
		f.header = opts.Header
	}
	f.PathHelper = fsimpl.PathHelper{URIPrefix: Prefix + u.Host + f.baseURL.Path, Rooted: true}

	info, err := f.propfind(ctx, "/", "0")
	if err != nil {
		return nil, fmt.Errorf("webdavfs: %s: %w", baseURL, err)
	}
	if len(info) == 0 || !info[0].IsDir {
		return nil, fmt.Errorf("webdavfs: %s is not a WebDAV collection", baseURL)
	}
	return f, nil
}

// NewAndRegister returns a registered file system
// for the WebDAV server at baseURL, see New.
func NewAndRegister(ctx context.Context, baseURL string, opts *Options) (fs.FileSystem, error) {
	f, err := New(ctx, baseURL, opts)
	if err != nil {
		return nil, err
	}
	fs.Register(f)
	return f, nil
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

func (f *fileSystem) Name() string {
	return "WebDAV file system"
}

func (f *fileSystem) String() string {
	return f.Name() + " with prefix " + f.URIPrefix
}

func (f *fileSystem) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}

func (f *fileSystem) checkClosed() error {
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// HTTP

// urlOf returns the URL of a file system path,
// with a trailing slash for directories.
func (f *fileSystem) urlOf(filePath string, dir bool) string {
	u := f.baseURL
	u.Path = path.Join(f.baseURL.Path, "/", path.Clean("/"+filePath))
	if dir && u.Path != "/" {
		u.Path += "/"
	}
	return u.String()
}

// request sends a request and returns the response for a 2xx or 207
// status. Other statuses are returned as error, with 404 wrapping
// os.ErrNotExist and 401/403 wrapping os.ErrPermission for filePath.
func (f *fileSystem) request(ctx context.Context, method, filePath string, dir bool, body io.Reader, header map[string]string) (*http.Response, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, f.urlOf(filePath, dir), body)
	if err != nil {
		return nil, err
	}
	maps.Copy(request.Header, f.header)
	for name, value := range header {
		request.Header.Set(name, value)
	}
	if f.username != "" || f.password != "" {
		request.SetBasicAuth(f.username, f.password)
	}
	response, err := f.client.Do(request) //#nosec G704 -- the WebDAV file system intentionally requests user-provided URLs
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	_ = response.Body.Close()
	return nil, f.statusError(method, filePath, response.StatusCode, response.Status)
}

// statusError maps an HTTP error status to the fs error types,
// other statuses are returned as *StatusError.
func (f *fileSystem) statusError(method, filePath string, statusCode int, status string) error {
	switch statusCode {
	case http.StatusNotFound, http.StatusGone:
		return fs.NewErrDoesNotExist(f.file(filePath))
	case http.StatusUnauthorized, http.StatusForbidden:
		return fs.NewErrPermission(f.file(filePath))
	case http.StatusConflict:
		// The parent collection does not exist
		return fs.NewErrDoesNotExist(f.file(path.Dir(filePath)))
	}
	return &StatusError{Method: method, File: f.file(filePath), StatusCode: statusCode, Status: status}
}

// StatusError is returned for HTTP error statuses
// that don't map to an fs error type.
type StatusError struct {
	Method     string
	File       fs.File
	StatusCode int
	Status     string
}

// Error implements the error interface
func (e *StatusError) Error() string {
	return fmt.Sprintf("webdavfs: %s %s: %s", e.Method, e.File, e.Status)
}

// discard closes a response after draining it, so the connection is reused.
func discard(response *http.Response) error {
	_, _ = io.Copy(io.Discard, response.Body)
	return response.Body.Close()
}

///////////////////////////////////////////////////////////////////////////////
// PROPFIND

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`

type multistatus struct {
	Responses []propfindResponse `xml:"DAV: response"`
}

type propfindResponse struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string `xml:"DAV: status"`
	Prop   struct {
		ResourceType struct {
			Collection *struct{} `xml:"DAV: collection"`
		} `xml:"DAV: resourcetype"`
		ContentLength string `xml:"DAV: getcontentlength"`
		LastModified  string `xml:"DAV: getlastmodified"`
	} `xml:"DAV: prop"`
}

// propfind returns the FileInfos of a PROPFIND request with depth "0"
// (the path itself) or "1" (the path and its children). The first
// FileInfo is the path itself.
func (f *fileSystem) propfind(ctx context.Context, filePath string, depth string) ([]*fs.FileInfo, error) {
	response, err := f.request(ctx, "PROPFIND", filePath, depth == "1", strings.NewReader(propfindBody), map[string]string{
		"Depth":        depth,
		"Content-Type": "application/xml; charset=utf-8",
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var ms multistatus
	err = xml.NewDecoder(response.Body).Decode(&ms)
	if err != nil {
		return nil, fmt.Errorf("webdavfs: PROPFIND %s: %w", f.file(filePath), err)
	}
	self := filePath
	var (
		infos    []*fs.FileInfo
		selfSeen bool
	)
	for _, r := range ms.Responses {
		info, infoPath, ok := f.responseInfo(r)
		if !ok {
			continue
		}
		if infoPath == self {
			infos = append([]*fs.FileInfo{info}, infos...)
			selfSeen = true
		} else {
			infos = append(infos, info)
		}
	}
	if !selfSeen {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	return infos, nil
}

// responseInfo converts a PROPFIND response to a FileInfo and its
// file system path, or returns false if the response carries no
// successful properties.
func (f *fileSystem) responseInfo(r propfindResponse) (*fs.FileInfo, string, bool) {
	href := r.Href
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	if unescaped, err := url.PathUnescape(href); err == nil {
		href = unescaped
	}
	filePath := path.Clean("/" + strings.TrimPrefix(href, f.baseURL.Path))
	for _, ps := range r.Propstats {
		if !strings.Contains(ps.Status, " 200 ") {
			continue
		}
		name := path.Base(filePath)
		info := &fs.FileInfo{
			File:        f.file(filePath),
			Name:        name,
			Exists:      true,
			IsHidden:    strings.HasPrefix(name, "."),
			Permissions: DefaultPermissions,
		}
		if filePath == "/" {
			info.File = f.RootDir()
			info.Name = Separator
		}
		if ps.Prop.ResourceType.Collection != nil {
			info.IsDir = true
			info.Permissions = DefaultDirPermissions
		} else {
			info.IsRegular = true
			info.Size, _ = strconv.ParseInt(ps.Prop.ContentLength, 10, 64)
		}
		if modified, err := http.ParseTime(ps.Prop.LastModified); err == nil {
			info.Modified = modified
		}
		return info, filePath, true
	}
	return nil, "", false
}

///////////////////////////////////////////////////////////////////////////////
// Reading

func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	infos, err := f.propfind(context.Background(), filePath, "0")
	if err != nil {
		return nil, err
	}
	return infos[0], nil
}

// ListDir lists the children of a collection with a PROPFIND of depth 1.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	infos, err := f.propfind(ctx, dirPath, "1")
	if err != nil {
		return err
	}
	if !infos[0].IsDir {
		return fs.NewErrIsNotDirectory(infos[0].File)
	}
	for _, info := range infos[1:] {
		if err := ctx.Err(); err != nil {
			return err
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		if err := callback(info); err != nil {
			return err
		}
	}
	return nil
}

// OpenReader returns a reader that streams the file with GET
// and supports Seek by requesting byte ranges.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, fs.NewErrIsDirectory(info.File)
	}
	return &fsimpl.RangeReader{
		Size: info.Size,
		Open: func(offset, count int64) (io.ReadCloser, error) {
			return f.getRange(filePath, offset, count)
		},
	}, nil
}

// getRange returns the body of a GET request for the byte range
// from offset, limited to count bytes if count is not negative.
// If the server ignores the Range header the body is skipped to offset.
func (f *fileSystem) getRange(filePath string, offset, count int64) (io.ReadCloser, error) {
	header := map[string]string{}
	switch {
	case count >= 0:
		header["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+count-1)
	case offset > 0:
		header["Range"] = fmt.Sprintf("bytes=%d-", offset)
	}
	response, err := f.request(context.Background(), http.MethodGet, filePath, false, nil, header)
	if err != nil {
		return nil, err
	}
	if offset > 0 && response.StatusCode != http.StatusPartialContent {
		_, err = io.CopyN(io.Discard, response.Body, offset)
		if err != nil {
			return nil, errors.Join(err, response.Body.Close())
		}
	}
	return response.Body, nil
}

// ReadAll downloads the file with a single GET request.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	response, err := f.request(ctx, http.MethodGet, filePath, false, nil, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return fs.ReadAllContext(ctx, response.Body)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads the data with a PUT request.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	response, err := f.request(ctx, http.MethodPut, filePath, false, bytes.NewReader(data), map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return err
	}
	return discard(response)
}

// OpenWriter returns a writer that buffers the data in memory
// and uploads it with a PUT request on Close.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// MakeDir creates a collection with MKCOL.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if dirPath == "/" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	response, err := f.request(context.Background(), "MKCOL", dirPath, true, nil, nil)
	if err != nil {
		// MKCOL on an existing path is 405 Method Not Allowed
		var statusErr *StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusMethodNotAllowed {
			return err
		}
		if _, statErr := f.Stat(dirPath); statErr == nil {
			return fs.NewErrAlreadyExists(f.file(dirPath))
		}
		return err
	}
	return discard(response)
}

// Remove deletes a file or an empty collection.
func (f *fileSystem) Remove(filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	ctx := context.Background()
	infos, err := f.propfind(ctx, filePath, "1")
	if err != nil {
		return err
	}
	if infos[0].IsDir {
		if filePath == "/" {
			return fmt.Errorf("can't remove root directory of %s", f)
		}
		if len(infos) > 1 {
			return fmt.Errorf("directory not empty: %s", infos[0].File)
		}
	}
	response, err := f.request(ctx, http.MethodDelete, filePath, infos[0].IsDir, nil, nil)
	if err != nil {
		return err
	}
	return discard(response)
}

// RemoveAll deletes a file or a collection with its content.
// A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if filePath == "/" {
		return fmt.Errorf("can't remove root directory of %s", f)
	}
	response, err := f.request(ctx, http.MethodDelete, filePath, false, nil, nil)
	if err != nil {
		return fs.RemoveErrDoesNotExist(err)
	}
	return discard(response)
}

// Move moves or renames a file or collection with the MOVE method,
// overwriting an existing destination.
func (f *fileSystem) Move(filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	if filePath == destPath {
		return nil
	}
	response, err := f.request(context.Background(), "MOVE", filePath, false, nil, map[string]string{
		"Destination": f.urlOf(destPath, false),
		"Overwrite":   "T",
	})
	if err != nil {
		return err
	}
	return discard(response)
}

// CopyFile copies a file server-side with the COPY method,
// overwriting an existing destination.
func (f *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	if srcFile == destFile {
		return nil
	}
	response, err := f.request(ctx, "COPY", srcFile, false, nil, map[string]string{
		"Destination": f.urlOf(destFile, false),
		"Overwrite":   "T",
		"Depth":       "0",
	})
	if err != nil {
		return err
	}
	return discard(response)
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards. Close is idempotent.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	fs.Unregister(f)
	return nil
}
