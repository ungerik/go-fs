// Package azureblobfs implements a go-fs file system for an Azure Blob
// Storage container using the Azure SDK for Go.
//
// Blob storage has no directories: a blob named "docs/readme.txt" is a
// file whose "directory" docs exists implicitly. MakeDir creates a
// zero-byte marker blob with a trailing slash ("docs/") so that empty
// directories exist too, like s3fs does for S3.
//
// Blob names map to rooted paths without the leading slash: the path
// "/docs/readme.txt" is the blob "docs/readme.txt". Blobs that no path
// maps to, because their name starts with a slash or contains an empty
// element like "docs//readme.txt", are skipped by the listings.
package azureblobfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of azureblobfs URIs, followed by the host and container of the container URL
	Prefix = "azblob://"

	// Separator used in azureblobfs paths
	Separator = "/"

	// DefaultPermissions reported for blobs
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute

	// uploadBlockSize and uploadConcurrency configure the streaming
	// uploads of OpenWriter: blocks of 4 MiB allow blobs of up to
	// 195 GiB with the 50000 blocks of a block blob, at most
	// uploadConcurrency blocks are buffered and staged in parallel.
	// WriteAll stages at most uploadConcurrency blocks in parallel too.
	uploadBlockSize   = 4 << 20
	uploadConcurrency = 4
)

// Compile-time interface checks
var (
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ReadAllFileSystem          = new(fileSystem)
	_ fs.WriteAllFileSystem         = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.TouchFileSystem            = new(fileSystem)
	_ fs.CopyFileSystem             = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
)

// Options of New, NewAndRegister and NewFromConnectionString.
// A nil Options uses the zero value.
type Options struct {
	// ReadOnly makes every write operation return fs.ErrReadOnlyFileSystem.
	ReadOnly bool

	// SkipContainerCheck skips the request of New that verifies that the
	// container exists and can be listed. Use it for credentials without
	// the list permission, like a SAS token that only allows uploads.
	SkipContainerCheck bool
}

type fileSystem struct {
	fsimpl.PathHelper

	client   *container.Client
	readOnly bool
	closed   atomic.Bool
}

// New returns a file system for the container of the client without
// registering it, use fs.Register or NewAndRegister for that.
// Files of the file system are only usable via the File type
// while it is registered, because URIs are resolved with the registry.
//
// Unless Options.SkipContainerCheck is set, New lists one blob of the
// container with ctx to verify that it exists and that the credentials
// allow listing it. That works with account keys, Entra ID roles and
// SAS tokens with the list permission, also container scoped ones.
//
// The URI prefix is "azblob://" + host + container path of the container
// URL, for example "azblob://myaccount.blob.core.windows.net/mycontainer".
// The query of the URL, like a SAS token, is not part of the prefix and
// is removed from the error messages of the file system.
func New(ctx context.Context, client *container.Client, opts *Options) (fs.FileSystem, error) {
	if client == nil {
		return nil, errors.New("azureblobfs: nil container client")
	}
	if opts == nil {
		opts = &Options{}
	}
	u, err := url.Parse(client.URL())
	if err != nil {
		return nil, fmt.Errorf("azureblobfs: invalid container URL: %w", redactErr(err))
	}
	containerPath := u.Host + strings.TrimSuffix(u.Path, Separator)
	if !opts.SkipContainerCheck {
		pager := client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{MaxResults: new(int32(1))})
		if _, err := pager.NextPage(ctx); err != nil {
			return nil, fmt.Errorf("azureblobfs: container %s://%s: %w", u.Scheme, containerPath, redactErr(err))
		}
	}
	return &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + containerPath, Rooted: true},
		client:     client,
		readOnly:   opts.ReadOnly,
	}, nil
}

// NewAndRegister creates a file system for the container of the client
// with New and registers it. It returns an error wrapping
// fs.ErrAlreadyRegistered if another file system is registered for
// the container. Close the file system to unregister it,
// for example with fs.GetFileSystemByPrefixOrNil(prefix).Close().
func NewAndRegister(ctx context.Context, client *container.Client, opts *Options) error {
	f, err := New(ctx, client, opts)
	if err != nil {
		return err
	}
	return fs.Register(f)
}

