// Package webdavfs implements a WebDAV client file system
// with the standard library only.
//
// Paths map directly to URL paths below the base URL. Stat and ListDir
// use PROPFIND, reads use GET with Range requests for seeking, writes
// PUT, directories MKCOL, and Move and CopyFile the native MOVE and
// COPY methods. One module covers every WebDAV server: Nextcloud,
// ownCloud, Apache, nginx, SharePoint, or the golang.org/x/net/webdav
// handler.
//
// Servers differ in how strictly they treat the trailing slash of
// collection URLs: nginx answers a DELETE or MKCOL of a collection
// without it with 409 Conflict and a file URL with it with 404, Apache
// redirects a collection URL without it. The file system therefore
// asks for the type of a path before it deletes, moves or copies it
// and sends collection URLs with and file URLs without the slash.
package webdavfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// maxRedirects is the number of redirects followed per request,
// like by the default CheckRedirect of net/http
const maxRedirects = 10

const (
	// Prefix of webdavfs URIs, followed by the user, host
	// and base path of the server, see New
	Prefix = "webdav://"

	// Separator used in webdavfs paths
	Separator = "/"

	// DefaultPermissions reported for files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute
)

// Compile-time interface checks
var (
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
	// A username, from the options or the base URL,
	// is part of the URI prefix, see New.
	Username string
	Password string

	// Header is added to every request, for bearer tokens for example.
	// An Authorization header in Header takes precedence over
	// basic authentication: Username and Password and the credentials
	// of the base URL are not sent then, a username only
	// becomes part of the URI prefix.
	Header http.Header

	// Client is used for all requests, nil uses http.DefaultClient.
	//
	// The file system uses a copy of Client with its own CheckRedirect:
	// it refuses redirects that would change the method, because Go turns
	// PROPFIND, PUT, DELETE, MKCOL, MOVE and COPY into GET when following
	// a 301, 302 or 303, and redirects to the same URL with a trailing slash,
	// which tell that the path is a collection.
	//
	// It also refuses redirects to another host or port than the one of the
	// base URL, and from an https base URL to http, because Go would send
	// the Header entries to any host and basic authentication to the same
	// host name, also over plain HTTP. If Client has a CheckRedirect, that
	// decides about such a redirect instead, with a request without the
	// Authorization header and the Header entries, which it may add back.
	// A refused redirect is a *StatusError. Other redirects are passed
	// to the CheckRedirect of Client, if it has one.
	Client *http.Client
}

type fileSystem struct {
	fsimpl.PathHelper

	client    *http.Client
	baseURL   url.URL // scheme, host and base path without trailing slash
	username  string
	password  string
	basicAuth bool // send username and password, false if header has an Authorization
	header    http.Header
	closed    atomic.Bool
}

// New returns a file system for the WebDAV server at baseURL
// (http or https) without registering it. The base path of the
// URL is the root of the file system. ctx is used for a PROPFIND
// request that verifies the server and the credentials.
//
// The URI prefix is "webdav://" + user + "@" + host + base path, so a file
// below "https://cloud.example.com/remote.php/dav/files/alice" with the
// username alice is
// "webdav://alice@cloud.example.com/remote.php/dav/files/alice/notes.txt".
// The user is part of the prefix because endpoints like Nextcloud's
// "/remote.php/webdav" show every user their own files at the same URL.
// Without a username the prefix is "webdav://" + host + base path.
// The password is never part of the prefix or of error messages.
//
// A '/', '?' or '#' of the username or password in baseURL must be
// percent-encoded, like "%2F" for '/'. An '@' in the path, query or
// fragment of a baseURL without user info whose host has a port must be
// percent-encoded too ("%40"), else baseURL is rejected: url.Parse would
// take a username with a password of digits and an unencoded '/', '?'
// or '#', like "alice:1234/5678@host", for the host and port "alice:1234".
func New(ctx context.Context, baseURL string, opts *Options) (fs.FileSystem, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, invalidBaseURLErr(baseURL)
	}
	// fsimpl.RedactURI changes a URL without user info only
	// if it takes an '@' after the host for the end of a password
	if u.User == nil && fsimpl.RedactURI(baseURL) != baseURL {
		return nil, fmt.Errorf("webdavfs: invalid base URL %s: '@' after the host and port, percent-encode a '/', '?' or '#' of the username or password and an '@' of the path", fsimpl.RedactAddress(baseURL))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("webdavfs: base URL must use http or https: %s", u.Redacted())
	}
	if u.Host == "" {
		return nil, fmt.Errorf("webdavfs: base URL without host: %s", u.Redacted())
	}
	f := &fileSystem{
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
	var client *http.Client
	if opts != nil {
		if opts.Username != "" {
			f.username, f.password = opts.Username, opts.Password
		}
		client = opts.Client
		f.header = opts.Header
	}
	f.client = f.newClient(client)
	f.basicAuth = (f.username != "" || f.password != "") && !hasAuthorization(f.header)
	prefix := Prefix
	if f.username != "" {
		prefix += url.User(f.username).String() + "@"
	}
	f.PathHelper = fsimpl.PathHelper{URIPrefix: prefix + u.Host + f.baseURL.Path, Rooted: true}

	info, err := f.stat(ctx, "/")
	if err != nil {
		return nil, fmt.Errorf("webdavfs: %s: %w", u.Redacted(), err)
	}
	if !info.IsDir {
		return nil, fmt.Errorf("webdavfs: %s is not a WebDAV collection", u.Redacted())
	}
	return f, nil
}

