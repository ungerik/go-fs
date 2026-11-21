// Package onedrivefs implements a go-fs file system for a drive of
// Microsoft OneDrive or SharePoint over the Microsoft Graph API.
//
// One file system covers one drive: the OneDrive of a user (OneDrive
// Personal or OneDrive for Business) or a document library of
// a SharePoint site, which Graph serves with the same driveItem API.
// The file system is identified by the drive id, which New fetches,
// so the same drive always gets the same prefix "onedrive://<drive id>".
//
// The file system adds the access token of an oauth2.TokenSource to the
// requests of the Graph API and never to the pre-authenticated URLs that
// Graph returns for downloads, upload sessions and copies.
//
// OneDrive and SharePoint names are case-insensitive: "/Docs/a.txt" and
// "/docs/A.TXT" are the same file. FileInfos have the names in the case
// the server reports, usually the one they were created with.
//
// Files of up to 4 MiB are uploaded with one request, larger ones with an
// upload session in fragments of 10 MiB. An upload session needs the size
// of the file before the first fragment, so a writer from OpenWriter keeps
// up to 4 MiB in memory and writes a larger file to a temporary file of
// os.TempDir before it uploads it in Close. WriteAll knows the size and
// uploads directly.
package onedrivefs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of OneDrive file system URIs, followed by the drive id
	Prefix = "onedrive://"

	// Separator used in OneDrive file system paths
	Separator = "/"

	// DefaultGraphURL is the base URL of the Microsoft Graph API
	// of the global Microsoft cloud, see Options.GraphURL.
	DefaultGraphURL = "https://graph.microsoft.com/v1.0"

	// DefaultPermissions reported for OneDrive files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for OneDrive folders
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute

	// copyPollDelay is the first wait before the monitor of a server-side
	// copy is asked again, doubled after every answer up to maxCopyPollDelay.
	copyPollDelay    = 100 * time.Millisecond
	maxCopyPollDelay = 2 * time.Second

	// retryDelay is the first wait before a request that was answered
	// with 429 or 503 without a Retry-After header is repeated,
	// doubled after every attempt.
	retryDelay = time.Second
)

// Compile-time interface checks
var (
	_ fs.FileSystem             = new(fileSystem)
	_ fs.WriteFileSystem        = new(fileSystem)
	_ fs.WriteAllFileSystem     = new(fileSystem)
	_ fs.ValidateNameFileSystem = new(fileSystem)
	_ fs.TouchFileSystem        = new(fileSystem)
	_ fs.RemoveAllFileSystem    = new(fileSystem)
	_ fs.CopyFileSystem         = new(fileSystem)
	_ fs.MoveFileSystem         = new(fileSystem)
)

// Options for a OneDrive file system. A nil *Options uses the defaults.
type Options struct {
	// HTTPClient sends all requests, http.DefaultClient if nil.
	//
	// It must not authenticate the requests itself, like the client of an
	// oauth2.Config does: the file system adds the access token only to
	// the requests of the Graph API. The pre-authenticated URLs of upload
	// sessions reject requests with a token, and the URLs of downloads and
	// copy monitors are on other hosts that must not get it.
	// The file system doesn't follow redirects with the client.
	HTTPClient *http.Client

	// GraphURL is the base URL of the Graph API, DefaultGraphURL if empty.
	// The national clouds have their own, like
	// "https://graph.microsoft.us/v1.0" for US Government L4 or
	// "https://microsoftgraph.chinacloudapi.cn/v1.0" for China.
	GraphURL string
}

// fileSystem implements fs.FileSystem for a OneDrive or SharePoint drive.
type fileSystem struct {
	fsimpl.PathHelper

	driveID  string
	driveURL string // Graph API URL of the drive
	graphURL string
	tokens   oauth2.TokenSource
	client   *http.Client

	maxSimple    int           // maxSimpleUpload, smaller in tests
	fragmentSize int           // uploadFragmentSize, smaller in tests
	retryDelay   time.Duration // retryDelay, smaller in tests
	pollDelay    time.Duration // copyPollDelay, smaller in tests

	closed atomic.Bool
}

