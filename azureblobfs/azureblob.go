// Package azureblobfs implements a go-fs file system for an Azure Blob
// Storage container using the Azure SDK for Go.
//
// Blob storage has no directories: a blob named "docs/readme.txt" is a
// file whose "directory" docs exists implicitly. MakeDir creates a
// zero-byte marker blob with a trailing slash ("docs/") so that empty
// directories exist too, like s3fs does for S3.
package azureblobfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of azureblobfs URIs, followed by the host and container of the container URL
	Prefix = "azblob://"

	// Separator used in azureblobfs paths
	Separator = "/"
)

var (
	// DefaultPermissions reported for blobs
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute

	// Compile-time interface checks
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

type fileSystem struct {
	fsimpl.PathHelper

	client   *container.Client
	readOnly bool
	closed   atomic.Bool
}

// NewAndRegister creates a file system for the container of the client
// and registers it. ctx is used for a request that verifies the container.
// With readOnly true every write operation returns fs.ErrReadOnlyFileSystem.
//
// The URI prefix is "azblob://" + host + container path of the container
// URL, for example "azblob://myaccount.blob.core.windows.net/mycontainer".
func NewAndRegister(ctx context.Context, client *container.Client, readOnly bool) (fs.FileSystem, error) {
	f, err := New(ctx, client, readOnly)
	if err != nil {
		return nil, err
	}
	fs.Register(f)
	return f, nil
}

// New creates a file system for the container of the client
// without registering it, see NewAndRegister.
func New(ctx context.Context, client *container.Client, readOnly bool) (fs.FileSystem, error) {
	if client == nil {
		return nil, errors.New("azureblobfs: nil container client")
	}
	u, err := url.Parse(client.URL())
	if err != nil {
		return nil, err
	}
	_, err = client.GetProperties(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("azureblobfs: container %s: %w", client.URL(), err)
	}
	return &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + u.Host + strings.TrimSuffix(u.Path, "/"), Rooted: true},
		client:     client,
		readOnly:   readOnly,
	}, nil
}

// NewFromConnectionString creates and registers a file system for a
// container using an Azure Storage connection string, see NewAndRegister.
func NewFromConnectionString(ctx context.Context, connectionString, containerName string, readOnly bool) (fs.FileSystem, error) {
	client, err := container.NewClientFromConnectionString(connectionString, containerName, nil)
	if err != nil {
		return nil, err
	}
	return NewAndRegister(ctx, client, readOnly)
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !f.readOnly
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.URIPrefix + Separator)
}

func (f *fileSystem) ID() string {
	return strings.TrimPrefix(f.URIPrefix, Prefix)
}

func (f *fileSystem) Name() string {
	return "Azure Blob file system"
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

func isNotFound(err error) bool {
	return bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound, bloberror.ResourceNotFound)
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
		return nil, err
	}
	// Marker blob or implicit directory
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix:     new(name + Separator),
		MaxResults: new(int32(1)),
	})
	page, err := pager.NextPage(ctx)
	if err != nil {
		return nil, err
	}
	if len(page.Segment.BlobItems) == 0 {
		return nil, fs.NewErrDoesNotExist(f.file(filePath))
	}
	item := page.Segment.BlobItems[0]
	var modified time.Time
	if deref(item.Name) == name+Separator && item.Properties != nil {
		modified = deref(item.Properties.LastModified)
	}
	return f.dirInfo(name, modified), nil
}