// invalidBaseURLErr returns the error for a base URL that url.Parse rejects.
// The errors of url.Parse repeat parts of the URL, which can be parts
// of the password, so the reason is the error of parsing the URL with
// the redacted password. If that one is valid, the user info is invalid.
// The URL is redacted like an address of the other backends, with
// everything up to the last '@' taken for the user info, because
// the rejected URL can't tell whether an '@' after the host ends it.
func invalidBaseURLErr(baseURL string) error {
	redacted := fsimpl.RedactAddress(baseURL)
	_, reason := url.Parse(redacted)
	if urlErr, ok := errors.AsType[*url.Error](reason); ok {
		reason = urlErr.Err // without the repeated URL
	}
	if reason == nil {
		reason = errors.New("invalid username or password, special characters must be percent-encoded")
	}
	return fmt.Errorf("webdavfs: invalid base URL %s: %w", redacted, reason)
}

// NewAndRegister creates a file system for the WebDAV server at
// baseURL like New and registers it, so that fs.File values with its
// prefix work. fs.GetFileSystem returns it for the prefix,
// closing it unregisters it again.
func NewAndRegister(ctx context.Context, baseURL string, opts *Options) error {
	f, err := New(ctx, baseURL, opts)
	if err != nil {
		return err
	}
	if err := fs.Register(f); err != nil {
		return errors.Join(err, f.Close())
	}
	return nil
}

// newClient returns a copy of client, or of http.DefaultClient
// if client is nil, with the CheckRedirect described at Options.Client.
func (f *fileSystem) newClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	next := client.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		last := via[len(via)-1]
		if req.Method != last.Method || req.URL.Path == last.URL.Path+"/" {
			// The response of last is returned
			// and handled as *StatusError by do
			return http.ErrUseLastResponse
		}
		if !f.keepsCredentials(req.URL) {
			if next == nil {
				return http.ErrUseLastResponse
			}
			// Go copied the headers of the first request to req
			f.removeCredentials(req.Header)
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}
	return &c
}

// keepsCredentials reports whether the credentials may be sent to u:
// it must have the host and port of the base URL, and the scheme https
// if the base URL has it.
func (f *fileSystem) keepsCredentials(u *url.URL) bool {
	if !strings.EqualFold(u.Host, f.baseURL.Host) {
		return false
	}
	return u.Scheme == f.baseURL.Scheme || u.Scheme == "https"
}

// removeCredentials removes the Authorization header
// and the entries of Options.Header from header.
func (f *fileSystem) removeCredentials(header http.Header) {
	header.Del("Authorization")
	for key := range f.header {
		// The keys of Options.Header are copied as they are
		delete(header, key)
		header.Del(key)
	}
}