// New fetches the drive and returns a file system with the prefix
// "onedrive://<drive id>" for it, without registering it.
// ctx is used for the request of the drive.
//
// drive is a drive id or the Graph API path of a drive resource:
//   - "me/drive" for the OneDrive of the signed-in user
//     (delegated access tokens only),
//   - "users/<user id or principal name>/drive" for the OneDrive of a user,
//   - "sites/<site id>/drive" for the default document library of
//     a SharePoint site, "drives/<drive id>" for any library,
//   - "groups/<group id>/drive" for the library of a Microsoft 365 group.
//
// tokens returns the access tokens for the Graph API, for example
// the TokenSource of a golang.org/x/oauth2/clientcredentials Config
// for an app of Microsoft Entra ID, see the README.
func New(ctx context.Context, drive string, tokens oauth2.TokenSource, opts *Options) (fs.FileSystem, error) {
	if tokens == nil {
		return nil, errors.New("onedrivefs: no token source")
	}
	resource := strings.Trim(drive, "/")
	switch {
	case resource == "":
		return nil, fmt.Errorf("%w: onedrivefs: empty drive", os.ErrInvalid)
	case strings.ContainsAny(resource, "?#") || slices.Contains(strings.Split(resource, "/"), ".."):
		return nil, fmt.Errorf("%w: onedrivefs: drive %q is neither a drive id nor a Graph API path of a drive", os.ErrInvalid, drive)
	case !strings.Contains(resource, "/"):
		resource = "drives/" + url.PathEscape(resource)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probe := newFileSystem("", tokens, opts)
	var d struct {
		ID string `json:"id"`
	}
	if _, err := probe.doJSON(ctx, http.MethodGet, probe.graphURL+"/"+resource+"?$select=id", nil, nil, &d); err != nil {
		return nil, fmt.Errorf("onedrivefs: getting the drive %q: %w", drive, err)
	}
	if d.ID == "" {
		return nil, fmt.Errorf("onedrivefs: the drive %q has no id", drive)
	}
	return newFileSystem(d.ID, tokens, opts), nil
}

// NewAndRegister creates a file system for the drive like New and
// registers it, so that fs.File values with its prefix
// "onedrive://<drive id>" work. It is meant for callers that know the
// drive id and work with URIs; fs.GetFileSystem returns the file system
// for the prefix, closing it unregisters it again. Use New and
// fs.Register to get the file system and its prefix.
func NewAndRegister(ctx context.Context, drive string, tokens oauth2.TokenSource, opts *Options) error {
	odfs, err := New(ctx, drive, tokens, opts)
	if err != nil {
		return err
	}
	if err := fs.Register(odfs); err != nil {
		return errors.Join(err, odfs.Close())
	}
	return nil
}

func newFileSystem(driveID string, tokens oauth2.TokenSource, opts *Options) *fileSystem {
	if opts == nil {
		opts = new(Options)
	}
	graphURL := strings.TrimSuffix(opts.GraphURL, "/")
	if graphURL == "" {
		graphURL = DefaultGraphURL
	}
	client := new(http.Client)
	if opts.HTTPClient != nil {
		*client = *opts.HTTPClient
	} else {
		*client = *http.DefaultClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &fileSystem{
		PathHelper:   fsimpl.PathHelper{URIPrefix: Prefix + driveID, Rooted: true},
		driveID:      driveID,
		driveURL:     graphURL + "/drives/" + url.PathEscape(driveID),
		graphURL:     graphURL,
		tokens:       oauth2.ReuseTokenSource(nil, tokens),
		client:       client,
		maxSimple:    maxSimpleUpload,
		fragmentSize: uploadFragmentSize,
		retryDelay:   retryDelay,
		pollDelay:    copyPollDelay,
	}
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

///////////////////////////////////////////////////////////////////////////////
// Metadata

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

// ValidateName implements fs.ValidateNameFileSystem: a name is an error
// wrapping os.ErrInvalid if OneDrive or SharePoint doesn't store it,
// so a copy or move into the drive fails before it writes anything:
//   - a name that is not valid UTF-8, whose bytes the JSON of the API
//     would replace, so the file would get another name,
//   - a name with a control character or one of the characters
//     " * : < > ? \ |,
//   - a name that begins or ends with a space or ends with a dot,
//   - the names .lock and desktop.ini, a name that begins with ~$
//     or contains _vti_, and the device names of Windows like CON, NUL
//     or COM1, also in other cases and with an extension like "nul.txt".
//
// The name "forms" is reserved only in the root folder of a SharePoint
// library, which ValidateName can't tell, so it is accepted.
// Existing files are read with every name.
func (f *fileSystem) ValidateName(name string) error {
	base, _, _ := strings.Cut(name, ".")
	lower := strings.ToLower(name)
	var reason string
	switch {
	case !utf8.ValidString(name):
		reason = "is not valid UTF-8"
	case strings.ContainsFunc(name, func(r rune) bool { return r < ' ' || r == 0x7f || strings.ContainsRune(`"*:<>?\|`, r) }):
		reason = `contains a control character or one of " * : < > ? \ |`
	case strings.HasPrefix(name, " ") || strings.HasSuffix(name, " ") || strings.HasSuffix(name, "."):
		reason = "begins or ends with a space or ends with a dot"
	case lower == ".lock" || lower == "desktop.ini" || strings.HasPrefix(name, "~$") || strings.Contains(lower, "_vti_"):
		reason = "is reserved by OneDrive and SharePoint"
	case slices.Contains(windowsDeviceNames, strings.ToUpper(strings.TrimRight(base, " "))):
		reason = "is a device name of Windows"
	default:
		return nil
	}
	return fmt.Errorf("%w: onedrivefs name %.200q %s", os.ErrInvalid, name, reason)
}

// windowsDeviceNames are the device names of Windows,
// which OneDrive and SharePoint reserve, see ValidateName.
var windowsDeviceNames = []string{
	"CON", "PRN", "AUX", "NUL",
	"COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the drive id.
func (f *fileSystem) ID() string {
	return f.driveID
}

func (f *fileSystem) Name() string {
	return "OneDrive"
}

// String returns the name and the prefix,
// which contains no secret, only the drive id.
func (f *fileSystem) String() string {
	return f.Name() + " " + f.Prefix()
}

func (f *fileSystem) file(filePath string) fs.File {
	return fs.File(f.JoinCleanURI(filePath))
}

// cleanPath returns filePath cleaned and starting with a slash,
// "/" for the root.
func cleanPath(filePath string) string {
	return path.Clean("/" + filePath)
}

// isInside reports whether the clean path p is below the clean
// directory path dir, compared case-insensitively like OneDrive does.
func isInside(dir, p string) bool {
	return dir == "/" || strings.HasPrefix(strings.ToLower(p), strings.ToLower(dir)+"/")
}

// itemURL returns the Graph API URL of the item at the clean path p,
// followed by action, like "/children" or "/content", if not empty.
// Below the root it uses the path-based addressing of Graph:
// /root:/escaped/path:/action
func (f *fileSystem) itemURL(p, action string) string {
	if p == "/" {
		return f.driveURL + "/root" + action
	}
	var b strings.Builder
	b.WriteString(f.driveURL)
	b.WriteString("/root:")
	for name := range strings.SplitSeq(p[1:], "/") {
		b.WriteByte('/')
		// A colon would end the path of the path-based addressing
		b.WriteString(strings.ReplaceAll(url.PathEscape(name), ":", "%3A"))
	}
	if action != "" {
		b.WriteByte(':')
		b.WriteString(action)
	}
	return b.String()
}

func (f *fileSystem) rootInfo() *fs.FileInfo {
	return &fs.FileInfo{
		File:        f.RootDir(),
		Name:        Separator,
		Exists:      true,
		IsDir:       true,
		Permissions: DefaultDirPermissions,
	}
}

// fileInfo returns the FileInfo of item at the clean path p,
// with the name in the case of the item.
// An item that is neither a file nor has children, like a shortcut
// to a folder shared from another drive, is neither IsRegular nor IsDir.
func (f *fileSystem) fileInfo(p string, item *driveItem) *fs.FileInfo {
	if p == "/" {
		return f.rootInfo()
	}
	info := &fs.FileInfo{
		File:     f.file(path.Join(path.Dir(p), item.Name)),
		Name:     item.Name,
		Exists:   true,
		IsHidden: strings.HasPrefix(item.Name, "."),
		Modified: item.modified(),
	}
	switch {
	case item.isDir():
		info.IsDir = true
		info.Permissions = DefaultDirPermissions
	case item.File != nil:
		info.IsRegular = true
		info.Size = item.Size
		info.Permissions = DefaultPermissions
	}
	return info
}

// item returns the driveItem at the clean path p from the server.
// A missing item is an fs.ErrDoesNotExist.
func (f *fileSystem) item(ctx context.Context, p string) (*driveItem, error) {
	var item driveItem
	_, err := f.doJSON(ctx, http.MethodGet, f.itemURL(p, "")+"?$select="+itemFields, nil, nil, &item)
	if err != nil {
		return nil, f.wrapErr(p, err)
	}
	return &item, nil
}

// ancestorErr finds the nearest existing folder above the clean path p
// and returns an fs.ErrIsNotDirectory if a file is in its place, or
// with mustExist an fs.ErrDoesNotExist if the parent folder of p is
// missing. It returns nil otherwise and if a request fails, so that
// callers return the error they are diagnosing.
func (f *fileSystem) ancestorErr(ctx context.Context, p string, mustExist bool) error {
	parent := path.Dir(p)
	if parent == "/" {
		return nil
	}
	for dir := parent; dir != "/"; dir = path.Dir(dir) {
		item, err := f.item(ctx, dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return nil
		case !item.isDir():
			return fs.NewErrIsNotDirectory(f.file(dir))
		case dir == parent:
			return nil
		}
		break
	}
	if mustExist {
		return fs.NewErrDoesNotExist(f.file(parent))
	}
	return nil
}

// Stat returns the FileInfo of filePath from the server.
func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return f.rootInfo(), nil
	}
	item, err := f.item(context.Background(), p)
	if err != nil {
		return nil, err
	}
	return f.fileInfo(p, item), nil
}

// ListDir lists the children of a folder, page by page.
// A file at dirPath is an fs.ErrIsNotDirectory.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(dirPath)
	if p != "/" {
		item, err := f.item(ctx, p)
		if err != nil {
			return err
		}
		if !item.isDir() {
			return fs.NewErrIsNotDirectory(f.file(p))
		}
	}
	next := f.itemURL(p, "/children") + "?$select=" + itemFields
	for next != "" {
		if err := f.check(ctx); err != nil {
			return err
		}
		var page struct {
			Value    []driveItem `json:"value"`
			NextLink string      `json:"@odata.nextLink"`
		}
		if _, err := f.doJSON(ctx, http.MethodGet, next, nil, nil, &page); err != nil {
			return f.wrapErr(p, err)
		}
		for i := range page.Value {
			if err := ctx.Err(); err != nil {
				return err
			}
			item := &page.Value[i]
			match, err := fsimpl.MatchAnyPattern(item.Name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if err := callback(f.fileInfo(path.Join(p, item.Name), item)); err != nil {
				return err
			}
		}
		// The token goes only to the Graph API
		if page.NextLink != "" && !f.isGraphURL(page.NextLink) {
			return fmt.Errorf("onedrivefs: the next page of the listing of %s is not at the Graph API URL %s", f.file(p), f.graphURL)
		}
		next = page.NextLink
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// OpenReader streams the download of the file from the pre-authenticated
// URL that Graph redirects to. A folder is an fs.ErrIsDirectory.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return nil, fs.NewErrIsDirectory(f.RootDir())
	}
	ctx := context.Background()
	resp, err := f.do(ctx, http.MethodGet, f.itemURL(p, "/content"), nil, nil)
	if err != nil {
		// Graph doesn't tell a folder from a missing file
		if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusNotFound {
			if item, statErr := f.item(ctx, p); statErr == nil && item.isDir() {
				return nil, fs.NewErrIsDirectory(f.file(p))
			}
		}
		return nil, f.wrapErr(p, err)
	}
	// Follow the redirects to the download URL
	for redirects := 0; resp.StatusCode >= 300 && resp.StatusCode < 400; redirects++ {
		location, err := resp.Location()
		_ = resp.Body.Close()
		if err != nil {
			// The error of an invalid location would contain it
			return nil, fmt.Errorf("onedrivefs: downloading %s: redirect without a valid location", f.file(p))
		}
		if redirects == 10 {
			return nil, fmt.Errorf("onedrivefs: downloading %s: too many redirects", f.file(p))
		}
		resp, err = f.do(ctx, http.MethodGet, location.String(), nil, nil)
		if err != nil {
			return nil, err
		}
	}
	return resp.Body, nil
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// MakeDir creates a folder. It returns an error wrapping os.ErrExist
// if the path already exists and an fs.ErrDoesNotExist for a missing
// parent folder.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(dirPath)
	if p == "/" {
		return fs.NewErrAlreadyExists(f.RootDir())
	}
	ctx := context.Background()
	body := map[string]any{
		"name":                              path.Base(p),
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}
	_, err := f.doJSON(ctx, http.MethodPost, f.itemURL(path.Dir(p), "/children"), nil, body, nil)
	if err == nil {
		return nil
	}
	if isConflict(err) {
		if _, statErr := f.item(ctx, p); statErr == nil {
			return fs.NewErrAlreadyExists(f.file(p))
		}
	}
	if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusNotFound || s == http.StatusConflict {
		if ancestorErr := f.ancestorErr(ctx, p, true); ancestorErr != nil {
			return ancestorErr
		}
	}
	return f.wrapErr(p, err)
}

// Touch creates an empty file at filePath if it doesn't exist,
// or sets the modification time of an existing file or folder to now.
// The modification time is the one of the fileSystemInfo of the item,
// which FileInfo.Modified reports.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	ctx := context.Background()
	body := map[string]any{
		"fileSystemInfo": map[string]any{"lastModifiedDateTime": time.Now().UTC().Format(time.RFC3339)},
	}
	_, err := f.doJSON(ctx, http.MethodPatch, f.itemURL(p, ""), nil, body, nil)
	if statusOf(err) != http.StatusNotFound {
		return f.wrapErr(p, err)
	}
	resp, err := f.do(ctx, http.MethodPut, f.itemURL(p, "/content")+"?@microsoft.graph.conflictBehavior=fail", octetStream, nil)
	switch {
	case err == nil:
		return resp.Body.Close()
	case isConflict(err):
		if _, statErr := f.item(ctx, p); statErr == nil {
			return nil // Created since the PATCH
		}
	}
	return f.wrapUploadErr(ctx, p, err)
}

// Remove deletes a file or an empty folder.
// It returns an error wrapping os.ErrNotExist for a missing path
// and an fs.ErrDirectoryNotEmpty for a folder with content.
//
// The Graph API deletes folders recursively, so the type and the number
// of children are checked first, and only the checked version (eTag)
// of the item is deleted. A folder could still get content between the
// check and the deletion, if that doesn't change its eTag, which is then
// deleted with it. Deleted items go to the recycle bin of the drive.
func (f *fileSystem) Remove(filePath string) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	ctx := context.Background()
	item, err := f.item(ctx, p)
	if err != nil {
		return err
	}
	// A package like a OneNote notebook has no child count
	if item.isDir() && (item.Folder == nil || item.Folder.ChildCount > 0) {
		return fs.NewErrDirectoryNotEmpty(f.file(p))
	}
	return f.deleteItem(ctx, p, item.ETag)
}

