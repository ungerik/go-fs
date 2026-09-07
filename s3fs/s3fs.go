// Package s3fs implements a go-fs file system for Amazon S3 and
// S3-compatible object stores like MinIO.
//
// S3 has no directories: an object key like "docs/readme.txt" is a file
// whose "directory" docs exists implicitly. MakeDir creates a zero-byte
// marker object with a trailing slash ("docs/") so that empty directories
// exist too. Stat, ListDir and Remove treat both the marker and the
// implicit prefix as a directory.
package s3fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of S3 file system URIs, followed by the bucket name
	Prefix = "s3://"

	// Separator used in S3 file system paths
	Separator = "/"

	// deleteBatchSize is the maximum number of keys
	// a single DeleteObjects request accepts.
	deleteBatchSize = 1000
)

var (
	// MultipartUploadThreshold is the minimum size in bytes from which
	// WriteAll uses a multipart upload instead of a single PutObject.
	MultipartUploadThreshold int64 = 5 * 1024 * 1024

	// MultipartDownloadThreshold is the minimum size in bytes from which
	// ReadAll downloads the object in concurrent parts.
	MultipartDownloadThreshold int64 = 10 * 1024 * 1024

	// DefaultPermissions reported for S3 objects
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for S3 directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite

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

	mtx        sync.RWMutex
	client     *s3.Client // nil after Close
	bucketName string
	readOnly   bool
}

// NewAndRegister creates a file system for an S3 bucket and registers it.
//
// The client must be a configured S3 client of the AWS SDK v2.
// With readOnly true every write operation returns fs.ErrReadOnlyFileSystem.
//
// The file system uses the URI prefix "s3://bucket-name", so file URIs
// look like "s3://bucket-name/path/to/object" like in the AWS CLI.
func NewAndRegister(client *s3.Client, bucketName string, readOnly bool) fs.FileSystem {
	s3fs := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + bucketName, Rooted: true},
		client:     client,
		bucketName: bucketName,
		readOnly:   readOnly,
	}
	fs.Register(s3fs)
	return s3fs
}

// NewLoadDefaultConfig creates and registers a file system for an S3 bucket
// using the default AWS credential chain (environment variables,
// ~/.aws/credentials, IAM roles). ctx is used for loading the configuration.
func NewLoadDefaultConfig(ctx context.Context, bucketName string, readOnly bool) (fs.FileSystem, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return NewAndRegister(s3.NewFromConfig(cfg), bucketName, readOnly), nil
}

// getClient returns the S3 client or fs.ErrFileSystemClosed after Close.
func (s *fileSystem) getClient() (*s3.Client, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	if s.client == nil {
		return nil, fs.ErrFileSystemClosed
	}
	return s.client, nil
}

// getWriteClient is getClient plus the read-only check.
func (s *fileSystem) getWriteClient() (*s3.Client, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, fs.ErrReadOnlyFileSystem
	}
	return client, nil
}

// ReadableWritable returns true and the negated readOnly flag.
func (s *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !s.readOnly
}

// RootDir returns "s3://bucket-name/" as File.
func (s *fileSystem) RootDir() fs.File {
	return fs.File(s.URIPrefix + Separator)
}

// ID returns the bucket name.
func (s *fileSystem) ID() string {
	return s.bucketName
}

// Name returns a human-readable name for the file system.
func (s *fileSystem) Name() string {
	return "S3 file system for bucket: " + s.bucketName
}

// String returns the name and prefix of the file system.
func (s *fileSystem) String() string {
	return s.Name() + " with prefix " + s.URIPrefix
}

///////////////////////////////////////////////////////////////////////////////
// Keys and errors

// key returns the object key of a file system path.
// Paths are rooted ("/a/b") but object keys have no leading slash.
// The root directory has the empty key.
func key(filePath string) string {
	return strings.Trim(filePath, Separator)
}

// dirKey returns the key prefix of a directory path: the key
// with a trailing slash, or the empty string for the root.
func dirKey(dirPath string) string {
	k := key(dirPath)
	if k == "" {
		return ""
	}
	return k + Separator
}

// copySource returns the URL-encoded CopySource value for an object key.
func (s *fileSystem) copySource(k string) string {
	segments := strings.Split(k, Separator)
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return s.bucketName + Separator + strings.Join(segments, Separator)
}

// isNotFound reports whether err is an S3 "not found" response.
// HeadObject returns types.NotFound, GetObject and CopyObject NoSuchKey.
func isNotFound(err error) bool {
	if _, ok := errors.AsType[*types.NotFound](err); ok {
		return true
	}
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}