// NewFromConnectionString creates a file system for a container like New,
// using an Azure Storage connection string. The file system is not
// registered, see fs.Register.
func NewFromConnectionString(ctx context.Context, connectionString, containerName string, opts *Options) (fs.FileSystem, error) {
	client, err := container.NewClientFromConnectionString(connectionString, containerName, nil)
	if err != nil {
		return nil, err
	}
	return New(ctx, client, opts)
}

// ReadableWritable returns true and the negated Options.ReadOnly.
func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !f.readOnly
}

// RootDir returns the prefix followed by a slash as File.
func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

// ID returns the host and the container path of the container URL.
func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

// Name returns "Azure Blob Storage".
func (f *fileSystem) Name() string {
	return "Azure Blob Storage"
}

// String returns the name and the prefix of the file system.
// The prefix never contains credentials.
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

func (f *fileSystem) checkWritable() error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if f.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Names and errors

// blobName returns the blob name of a file system path,
// the empty string for the root.
func blobName(filePath string) string {
	return strings.Trim(filePath, Separator)
}

// dirPrefix returns the blob name prefix of a directory path:
// the name with a trailing slash, or the empty string for the root.
func dirPrefix(dirPath string) string {
	name := blobName(dirPath)
	if name == "" {
		return ""
	}
	return name + Separator
}

// isPathName reports if a blob name relative to a listed directory
// is the name of a path: not empty and without empty elements,
// so it doesn't start or end with a slash or contain "//".
// The paths of the other names would be cleaned to different names,
// a blob "/x" for example would list the root as its own child.
func isPathName(relName string) bool {
	return relName != "" &&
		!strings.HasPrefix(relName, Separator) &&
		!strings.HasSuffix(relName, Separator) &&
		!strings.Contains(relName, Separator+Separator)
}

func isNotFound(err error) bool {
	return bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound, bloberror.ResourceNotFound)
}

// isPermissionDenied reports whether err is a 403 Forbidden response.
func isPermissionDenied(err error) bool {
	if respErr, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return respErr.StatusCode == http.StatusForbidden
	}
	return false
}

var (
	// urlQuery matches URLs with a query in error messages,
	// the query can carry the signature of a SAS token.
	urlQuery = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'?#]*)\?[^\s"'#]*`)

	// sasSignature matches a SAS signature parameter outside of a URL.
	sasSignature = regexp.MustCompile(`(?i)\bsig=[^&\s"']*`)
)

// redactedError has the message of err without SAS signatures.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactErr returns err with the queries of URLs and SAS signatures
// removed from its message, so that a SAS token of the container URL
// doesn't leak into logs. Transport errors of net/http, for example,
// contain the full request URL. errors.Is and errors.As still see err.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := sasSignature.ReplaceAllString(urlQuery.ReplaceAllString(msg, "$1"), "sig=REDACTED")
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, err: err}
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func (f *fileSystem) fileInfo(name string, size int64, modified time.Time) *fs.FileInfo {
	base := path.Base(name)
	return &fs.FileInfo{
		File:        f.file(name),
		Name:        base,
		Exists:      true,
		IsRegular:   true,
		IsHidden:    strings.HasPrefix(base, "."),
		Size:        size,
		Modified:    modified,
		Permissions: DefaultPermissions,
	}
}

func (f *fileSystem) dirInfo(name string, modified time.Time) *fs.FileInfo {
	if name == "" {
		return &fs.FileInfo{
			File:        f.RootDir(),
			Name:        Separator,
			Exists:      true,
			IsDir:       true,
			Permissions: DefaultDirPermissions,
		}
	}
	base := path.Base(name)
	return &fs.FileInfo{
		File:        f.file(name),
		Name:        base,
		Exists:      true,
		IsDir:       true,
		IsHidden:    strings.HasPrefix(base, "."),
		Modified:    modified,
		Permissions: DefaultDirPermissions,
	}
}