// deleteItem deletes the item at the clean path p, but only in the
// version eTag. A newer version is an *APIError with the status 412.
func (f *fileSystem) deleteItem(ctx context.Context, p, eTag string) error {
	var header http.Header
	if eTag != "" {
		header = http.Header{"If-Match": {eTag}}
	}
	resp, err := f.do(ctx, http.MethodDelete, f.itemURL(p, ""), header, nil)
	if err != nil {
		if statusOf(err) == http.StatusPreconditionFailed {
			return fmt.Errorf("onedrivefs: %s was changed since it was checked: %w", f.file(p), err)
		}
		return f.wrapErr(p, err)
	}
	return resp.Body.Close()
}

// RemoveAll deletes a file or a folder with all its content
// with one request. A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	resp, err := f.do(ctx, http.MethodDelete, f.itemURL(p, ""), nil, nil)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return nil
		}
		return f.wrapErr(p, err)
	}
	return resp.Body.Close()
}

// CopyFile copies a file server-side and waits for the copy to complete.
//
// An existing file at destFile is replaced, an existing folder
// is an fs.ErrIsDirectory. Copying a file onto itself, also with
// a destination that differs only in case, is a no-op.
func (f *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	src, dest := cleanPath(srcFile), cleanPath(destFile)
	if strings.EqualFold(src, dest) {
		return nil
	}
	item, err := f.item(ctx, src)
	if err != nil {
		return err
	}
	if item.isDir() {
		return fs.NewErrIsDirectory(f.file(src))
	}
	if dest == "/" {
		return fs.NewErrIsDirectory(f.RootDir())
	}
	return f.relocate(ctx, false, src, dest)
}