// hasAuthorization reports whether header has an Authorization header,
// also under a key that is not in canonical form.
func hasAuthorization(header http.Header) bool {
	for key, values := range header {
		if strings.EqualFold(key, "Authorization") && len(values) > 0 {
			return true
		}
	}
	return false
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the prefix without the scheme:
// the user, host and base path of the server.
func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

func (f *fileSystem) Name() string {
	return "WebDAV"
}

// String returns the name and the prefix, which never contains a password.
func (f *fileSystem) String() string {
	return f.Name() + " " + f.URIPrefix
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

// check returns the first error of the closed check and the context.
func (f *fileSystem) check(ctx context.Context) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	return ctx.Err()
}

// cleanPath returns filePath as rooted clean path.
func cleanPath(filePath string) string {
	return path.Clean("/" + filePath)
}

// isInside reports whether the clean path p is
// below the clean directory path dir.
func isInside(dir, p string) bool {
	return dir == "/" || strings.HasPrefix(p, dir+"/")
}

///////////////////////////////////////////////////////////////////////////////
// HTTP

// urlOf returns the URL of a file system path,
// with a trailing slash for directories and the root.
func (f *fileSystem) urlOf(filePath string, dir bool) string {
	u := f.baseURL
	clean := cleanPath(filePath)
	u.Path = path.Join(f.baseURL.Path, clean)
	if (dir || clean == "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String()
}

// do sends a request for the URL of filePath, a collection URL if dir is true,
// and returns the response for a 2xx status. A 207 Multi-Status is only
// a success for PROPFIND, for DELETE, MOVE and COPY it reports members
// that failed. For every other status the response is closed and a
// *StatusError returned, see fsError for its mapping to the fs errors.
func (f *fileSystem) do(ctx context.Context, method, filePath string, dir bool, body []byte, header map[string]string) (*http.Response, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, f.urlOf(filePath, dir), bodyReader)
	if err != nil {
		return nil, err
	}
	maps.Copy(request.Header, f.header)
	for name, value := range header {
		request.Header.Set(name, value)
	}
	if f.basicAuth {
		request.SetBasicAuth(f.username, f.password)
	}
	response, err := f.client.Do(request) //#nosec G704 -- the WebDAV file system intentionally requests user-provided URLs
	if err != nil {
		return nil, err
	}
	code := response.StatusCode
	if code >= 200 && code < 300 && (code != http.StatusMultiStatus || method == "PROPFIND") {
		return response, nil
	}
	statusErr := &StatusError{
		Method:     method,
		File:       f.file(filePath),
		StatusCode: code,
		Status:     response.Status,
		location:   response.Header.Get("Location"),
	}
	if code >= 300 && code < 400 && statusErr.location != "" {
		if loc, err := request.URL.Parse(statusErr.location); err == nil {
			statusErr.slashRedirect = loc.Host == request.URL.Host && loc.Path == request.URL.Path+"/"
		}
	}
	_ = discard(response)
	return nil, statusErr
}

// fsError maps a *StatusError for filePath to the fs error types:
// 404 and 410 to ErrDoesNotExist, 401 and 403 to ErrPermission.
// Other errors are returned unchanged.
//
// A 409 Conflict is not mapped, because it means a missing parent
// collection only for PUT, MKCOL, MOVE and COPY, and those methods
// map it themselves. nginx for example answers a DELETE of a
// collection URL without trailing slash with 409.
func (f *fileSystem) fsError(err error, filePath string) error {
	switch statusCode(err) {
	case http.StatusNotFound, http.StatusGone:
		return fs.NewErrDoesNotExist(f.file(filePath))
	case http.StatusUnauthorized, http.StatusForbidden:
		return fs.NewErrPermission(f.file(filePath))
	}
	return err
}

// statusCode returns the status code of a *StatusError err, else 0.
func statusCode(err error) int {
	if statusErr, ok := errors.AsType[*StatusError](err); ok {
		return statusErr.StatusCode
	}
	return 0
}

// isSlashRedirect reports whether err is a *StatusError
// for a redirect to the URL with a trailing slash appended,
// the answer of servers like Apache for a collection URL without it.
func isSlashRedirect(err error) bool {
	statusErr, ok := errors.AsType[*StatusError](err)
	return ok && statusErr.slashRedirect
}

// StatusError is returned for HTTP error statuses
// that don't map to an fs error type,
// and for redirects that the file system doesn't follow.
type StatusError struct {
	Method     string
	File       fs.File
	StatusCode int
	Status     string

	location      string // Location header of a redirect
	slashRedirect bool   // redirect to the URL with a trailing slash appended
}

// Error implements the error interface
func (e *StatusError) Error() string {
	if e.location != "" {
		return fmt.Sprintf("webdavfs: %s %s: %s, redirect to %s not followed", e.Method, e.File, e.Status, e.location)
	}
	return fmt.Sprintf("webdavfs: %s %s: %s", e.Method, e.File, e.Status)
}

// discard closes a response after draining it, so the connection is reused.
func discard(response *http.Response) error {
	_, _ = io.Copy(io.Discard, response.Body)
	return response.Body.Close()
}

///////////////////////////////////////////////////////////////////////////////
// PROPFIND

var propfindBody = []byte(`<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`)

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
// FileInfo is the path itself. The request goes to the collection URL
// if dir is true, else to the file URL, and is repeated for the
// collection URL if the server redirects to it.
func (f *fileSystem) propfind(ctx context.Context, filePath string, depth string, dir bool) ([]*fs.FileInfo, error) {
	filePath = cleanPath(filePath)
	header := map[string]string{
		"Depth":        depth,
		"Content-Type": "application/xml; charset=utf-8",
	}
	response, err := f.do(ctx, "PROPFIND", filePath, dir, propfindBody, header)
	if !dir && isSlashRedirect(err) {
		response, err = f.do(ctx, "PROPFIND", filePath, true, propfindBody, header)
	}
	if err != nil {
		return nil, f.fsError(err, filePath)
	}
	defer response.Body.Close()

	var ms multistatus
	err = xml.NewDecoder(response.Body).Decode(&ms)
	if err != nil {
		return nil, fmt.Errorf("webdavfs: PROPFIND %s: %w", f.file(filePath), err)
	}
	var (
		infos    []*fs.FileInfo
		selfSeen bool
	)
	for _, r := range ms.Responses {
		info, infoPath, ok := f.responseInfo(r)
		if !ok {
			continue
		}
		if infoPath == filePath && !selfSeen {
			infos = append([]*fs.FileInfo{info}, infos...)
			selfSeen = true
		} else {
			infos = append(infos, info)
		}
	}
	if !selfSeen && depth == "0" && len(infos) == 1 {
		// The only response of depth 0 is the path itself,
		// even if the server wrote its href in another form
		info := *infos[0]
		info.File = f.file(filePath)
		info.Name = path.Base(filePath)
		if filePath == "/" {
			info.File = f.RootDir()
			info.Name = Separator
		}
		return []*fs.FileInfo{&info}, nil
	}
	if !selfSeen {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	return infos, nil
}

// stat returns the FileInfo of filePath with a PROPFIND of depth 0
// to the file URL, which the server may redirect to the collection URL.
func (f *fileSystem) stat(ctx context.Context, filePath string) (*fs.FileInfo, error) {
	infos, err := f.propfind(ctx, filePath, "0", false)
	if err != nil {
		return nil, err
	}
	return infos[0], nil
}

// responseInfo converts a PROPFIND response to a FileInfo and its
// file system path, or returns false if the response carries no
// successful properties.
func (f *fileSystem) responseInfo(r propfindResponse) (*fs.FileInfo, string, bool) {
	// The href is URL escaped, url.Parse decodes its path exactly once,
	// so a file named "100%25" is not mistaken for "100%"
	href := r.Href
	if u, err := url.Parse(href); err == nil {
		href = u.Path
	}
	filePath := cleanPath(strings.TrimPrefix(href, f.baseURL.Path))
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
	return f.stat(context.Background(), filePath)
}

// ListDir lists the children of a collection with a PROPFIND of depth 1.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	infos, err := f.propfind(ctx, dirPath, "1", true)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, fs.ErrFileSystemClosed) {
			return err
		}
		// Servers like nginx answer the collection URL
		// of a file with 404, a Stat tells if it is one
		info, statErr := f.stat(ctx, dirPath)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
		}
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
	ctx := context.Background()
	header := map[string]string{}
	switch {
	case count >= 0:
		header["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+count-1)
	case offset > 0:
		header["Range"] = fmt.Sprintf("bytes=%d-", offset)
	}
	response, err := f.do(ctx, http.MethodGet, filePath, false, nil, header)
	if err != nil {
		return nil, f.readError(ctx, err, filePath)
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
	response, err := f.do(ctx, http.MethodGet, filePath, false, nil, nil)
	if err != nil {
		return nil, f.readError(ctx, err, filePath)
	}
	defer response.Body.Close()
	return readAll(ctx, response.Body, response.ContentLength)
}

// maxPreallocate limits the buffer that readAll allocates for the
// announced size of a body, so that a wrong Content-Length doesn't
// allocate much more memory than the body needs.
const maxPreallocate = 16 << 20

// readAll reads r until EOF, another error or the end of ctx like
// fs.ReadAll. If size is not negative, the buffer is allocated for size
// bytes, up to maxPreallocate, instead of growing it while reading.
func readAll(ctx context.Context, r io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return fs.ReadAll(ctx, r)
	}
	// One byte more, so that the read that returns io.EOF
	// doesn't grow the buffer
	b := make([]byte, 0, min(size, maxPreallocate)+1)
	for ctx.Err() == nil {
		if len(b) == cap(b) {
			// Add more capacity (let append pick how much)
			b = append(b, 0)[:len(b)]
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			return b, err
		}
	}
	return b, ctx.Err()
}