// ListDir lists the blobs and directories directly below dirPath
// with a hierarchical listing. A missing directory lists nothing.
func (f *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	prefix := dirPrefix(dirPath)
	pager := f.client.NewListBlobsHierarchyPager(Separator, &container.ListBlobsHierarchyOptions{Prefix: new(prefix)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, blobPrefix := range page.Segment.BlobPrefixes {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := strings.TrimSuffix(deref(blobPrefix.Name), Separator)
			match, err := fsimpl.MatchAnyPattern(path.Base(name), patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			if err := callback(f.dirInfo(name, time.Time{})); err != nil {
				return err
			}
		}
		err = f.callbackBlobItems(ctx, page.Segment.BlobItems, patterns, callback)
		if err != nil {
			return err
		}
	}
	return nil
}

// callbackBlobItems calls callback with the FileInfo of every
// listed blob that matches patterns, skipping directory markers.
func (f *fileSystem) callbackBlobItems(ctx context.Context, items []*container.BlobItem, patterns []string, callback func(*fs.FileInfo) error) error {
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := deref(item.Name)
		if name == "" || strings.HasSuffix(name, Separator) {
			continue // directory marker
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
func (f *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if err := f.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(dirPrefix(dirPath))})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		err = f.callbackBlobItems(ctx, page.Segment.BlobItems, patterns, callback)
		if err != nil {
			return err
		}
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
				return nil, err
			}
			return response.Body, nil
		},
	}, nil
}

// ReadAll downloads the blob.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	response, err := f.client.NewBlobClient(blobName(filePath)).DownloadStream(ctx, nil)
	if err != nil {
		if isNotFound(err) {
			return nil, f.notExistError(filePath)
		}
		return nil, err
	}
	defer response.Body.Close()
	return fs.ReadAllContext(ctx, response.Body)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads data as block blob, overwriting an existing one.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	_, err := f.client.NewBlockBlobClient(blobName(filePath)).UploadBuffer(ctx, data, nil)
	return err
}

// OpenWriter returns a writer that buffers the data in memory
// and uploads it on Close.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkWritable(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return f.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// OpenReadWriter downloads the blob (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
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

// Touch updates the modification time of an existing blob by setting its
// metadata, creates an empty blob for a missing path and does nothing
// for a directory.
func (f *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	info, err := f.Stat(filePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return f.WriteAll(context.Background(), filePath, nil, perm)
	case err != nil:
		return err
	case info.IsDir:
		return nil
	}
	_, err = f.client.NewBlobClient(blobName(filePath)).SetMetadata(context.Background(), nil, nil)
	return err
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
	return err
}

// Remove deletes a blob or an empty directory marker.
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
		return err
	}
	if name == "" {
		return fmt.Errorf("can't remove root directory of %s", f)
	}
	marker := name + Separator
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(marker), MaxResults: new(int32(2))})
	page, err := pager.NextPage(ctx)
	if err != nil {
		return err
	}
	for _, item := range page.Segment.BlobItems {
		if deref(item.Name) != marker {
			return fmt.Errorf("directory not empty: %s", info.File)
		}
	}
	_, err = f.client.NewBlobClient(marker).Delete(ctx, nil)
	if err != nil && isNotFound(err) {
		return nil // implicit directory without marker and without content
	}
	return err
}

// RemoveAll deletes the blob at filePath and every blob below it.
// A missing path is not an error.
func (f *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if name := blobName(filePath); name != "" {
		_, err := f.client.NewBlobClient(name).Delete(ctx, nil)
		if err != nil && !isNotFound(err) {
			return err
		}
	}
	pager := f.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: new(dirPrefix(filePath))})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, item := range page.Segment.BlobItems {
			_, err := f.client.NewBlobClient(deref(item.Name)).Delete(ctx, nil)
			if err != nil && !isNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// CopyFile copies a blob server-side within the container.
// Copying a blob onto itself is a no-op.
func (f *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := f.checkWritable(); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	src, dest := blobName(srcFile), blobName(destFile)
	if src == dest {
		return nil
	}
	destBlob := f.client.NewBlobClient(dest)
	response, err := destBlob.StartCopyFromURL(ctx, f.client.NewBlobClient(src).URL(), nil)
	if err != nil {
		if isNotFound(err) {
			return f.notExistError(srcFile)
		}
		return err
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
			return err
		}
		status = deref(props.CopyStatus)
	}
	if status != blob.CopyStatusTypeSuccess {
		return fmt.Errorf("azureblobfs: copying %s to %s: %s", f.file(srcFile), f.file(destFile), status)
	}
	return nil
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