// notExistError returns an error wrapping os.ErrNotExist for a file path,
// or fs.ErrIsDirectory if the path is a directory.
func (s *fileSystem) notExistError(filePath string) error {
	info, err := s.Stat(filePath)
	if err == nil && info.IsDir {
		return fs.NewErrIsDirectory(info.File)
	}
	return fs.NewErrDoesNotExist(fs.File(s.JoinCleanURI(filePath)))
}

func (s *fileSystem) fileInfo(k string, size int64, modified time.Time) *fs.FileInfo {
	name := path.Base(k)
	return &fs.FileInfo{
		File:        fs.File(s.JoinCleanURI(k)),
		Name:        name,
		Exists:      true,
		IsRegular:   true,
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        size,
		Modified:    modified,
		Permissions: DefaultPermissions,
	}
}

func (s *fileSystem) dirInfo(k string, modified time.Time) *fs.FileInfo {
	if k == "" {
		return &fs.FileInfo{
			File:        s.RootDir(),
			Name:        Separator,
			Exists:      true,
			IsDir:       true,
			Permissions: DefaultDirPermissions,
		}
	}
	name := path.Base(k)
	return &fs.FileInfo{
		File:        fs.File(s.JoinCleanURI(k)),
		Name:        name,
		Exists:      true,
		IsDir:       true,
		IsHidden:    strings.HasPrefix(name, "."),
		Modified:    modified,
		Permissions: DefaultDirPermissions,
	}
}

///////////////////////////////////////////////////////////////////////////////
// Read operations

// Stat returns the FileInfo of an object or directory.
//
// Objects are looked up with HeadObject. A path that is not an object
// is a directory if a marker object with a trailing slash exists or
// if any object key starts with the path and a slash (implicit directory),
// checked with a single ListObjectsV2 request limited to one key.
func (s *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	k := key(filePath)
	if k == "" {
		return s.dirInfo("", time.Time{}), nil
	}
	ctx := context.Background()
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
	})
	if err == nil {
		return s.fileInfo(k, aws.ToInt64(head.ContentLength), aws.ToTime(head.LastModified)), nil
	}
	if !isNotFound(err) {
		return nil, err
	}
	// Marker object or implicit directory
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &s.bucketName,
		Prefix:  aws.String(k + Separator),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(out.Contents) == 0 {
		return nil, fs.NewErrDoesNotExist(fs.File(s.JoinCleanURI(k)))
	}
	var modified time.Time
	if obj := out.Contents[0]; aws.ToString(obj.Key) == k+Separator {
		modified = aws.ToTime(obj.LastModified)
	}
	return s.dirInfo(k, modified), nil
}