// readError maps an error of a GET request for filePath. Servers answer
// a GET of a collection with a redirect to the collection URL (Apache,
// nginx) or an error status like 405 (golang.org/x/net/webdav), which
// become an fs.ErrIsDirectory. The redirect is not followed, because
// its target would be an HTML index of the collection.
func (f *fileSystem) readError(ctx context.Context, err error, filePath string) error {
	if isSlashRedirect(err) {
		return fs.NewErrIsDirectory(f.file(filePath))
	}
	switch statusCode(err) {
	case 0, http.StatusNotFound, http.StatusGone, http.StatusUnauthorized:
	default:
		if info, statErr := f.stat(ctx, filePath); statErr == nil && info.IsDir {
			return fs.NewErrIsDirectory(info.File)
		}
	}
	return f.fsError(err, filePath)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads the data with a PUT request.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if data == nil {
		data = []byte{} // PUT with a Content-Length of zero
	}
	response, err := f.do(ctx, http.MethodPut, filePath, false, data, map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return f.writeError(ctx, err, filePath)
	}
	return discard(response)
}

// writeError maps an error of a PUT request for filePath.
// Servers answer a PUT to a collection with an error status like 405
// or 409, or with a redirect to the collection URL, which become an
// fs.ErrIsDirectory. Else 409 means that the parent collection is missing.
func (f *fileSystem) writeError(ctx context.Context, err error, filePath string) error {
	if isSlashRedirect(err) {
		return fs.NewErrIsDirectory(f.file(filePath))
	}
	code := statusCode(err)
	if code == 0 || code == http.StatusUnauthorized {
		return f.fsError(err, filePath)
	}
	if info, statErr := f.stat(ctx, filePath); statErr == nil && info.IsDir {
		return fs.NewErrIsDirectory(info.File)
	}
	if code == http.StatusConflict {
		return fs.NewErrDoesNotExist(f.file(path.Dir(cleanPath(filePath))))
	}
	return f.fsError(err, filePath)
}