// notExistError returns an error wrapping os.ErrNotExist for a file path,
// or fs.ErrIsDirectory if the path is a directory.
func (f *fileSystem) notExistError(filePath string) error {
	info, err := f.Stat(filePath)
	if err == nil && info.IsDir {
		return fs.NewErrIsDirectory(info.File)
	}
	return fs.NewErrDoesNotExist(f.file(filePath))
}

// dirExists reports if name is a directory: a marker blob "name/" exists
// or a blob below it (implicit directory), checked with a listing
// limited to one blob. modified is the modification time of the marker
// or zero for an implicit directory. The root with the empty name
// always exists.
func (f *fileSystem) dirExists(ctx context.Context, name string) (exists bool, modified time.Time, err error) {
	if name == "" {
		return true, time.Time{}, nil
	}
	item, err := firstBlob(ctx, f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix:     new(name + Separator),
		MaxResults: new(int32(1)),
	}))
	if err != nil || item == nil {
		return false, time.Time{}, err
	}
	if deref(item.Name) == name+Separator && item.Properties != nil {
		modified = deref(item.Properties.LastModified)
	}
	return true, modified, nil
}

// firstBlob returns the first blob that pager lists, or nil.
// Azure can return an empty page with a continuation marker,
// so it reads pages until one has a blob or there are no more.
func firstBlob(ctx context.Context, pager *runtime.Pager[container.ListBlobsFlatResponse]) (*container.BlobItem, error) {
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, redactErr(err)
		}
		if len(page.Segment.BlobItems) > 0 {
			return page.Segment.BlobItems[0], nil
		}
	}
	return nil, nil
}

// checkNotDir returns an fs.ErrIsDirectory error if name is a directory,
// so that writing a blob name doesn't turn a directory into a file.
// Credentials without the list permission, like an upload-only SAS,
// can't see directories, for them the check passes.
func (f *fileSystem) checkNotDir(ctx context.Context, name string) error {
	isDir, _, err := f.dirExists(ctx, name)
	if isPermissionDenied(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if isDir {
		return fs.NewErrIsDirectory(f.dirInfo(name, time.Time{}).File)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// Stat returns the FileInfo of a blob, or of a directory that exists
// as marker blob or implicitly through blobs below it.
func (f *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	name := blobName(filePath)
	if name == "" {
		return f.dirInfo("", time.Time{}), nil
	}
	ctx := context.Background()
	props, err := f.client.NewBlobClient(name).GetProperties(ctx, nil)
	if err == nil {
		return f.fileInfo(name, deref(props.ContentLength), deref(props.LastModified)), nil
	}
	if !isNotFound(err) {
		return nil, redactErr(err)
	}
	isDir, modified, err := f.dirExists(ctx, name)
	if err != nil {
		return nil, err
	}
	if !isDir {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	return f.dirInfo(name, modified), nil
}

// ListDir lists the blobs and directories directly below dirPath
// with a hierarchical listing. It returns an error wrapping
// os.ErrNotExist for a missing directory and an
// fs.ErrIsNotDirectory for a blob.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	prefix := dirPrefix(dirPath)
	// Any blob below the prefix, the marker included, makes it a directory
	found := prefix == ""
	pager := f.client.NewListBlobsHierarchyPager(Separator, &container.ListBlobsHierarchyOptions{Prefix: new(prefix)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return redactErr(err)
		}
		found = found || len(page.Segment.BlobPrefixes) > 0 || len(page.Segment.BlobItems) > 0
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := strings.TrimSuffix(strings.TrimPrefix(deref(blobPrefix.Name), prefix), Separator)
			if !isPathName(name) {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if err := callback(f.dirInfo(prefix+name, time.Time{})); err != nil {
				return err
			}
		}
		err = f.callbackBlobItems(ctx, prefix, page.Segment.BlobItems, patterns, callback)
		if err != nil {
			return err
		}
	}
	if !found {
		return f.listMissingError(dirPath)
	}
	return nil
}

// listMissingError returns the error for a listing of dirPath
// that found no blob: an fs.ErrIsNotDirectory for a blob,
// an error wrapping os.ErrNotExist for a missing path,
// or nil for an empty directory that was created in between.
func (f *fileSystem) listMissingError(dirPath string) error {
	info, err := f.Stat(dirPath)
	if err != nil {
		return err
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(info.File)
	}
	return nil
}

// callbackBlobItems calls callback with the FileInfo of every listed
// blob below prefix that matches patterns, skipping directory markers
// and blobs without a path.
func (f *fileSystem) callbackBlobItems(ctx context.Context, prefix string, items []*container.BlobItem, patterns []string, callback func(*fs.FileInfo) error) error {
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := deref(item.Name)
		// Directory markers end with a slash and are skipped
		if !isPathName(strings.TrimPrefix(name, prefix)) {
			continue
		}
		match, err := fsimpl.MatchAnyPattern(path.Base(name), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		var size int64
		var modified time.Time
		if item.Properties != nil {
			size, modified = deref(item.Properties.ContentLength), deref(item.Properties.LastModified)
		}
		if err := callback(f.fileInfo(name, size, modified)); err != nil {
			return err
		}
	}
	return nil
}

// ListDirRecursive lists all blobs below dirPath with a flat listing.
// It returns an error wrapping os.ErrNotExist for a missing directory
// and an fs.ErrIsNotDirectory for a blob.
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	prefix := dirPrefix(dirPath)
	found := prefix == ""
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(prefix)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return redactErr(err)
		}
		found = found || len(page.Segment.BlobItems) > 0
		err = f.callbackBlobItems(ctx, prefix, page.Segment.BlobItems, patterns, callback)
		if err != nil {
			return err
		}
	}
	if !found {
		return f.listMissingError(dirPath)
	}
	return nil
}