// Move moves or renames a file or folder server-side.
//
// An existing file at destPath is replaced (an fs.ErrIsNotDirectory
// if filePath is a folder), an existing folder is an error wrapping
// os.ErrExist and is neither deleted nor merged.
// Moving a folder into itself is an error wrapping os.ErrInvalid.
// Renames that only change the case of a name are supported.
func (f *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	src, dest := cleanPath(filePath), cleanPath(destPath)
	switch {
	case src == dest:
		return nil
	case dest == "/":
		return fs.NewErrAlreadyExists(f.RootDir())
	case isInside(src, dest):
		// Only a folder can contain the destination
		item, err := f.item(ctx, src)
		if err != nil {
			return err
		}
		if !item.isDir() {
			return fs.NewErrIsNotDirectory(f.file(src))
		}
		return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, f.file(src), f.file(dest))
	case strings.EqualFold(src, dest):
		return f.moveCase(ctx, src, dest)
	}
	return f.relocate(ctx, true, src, dest)
}

// relocate moves or copies the clean path src to dest, which differ
// not only in case, with the overwrite semantics of Move and CopyFile.
//
// The Graph API refuses to move or copy onto an existing name. An
// existing file at dest is therefore deleted before a second attempt,
// if src is not a folder on the server, and only the version (eTag)
// of the file that was checked, so a folder or a newer file that took
// its place in the meantime is never deleted. The deleted file stays
// restorable in the recycle bin of the drive.
func (f *fileSystem) relocate(ctx context.Context, move bool, src, dest string) error {
	parentPath := path.Dir(dest)
	parent, err := f.item(ctx, parentPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if ancestorErr := f.ancestorErr(ctx, dest, true); ancestorErr != nil {
				return ancestorErr
			}
		}
		return err
	}
	if !parent.isDir() {
		return fs.NewErrIsNotDirectory(f.file(parentPath))
	}
	body := map[string]any{
		"parentReference": map[string]any{"driveId": f.driveID, "id": parent.ID},
		"name":            path.Base(dest),
	}
	err = f.relocateOnce(ctx, move, src, body)
	if isConflict(err) {
		destItem, statErr := f.item(ctx, dest)
		if statErr != nil {
			return statErr
		}
		srcItem, statErr := f.item(ctx, src)
		if statErr != nil {
			return statErr
		}
		if destItem.ID == srcItem.ID {
			// The server takes dest for another name of src, like another
			// Unicode normalization of it: rename src, never delete it
			if move {
				return f.moveCase(ctx, src, dest)
			}
			return nil
		}
		switch {
		case destItem.isDir() && move:
			return fs.NewErrAlreadyExists(f.file(dest))
		case destItem.isDir():
			return fs.NewErrIsDirectory(f.file(dest))
		}
		// A folder never replaces a file
		switch {
		case srcItem.isDir() && move:
			return fs.NewErrIsNotDirectory(f.file(dest))
		case srcItem.isDir():
			return fs.NewErrIsDirectory(f.file(src))
		}
		if err := f.deleteItem(ctx, dest, destItem.ETag); err != nil {
			if statusOf(err) == http.StatusPreconditionFailed {
				return errors.Join(fs.NewErrAlreadyExists(f.file(dest)), err)
			}
			return err
		}
		err = f.relocateOnce(ctx, move, src, body)
	}
	switch {
	case err == nil:
		return nil
	case isConflict(err):
		return fs.NewErrAlreadyExists(f.file(dest))
	}
	return f.wrapErr(src, err)
}