// OpenWriter returns a writer that buffers the data in memory
// and uploads it with a PUT request on Close.
//
// The writer implements fs.WriteAborter, Abort discards the data.
// It doesn't stream the upload, because servers write the body of
// an interrupted PUT as file, which would replace an existing file
// with partial content.
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
	if err := f.checkClosed(); err != nil {
		return err
	}
	dirPath = cleanPath(dirPath)
	if dirPath == "/" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	ctx := context.Background()
	response, err := f.do(ctx, "MKCOL", dirPath, true, nil, nil)
	if err != nil {
		code := statusCode(err)
		if code == 0 || code == http.StatusUnauthorized {
			return f.fsError(err, dirPath)
		}
		// An existing path is 405 Method Not Allowed, but servers
		// like nginx answer the collection URL of a file with 404 or 409
		if _, statErr := f.stat(ctx, dirPath); statErr == nil {
			return fs.NewErrAlreadyExists(f.file(dirPath))
		}
		if code == http.StatusConflict {
			return fs.NewErrDoesNotExist(f.file(path.Dir(dirPath)))
		}
		return f.fsError(err, dirPath)
	}
	return discard(response)
}

// Remove deletes a file or an empty collection.
// It returns an fs.ErrDirectoryNotEmpty for a collection with members
// and an error wrapping os.ErrInvalid for the root directory.
func (f *fileSystem) Remove(filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	ctx := context.Background()
	filePath = cleanPath(filePath)
	if filePath == "/" {
		if err := f.check(ctx); err != nil {
			return err
		}
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	info, err := f.stat(ctx, filePath)
	if err != nil {
		return err
	}
	if info.IsDir {
		infos, err := f.propfind(ctx, filePath, "1", true)
		if err != nil {
			return err
		}
		if len(infos) > 1 {
			return fs.NewErrDirectoryNotEmpty(info.File)
		}
	}
	response, err := f.do(ctx, http.MethodDelete, filePath, info.IsDir, nil, nil)
	if err != nil {
		return f.fsError(err, filePath)
	}
	return discard(response)
}

// RemoveAll deletes a file or a collection with its content.
// A missing path is not an error. The root directory can't be removed,
// that is an error wrapping os.ErrInvalid.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	filePath = cleanPath(filePath)
	if filePath == "/" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	info, err := f.stat(ctx, filePath)
	if err != nil {
		return fs.RemoveErrDoesNotExist(err)
	}
	response, err := f.do(ctx, http.MethodDelete, filePath, info.IsDir, nil, nil)
	if err != nil {
		return fs.RemoveErrDoesNotExist(f.fsError(err, filePath))
	}
	return discard(response)
}