// OpenReader returns a reader that streams the blob and seeks with range requests.
func (f *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, fs.NewErrIsDirectory(info.File)
	}
	blobClient := f.client.NewBlobClient(blobName(filePath))
	return &fsimpl.RangeReader{
		Size: info.Size,
		Open: func(offset, count int64) (io.ReadCloser, error) {
			// A zero Count downloads from Offset to the end
			response, err := blobClient.DownloadStream(context.Background(), &blob.DownloadStreamOptions{
				Range: blob.HTTPRange{Offset: offset, Count: max(count, 0)},
			})
			if err != nil {
				return nil, redactErr(err)
			}
			if response.Body == nil {
				return nil, f.notModifiedError(blobName(filePath))
			}
			return response.Body, nil
		},
	}, nil
}

// maxSizeHint caps the buffer that ReadAll allocates up front for the
// Content-Length of a download, so that a huge blob or a wrong header
// can't allocate a huge buffer before any data arrived, which would
// end the process with an out-of-memory error that no context can cancel.
// A longer body grows the buffer while it is read.
const maxSizeHint = 16 << 20 // 16 MiB

// ReadAll downloads the blob. The buffer is allocated once for the
// Content-Length of the download, up to 16 MiB, a larger blob grows it.
// A blob that is larger than an int can address is an error.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	name := blobName(filePath)
	if name == "" {
		return nil, fs.NewErrIsDirectory(f.RootDir())
	}
	response, err := f.client.NewBlobClient(name).DownloadStream(ctx, nil)
	if err != nil {
		if isNotFound(err) {
			return nil, f.notExistError(filePath)
		}
		return nil, redactErr(err)
	}
	if response.Body == nil {
		return nil, f.notModifiedError(name)
	}
	defer response.Body.Close()
	size := max(deref(response.ContentLength), 0)
	if size > math.MaxInt {
		return nil, fmt.Errorf("azureblobfs: %s is too large to read into memory", f.file(name))
	}
	// Allocate the buffer once for the size of the blob up to maxSizeHint,
	// ctx cancels reading the body
	buf := bytes.NewBuffer(make([]byte, 0, int(min(size, maxSizeHint))+bytes.MinRead))
	if _, err := buf.ReadFrom(response.Body); err != nil {
		return nil, redactErr(err)
	}
	return buf.Bytes(), nil
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads data as block blob, overwriting an existing one.
// It returns an fs.ErrIsDirectory error for a directory.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := blobName(filePath)
	if err := f.checkNotDir(ctx, name); err != nil {
		return err
	}
	_, err := f.client.NewBlockBlobClient(name).UploadBuffer(ctx, data, &blockblob.UploadBufferOptions{
		Concurrency: uploadConcurrency,
	})
	return redactErr(err)
}