// relocateOnce moves the clean path src with a PATCH of its parent and
// name in body, or copies it with body and waits for the copy to complete.
func (f *fileSystem) relocateOnce(ctx context.Context, move bool, src string, body map[string]any) error {
	if move {
		_, err := f.doJSON(ctx, http.MethodPatch, f.itemURL(src, ""), nil, body, nil)
		return err
	}
	header, err := f.doJSON(ctx, http.MethodPost, f.itemURL(src, "/copy"), nil, body, nil)
	if err != nil {
		return err
	}
	monitor := header.Get("Location")
	if monitor == "" {
		return fmt.Errorf("onedrivefs: the copy of %s has no monitor URL to wait for", f.file(src))
	}
	delay := f.pollDelay
	for {
		resp, err := f.do(ctx, http.MethodGet, monitor, nil, nil)
		if err != nil {
			return err
		}
		var status struct {
			Status string `json:"status"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if resp.StatusCode == http.StatusSeeOther {
			// Redirect to the new item
			_ = resp.Body.Close()
			return nil
		}
		err = errors.Join(json.NewDecoder(resp.Body).Decode(&status), resp.Body.Close())
		if err != nil {
			return fmt.Errorf("onedrivefs: reading the copy status of %s: %w", f.file(src), err)
		}
		switch status.Status {
		case "completed":
			return nil
		case "notStarted", "inProgress", "waiting", "updating":
			// Still running
		default:
			// Failed, or a status this version doesn't know
			apiErr := &APIError{
				Method:           http.MethodGet,
				URL:              redactURL(monitor),
				StatusCode:       resp.StatusCode,
				Message:          fmt.Sprintf("copy status %q", status.Status),
				preAuthenticated: true,
			}
			if status.Error != nil {
				apiErr.Code, apiErr.Message = status.Error.Code, status.Error.Message
			}
			return apiErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, maxCopyPollDelay)
	}
}

// moveCase renames the clean path src to dest, which the server takes for
// the same name, like a name that differs only in case.
// If the server refuses that as a name conflict, src is renamed to
// a temporary name in the same folder first. The conflict handling of
// relocate must never run for such a move: it would delete src.
func (f *fileSystem) moveCase(ctx context.Context, src, dest string) error {
	name := path.Base(dest)
	if path.Base(src) == name {
		return nil // Only the case of a parent folder differs
	}
	rename := func(ctx context.Context, from, to string) error {
		_, err := f.doJSON(ctx, http.MethodPatch, f.itemURL(from, ""), nil, map[string]any{"name": path.Base(to)}, nil)
		return err
	}
	err := rename(ctx, src, dest)
	if !isConflict(err) {
		return f.wrapErr(src, err)
	}
	tmp := path.Join(path.Dir(src), ".gofs-rename-"+rand.Text())
	if err := rename(ctx, src, tmp); err != nil {
		return f.wrapErr(src, err)
	}
	if err := rename(ctx, tmp, dest); err != nil {
		// Restore the original name
		return errors.Join(f.wrapErr(src, err), rename(context.WithoutCancel(ctx), tmp, src))
	}
	return nil
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards. Close is idempotent.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil // already closed
	}
	fs.Unregister(f)
	return nil
}