// Move moves or renames a file or collection with the MOVE method.
//
// An existing file at destPath is replaced (an fs.ErrIsNotDirectory
// if filePath is a collection), an existing collection is an error
// wrapping os.ErrExist and is neither deleted nor merged.
// Moving a collection into itself is an error wrapping os.ErrInvalid.
//
// A destPath that only differs in case from filePath renames it on a
// case-insensitive server. An existing destination with such a path, or
// an existing file with the same size and modification time, may be the
// source itself, so filePath is moved to a temporary name ".gofs-rename-*"
// in its collection and from there onto destPath, and moved back if that
// fails. An error that leaves it at the temporary name contains that name.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	src, dest := cleanPath(filePath), cleanPath(destPath)
	if src == dest {
		return nil
	}
	srcInfo, err := f.stat(ctx, src)
	if err != nil {
		return err
	}
	if srcInfo.IsDir && isInside(src, dest) {
		return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, srcInfo.File, f.file(dest))
	}
	return f.relocate(ctx, "MOVE", srcInfo, src, dest)
}

// CopyFile copies a file server-side with the COPY method.
// An existing file at destFile is replaced,
// an existing collection is an fs.ErrIsDirectory.
//
// An existing destFile that may be srcFile itself under another name,
// because it only differs in case or has the same size and modification
// time, is checked by moving srcFile to a temporary name ".gofs-rename-*"
// and back. A copy onto the file itself, like on a case-insensitive
// server, does nothing.
func (f *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	if err := f.check(ctx); err != nil {
		return err
	}
	src, dest := cleanPath(srcFile), cleanPath(destFile)
	if src == dest {
		return nil
	}
	srcInfo, err := f.stat(ctx, src)
	if err != nil {
		return err
	}
	if srcInfo.IsDir {
		return fs.NewErrIsDirectory(srcInfo.File)
	}
	return f.relocate(ctx, "COPY", srcInfo, src, dest)
}

// relocate sends a MOVE or COPY request for src with Overwrite F,
// so that the server never deletes an existing destination.
// If the destination exists (412 Precondition Failed), an existing
// file is replaced by a second request with Overwrite T,
// an existing collection is an error.
//
// A destination that may be the source itself under another name, see
// maybeSource, is never replaced like that, because the server would
// delete the source: a MOVE renames the source through a temporary name,
// and a COPY onto the source itself does nothing.
func (f *fileSystem) relocate(ctx context.Context, method string, srcInfo *fs.FileInfo, src, dest string) error {
	err := f.send(ctx, method, src, dest, srcInfo.IsDir, false)
	if statusCode(err) == http.StatusPreconditionFailed {
		destInfo, statErr := f.stat(ctx, dest)
		if statErr != nil {
			return statErr
		}
		maybeSource := maybeSource(src, dest, srcInfo, destInfo)
		switch {
		case maybeSource && method == "MOVE":
			return f.moveThroughTemp(ctx, srcInfo, src, dest)
		case destInfo.IsDir && method == "COPY":
			return fs.NewErrIsDirectory(destInfo.File)
		case destInfo.IsDir:
			return fs.NewErrAlreadyExists(destInfo.File)
		case srcInfo.IsDir:
			return fs.NewErrIsNotDirectory(destInfo.File)
		case maybeSource:
			isSource, err := f.isSource(ctx, src, dest)
			if err != nil || isSource {
				return err
			}
		}
		err = f.send(ctx, method, src, dest, srcInfo.IsDir, true)
	}
	return f.relocateError(ctx, err, src, dest)
}