// notModifiedError is returned for a download without body:
// azblob reports a 304 Not Modified answer as a response
// without body and without error.
func (f *fileSystem) notModifiedError(name string) error {
	return fmt.Errorf("azureblobfs: download of %s returned no body (304 Not Modified)", f.file(name))
}

// OpenWriter returns a writer that streams the written data to an upload
// started by the first Write: blocks of 4 MiB are staged while the data
// is written and committed on Close, smaller data is uploaded with a single
// request on Close. The blob only appears (or changes) when Close
// completes the upload. The writer implements fs.WriteAborter,
// Abort cancels the upload and commits nothing.
// It returns an fs.ErrIsDirectory error for a directory.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkWritable(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	name := blobName(filePath)
	if err := f.checkNotDir(context.Background(), name); err != nil {
		return nil, err
	}
	blockBlob := f.client.NewBlockBlobClient(name)
	return newUploadWriter(
		func(ctx context.Context, body io.Reader) error {
			_, err := blockBlob.UploadStream(ctx, body, &blockblob.UploadStreamOptions{
				BlockSize:   uploadBlockSize,
				Concurrency: uploadConcurrency,
			})
			return redactErr(err)
		},
		f.checkWritable,
	), nil
}

// OpenReadWriter downloads the blob (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
// The returned value implements fs.WriteAborter.
func (f *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if err := f.checkWritable(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	data, err := f.ReadAll(context.Background(), filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(data, func(data []byte) error {
		return f.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// blobItem returns the listed item of the blob name with its metadata,
// or nil if the blob doesn't exist. A listing returns the metadata names
// as stored, GetProperties returns them in the canonical case of HTTP
// headers ("Owner" for "owner"), so setting them again would rename them.
// The blob itself is the first one listed with its name as prefix,
// because a name sorts before all names it is a prefix of.
func (f *fileSystem) blobItem(ctx context.Context, name string) (*container.BlobItem, error) {
	item, err := firstBlob(ctx, f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix:     new(name),
		MaxResults: new(int32(1)),
		Include:    container.ListBlobsInclude{Metadata: true},
	}))
	if err != nil || item == nil || deref(item.Name) != name {
		return nil, err
	}
	return item, nil
}

// Touch updates the modification time of an existing blob by setting
// its metadata again, which keeps the metadata, the content headers and
// the content. It creates an empty blob for a missing path, but never
// overwrites a blob that was created in the meantime,
// and does nothing for a directory.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	name := blobName(filePath)
	if name == "" {
		return nil // Root directory
	}
	ctx := context.Background()
	item, err := f.blobItem(ctx, name)
	if err != nil {
		return err
	}
	if item == nil {
		isDir, _, err := f.dirExists(ctx, name)
		if err != nil || isDir {
			return err
		}
		_, err = f.client.NewBlockBlobClient(name).UploadBuffer(ctx, nil, &blockblob.UploadBufferOptions{
			// Don't replace a blob that was created after the listing
			AccessConditions: &blob.AccessConditions{
				ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: new(azcore.ETagAny)},
			},
		})
		if bloberror.HasCode(err, bloberror.BlobAlreadyExists, bloberror.ConditionNotMet) {
			return nil // Created after the listing, so modified anyway
		}
		return redactErr(err)
	}
	opts := &blob.SetMetadataOptions{}
	if item.Properties != nil && item.Properties.ETag != nil {
		// Don't replace the metadata of a blob that changed after the listing
		opts.AccessConditions = &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfMatch: item.Properties.ETag},
		}
	}
	_, err = f.client.NewBlobClient(name).SetMetadata(ctx, item.Metadata, opts)
	if bloberror.HasCode(err, bloberror.ConditionNotMet) {
		return nil // Changed after the listing, so modified anyway
	}
	return redactErr(err)
}