// ListDir lists the objects and (marker or implicit) directories
// directly below dirPath using ListObjectsV2 with the "/" delimiter.
// A missing directory lists nothing.
func (s *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return s.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive lists all objects below dirPath with a single
// paginated ListObjectsV2 request without delimiter.
// Directory markers are skipped, patterns match object names.
func (s *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return s.listDir(ctx, dirPath, patterns, callback, true)
}

func (s *fileSystem) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error, recursive bool) error {
	client, err := s.getClient()
	if err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prefix := dirKey(dirPath)
	input := &s3.ListObjectsV2Input{
		Bucket: &s.bucketName,
		Prefix: aws.String(prefix),
	}
	if !recursive {
		input.Delimiter = aws.String(Separator)
	}
	paginator := s3.NewListObjectsV2Paginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, commonPrefix := range page.CommonPrefixes {
			if err := ctx.Err(); err != nil {
				return err
			}
			k := strings.TrimSuffix(aws.ToString(commonPrefix.Prefix), Separator)
			if k == "" {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(path.Base(k), patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			err = callback(s.dirInfo(k, time.Time{}))
			if err != nil {
				return err
			}
		}
		for _, obj := range page.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			k := aws.ToString(obj.Key)
			if k == "" || strings.HasSuffix(k, Separator) {
				continue // directory marker
			}
			match, err := fsimpl.MatchAnyPattern(path.Base(k), patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			err = callback(s.fileInfo(k, aws.ToInt64(obj.Size), aws.ToTime(obj.LastModified)))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// OpenReader returns the streaming body of a GetObject request.
func (s *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	k := key(filePath)
	out, err := client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, s.notExistError(filePath)
		}
		return nil, err
	}
	return out.Body, nil
}

// ReadAll returns the content of an object.
// Objects of at least MultipartDownloadThreshold bytes
// are downloaded in concurrent parts.
func (s *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k := key(filePath)
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, s.notExistError(filePath)
		}
		return nil, err
	}
	size := aws.ToInt64(head.ContentLength)
	if size >= MultipartDownloadThreshold {
		buf := manager.NewWriteAtBuffer(make([]byte, 0, size))
		_, err = manager.NewDownloader(client).Download(ctx, buf, &s3.GetObjectInput{
			Bucket: &s.bucketName,
			Key:    &k,
		})
		if err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, s.notExistError(filePath)
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

///////////////////////////////////////////////////////////////////////////////
// Write operations

// WriteAll creates or overwrites an object.
// Data of at least MultipartUploadThreshold bytes is uploaded in parts.
// perm is ignored, S3 access is controlled by IAM policies.
func (s *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	k := key(filePath)
	if int64(len(data)) >= MultipartUploadThreshold {
		_, err = manager.NewUploader(client).Upload(ctx, &s3.PutObjectInput{
			Bucket: &s.bucketName,
			Key:    &k,
			Body:   bytes.NewReader(data),
		})
		return err
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
		Body:   bytes.NewReader(data),
	})
	return err
}

// OpenWriter returns a writer that buffers the data in memory
// and uploads it as object on Close.
func (s *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if _, err := s.getWriteClient(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return s.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// OpenReadWriter downloads the object (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
func (s *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if _, err := s.getWriteClient(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	data, err := s.ReadAll(context.Background(), filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(data, func(data []byte) error {
		return s.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// Touch creates an empty object for a missing path and updates the
// modification time of an existing object by copying it onto itself.
// Touch of a directory does nothing.
func (s *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	ctx := context.Background()
	k := key(filePath)
	info, err := s.Stat(filePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s.WriteAll(ctx, filePath, nil, perm)
	case err != nil:
		return err
	case info.IsDir:
		return nil
	}
	// S3 can't update LastModified in place, copying the
	// object onto itself with replaced metadata rewrites it.
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            &s.bucketName,
		CopySource:        aws.String(s.copySource(k)),
		Key:               &k,
		MetadataDirective: types.MetadataDirectiveReplace,
	})
	return err
}

// MakeDir creates a zero-byte directory marker object "dirPath/".
// It returns an error wrapping os.ErrExist if the path exists
// as object or directory.
func (s *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	info, err := s.Stat(dirPath)
	switch {
	case err == nil:
		return fs.NewErrAlreadyExists(info.File)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	marker := dirKey(dirPath)
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &s.bucketName,
		Key:    &marker,
		Body:   bytes.NewReader(nil),
	})
	return err
}

// Remove deletes an object or an empty directory marker.
// It returns an error wrapping os.ErrNotExist for a missing path
// and refuses to remove a directory with content.
func (s *fileSystem) Remove(filePath string) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	info, err := s.Stat(filePath)
	if err != nil {
		return err
	}
	ctx := context.Background()
	k := key(filePath)
	if !info.IsDir {
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &s.bucketName,
			Key:    &k,
		})
		return err
	}
	if k == "" {
		return fmt.Errorf("can't remove root directory of %s", s)
	}
	marker := k + Separator
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &s.bucketName,
		Prefix:  &marker,
		MaxKeys: aws.Int32(2),
	})
	if err != nil {
		return err
	}
	for _, obj := range out.Contents {
		if aws.ToString(obj.Key) != marker {
			return fmt.Errorf("directory not empty: %s", info.File)
		}
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucketName,
		Key:    &marker,
	})
	return err
}

// RemoveAll deletes the object at filePath and every object below it
// (including directory markers) with batched DeleteObjects requests.
// A missing path is not an error.
func (s *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var batch []types.ObjectIdentifier
	deleteBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		out, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &s.bucketName,
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return err
		}
		for _, e := range out.Errors {
			if code := aws.ToString(e.Code); code != "NoSuchKey" && code != "NotFound" {
				return fmt.Errorf("deleting %s: %s: %s", s.JoinCleanURI(aws.ToString(e.Key)), code, aws.ToString(e.Message))
			}
		}
		batch = batch[:0]
		return nil
	}
	if k := key(filePath); k != "" {
		batch = append(batch, types.ObjectIdentifier{Key: aws.String(k)})
	}
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: &s.bucketName,
		Prefix: aws.String(dirKey(filePath)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			batch = append(batch, types.ObjectIdentifier{Key: obj.Key})
			if len(batch) == deleteBatchSize {
				if err := deleteBatch(); err != nil {
					return err
				}
			}
		}
	}
	return deleteBatch()
}

// CopyFile copies an object within the bucket server-side with CopyObject.
// Copying an object onto itself is a no-op.
func (s *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	srcKey, destKey := key(srcFile), key(destFile)
	if srcKey == destKey {
		return nil
	}
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     &s.bucketName,
		CopySource: aws.String(s.copySource(srcKey)),
		Key:        &destKey,
	})
	if err != nil && isNotFound(err) {
		return s.notExistError(srcFile)
	}
	return err
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards. Close is idempotent.
func (s *fileSystem) Close() error {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.client == nil {
		return nil
	}
	fs.Unregister(s)
	s.client = nil
	return nil
}