// send sends a MOVE or COPY request for src to dest with the Overwrite
// header T or F, for a collection with both URLs as collection URLs.
func (f *fileSystem) send(ctx context.Context, method, src, dest string, dir, overwrite bool) error {
	header := map[string]string{
		// nginx wants both URLs of a collection with trailing slash
		"Destination": f.urlOf(dest, dir),
		"Overwrite":   "F",
	}
	if overwrite {
		header["Overwrite"] = "T"
	}
	if method == "COPY" {
		header["Depth"] = "0"
	}
	response, err := f.do(ctx, method, src, dir, nil, header)
	if err != nil {
		return err
	}
	return discard(response)
}

// relocateError maps an error of a MOVE or COPY request for src to dest.
func (f *fileSystem) relocateError(ctx context.Context, err error, src, dest string) error {
	switch statusCode(err) {
	case http.StatusConflict:
		// The parent collection of the destination is missing
		return fs.NewErrDoesNotExist(f.file(path.Dir(dest)))
	case http.StatusForbidden:
		// golang.org/x/net/webdav answers a MOVE
		// into a missing collection with 403
		if _, statErr := f.stat(ctx, path.Dir(dest)); errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return f.fsError(err, src)
}

// maybeSource reports whether the existing destination of a MOVE or COPY
// may be the source itself under another name: if the paths only differ
// in case, because the server may be case-insensitive, or if both are
// files with the same size and modification time, because the server may
// normalize names, like their Unicode form. A false positive costs a few
// requests, a false negative would make the server delete the source.
func maybeSource(src, dest string, srcInfo, destInfo *fs.FileInfo) bool {
	switch {
	case srcInfo.IsDir != destInfo.IsDir:
		return false
	case strings.EqualFold(src, dest):
		return true
	}
	return !srcInfo.IsDir && srcInfo.Size == destInfo.Size && srcInfo.Modified.Equal(destInfo.Modified)
}

// tempPath returns a new temporary path in the collection of p.
func tempPath(p string) string {
	return path.Join(path.Dir(p), ".gofs-rename-"+rand.Text())
}

// moveThroughTemp moves src onto the existing dest, which may be src
// itself, through a temporary name in the collection of src. Once src is
// at the temporary name, an existing dest is another resource and is
// replaced like by relocate. If that fails, src is moved back.
func (f *fileSystem) moveThroughTemp(ctx context.Context, srcInfo *fs.FileInfo, src, dest string) error {
	tmp := tempPath(src)
	if err := f.send(ctx, "MOVE", src, tmp, srcInfo.IsDir, false); err != nil {
		return f.relocateError(ctx, err, src, tmp)
	}
	err := f.send(ctx, "MOVE", tmp, dest, srcInfo.IsDir, false)
	if statusCode(err) == http.StatusPreconditionFailed {
		var destInfo *fs.FileInfo
		destInfo, err = f.stat(ctx, dest)
		switch {
		case err != nil:
		case destInfo.IsDir:
			err = fs.NewErrAlreadyExists(destInfo.File)
		case srcInfo.IsDir:
			err = fs.NewErrIsNotDirectory(destInfo.File)
		default:
			err = f.send(ctx, "MOVE", tmp, dest, srcInfo.IsDir, true)
		}
	}
	if err == nil {
		return nil
	}
	err = f.relocateError(ctx, err, src, dest)
	// Also if the context is done
	if restoreErr := f.send(context.WithoutCancel(ctx), "MOVE", tmp, src, srcInfo.IsDir, false); restoreErr != nil {
		return fmt.Errorf("%w; %s remains at %s: %w", err, f.file(src), f.file(tmp), restoreErr)
	}
	return err
}

// isSource reports whether the existing file dest is the file src under
// another name. It moves src to a temporary name in its collection and
// back: dest is src if it was gone meanwhile.
func (f *fileSystem) isSource(ctx context.Context, src, dest string) (bool, error) {
	tmp := tempPath(src)
	if err := f.send(ctx, "MOVE", src, tmp, false, false); err != nil {
		return false, f.relocateError(ctx, err, src, tmp)
	}
	_, statErr := f.stat(ctx, dest)
	// Also if the context is done
	if err := f.send(context.WithoutCancel(ctx), "MOVE", tmp, src, false, false); err != nil {
		return false, fmt.Errorf("webdavfs: %s remains at %s: %w", f.file(src), f.file(tmp), err)
	}
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		return true, nil
	case statErr != nil:
		return false, statErr
	}
	return false, nil
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