// MakeDir creates a zero-byte directory marker blob "dirPath/".
// It returns an error wrapping os.ErrExist if the path exists.
func (f *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	info, err := f.Stat(dirPath)
	switch {
	case err == nil:
		return fs.NewErrAlreadyExists(info.File)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	_, err = f.client.NewBlockBlobClient(dirPrefix(dirPath)).UploadBuffer(context.Background(), nil, nil)
	return redactErr(err)
}

// Remove deletes a blob or an empty directory marker.
// It returns an error wrapping os.ErrNotExist for a missing path,
// an fs.ErrDirectoryNotEmpty for a directory with content,
// and an error wrapping os.ErrInvalid for the root directory.
func (f *fileSystem) Remove(filePath string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	info, err := f.Stat(filePath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	name := blobName(filePath)
	if !info.IsDir {
		_, err = f.client.NewBlobClient(name).Delete(ctx, nil)
		return redactErr(err)
	}
	if name == "" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	// The directory is empty if no blob but its marker has the prefix.
	// Azure can return pages with fewer blobs than MaxResults, even empty
	// ones, with a continuation marker, so the listing is read until it
	// has a blob besides the marker or ends.
	marker := name + Separator
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(marker), MaxResults: new(int32(2))})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return redactErr(err)
		}
		for _, item := range page.Segment.BlobItems {
			if deref(item.Name) != marker {
				return fs.NewErrDirectoryNotEmpty(info.File)
			}
		}
	}
	_, err = f.client.NewBlobClient(marker).Delete(ctx, nil)
	if err != nil && isNotFound(err) {
		return nil // implicit directory without marker and without content
	}
	return redactErr(err)
}

// RemoveAll deletes the blob at filePath and every blob below it.
// A missing path is not an error. The root directory can't be removed,
// that would delete every blob of the container, it is an error
// wrapping os.ErrInvalid.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	name := blobName(filePath)
	if name == "" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, f)
	}
	_, err := f.client.NewBlobClient(name).Delete(ctx, nil)
	if err != nil && !isNotFound(err) {
		return redactErr(err)
	}
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(name + Separator)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return redactErr(err)
		}
		for _, item := range page.Segment.BlobItems {
			_, err := f.client.NewBlobClient(deref(item.Name)).Delete(ctx, nil)
			if err != nil && !isNotFound(err) {
				return redactErr(err)
			}
		}
	}
	return nil
}

// CopyFile copies a blob server-side within the container.
// Copying a blob onto itself is a no-op.
// It returns an fs.ErrIsDirectory error if srcFile or destFile is a directory.
func (f *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	src, dest := blobName(srcFile), blobName(destFile)
	if src == dest {
		return nil
	}
	if src == "" || dest == "" {
		return fs.NewErrIsDirectory(f.RootDir())
	}
	srcBlob := f.client.NewBlobClient(src)
	if _, err := srcBlob.GetProperties(ctx, nil); err != nil {
		if isNotFound(err) {
			return f.notExistError(srcFile)
		}
		return redactErr(err)
	}
	if err := f.checkNotDir(ctx, dest); err != nil {
		return err
	}
	destBlob := f.client.NewBlobClient(dest)
	response, err := destBlob.StartCopyFromURL(ctx, srcBlob.URL(), nil)
	if err != nil {
		if isNotFound(err) {
			return f.notExistError(srcFile)
		}
		return redactErr(err)
	}
	status := deref(response.CopyStatus)
	for status == blob.CopyStatusTypePending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		props, err := destBlob.GetProperties(ctx, nil)
		if err != nil {
			return redactErr(err)
		}
		status = deref(props.CopyStatus)
	}
	if status != blob.CopyStatusTypeSuccess {
		return fmt.Errorf("azureblobfs: copying %s to %s: %s", f.file(srcFile), f.file(destFile), status)
	}
	return nil
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards, also the Close of writers
// that were opened before. Close is idempotent.
func (f *fileSystem) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	fs.Unregister(f)
	return nil
}
