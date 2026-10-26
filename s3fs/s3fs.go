// Package s3fs implements a go-fs file system for Amazon S3 and
// S3-compatible object stores like MinIO.
//
// S3 has no directories: an object key like "docs/readme.txt" is a file
// whose "directory" docs exists implicitly. MakeDir creates a zero-byte
// marker object with a trailing slash ("docs/") so that empty directories
// exist too. Stat, ListDir and Remove treat both the marker and the
// implicit prefix as a directory.
//
// Object keys map to rooted paths without the leading slash: the path
// "/docs/readme.txt" is the key "docs/readme.txt". Keys that no path maps
// to, because they start with a slash or contain an empty element like
// "docs//readme.txt", are skipped by the listings.
package s3fs

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
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

	// DefaultPermissions reported for S3 objects
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for S3 directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute

	// deleteBatchSize is the maximum number of keys
	// a single DeleteObjects request accepts.
	deleteBatchSize = 1000

	// maxListKeys is the most keys one ListObjectsV2 page returns.
	maxListKeys = 1000

	// Defaults of the Options fields
	defaultMultipartUploadThreshold   = 16 << 20
	defaultMultipartDownloadThreshold = 16 << 20
	defaultPartSize                   = 8 << 20
	defaultConcurrency                = 5

	// maxCopyObjectSize is the size limit of a single CopyObject request,
	// larger objects are copied in parts with UploadPartCopy.
	maxCopyObjectSize = 5 << 30

	// copyPartSize is the minimum part size of multipart copies.
	// Parts are copied server-side, so they can be much larger
	// than the parts of uploads.
	copyPartSize = 512 << 20

	// maxUploadParts is the maximum number of parts of a multipart upload.
	maxUploadParts = 10000

	// failTimeout limits the requests that abort a failed or canceled
	// multipart upload, they run with a fresh context because the
	// context of the upload may be canceled.
	failTimeout = time.Minute
)

// Compile-time interface checks
var (
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ValidateNameFileSystem     = new(fileSystem)
	_ fs.ReadAllFileSystem          = new(fileSystem)
	_ fs.WriteAllFileSystem         = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.TouchFileSystem            = new(fileSystem)
	_ fs.CopyFileSystem             = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
)

// Options of New, NewAndRegister and NewLoadDefaultConfig.
// A nil Options or zero fields use the defaults.
type Options struct {
	// ReadOnly makes every write operation return fs.ErrReadOnlyFileSystem.
	ReadOnly bool

	// MultipartUploadThreshold is the size in bytes from which uploads
	// use a multipart upload instead of a single PutObject request.
	// Zero means 16 MiB.
	MultipartUploadThreshold int64

	// MultipartDownloadThreshold is the size in bytes from which ReadAll
	// downloads an object with concurrent range requests of PartSize bytes.
	// Zero means 16 MiB.
	MultipartDownloadThreshold int64

	// PartSize is the size in bytes of the parts of multipart uploads and
	// of the ranges of multipart downloads. Zero means 8 MiB.
	// S3 requires at least 5 MiB for every part but the last.
	// WriteAll increases it for large data to stay within the 10000 parts
	// of a multipart upload, OpenWriter streams data of unknown size,
	// so its writers accept at most 10000 * PartSize bytes.
	PartSize int64

	// Concurrency is the number of parts that a multipart upload,
	// download or copy transfers in parallel. Zero means 5.
	Concurrency int

	// Prefix is the URI prefix of the file system, Prefix + bucket if empty.
	// Use another prefix to register file systems for buckets with the
	// same name at different endpoints, like "s3://minio.example.com/backup".
	// It must begin with a scheme followed by "://", with no slash or
	// backslash before the "://", and must not end with a slash.
	Prefix string
}

type fileSystem struct {
	fsimpl.PathHelper

	mtx    sync.RWMutex
	client *s3.Client // nil after Close

	// transfer uses client. It is never changed after New, so it is read
	// without mtx; the methods check with getClient or getWriteClient
	// that the file system is open before they use it.
	transfer   *transfermanager.Client
	bucketName string
	id         string
	readOnly   bool

	uploadThreshold   int64
	downloadThreshold int64
	concurrency       int

	// maxCopyObjectSize and copyPartSize are fields
	// so that tests can copy small objects in parts.
	maxCopyObjectSize int64
	copyPartSize      int64
}

// New returns a file system for an S3 bucket without registering it,
// use fs.Register or NewAndRegister for that.
// Files of the file system are only usable via the File type
// while it is registered, because URIs are resolved with the registry.
//
// The client must be a configured S3 client of the AWS SDK v2.
// New doesn't send any request, a missing bucket or missing
// credentials are reported by the first operation.
//
// The file system uses the URI prefix "s3://bucket-name" unless
// Options.Prefix is set, so file URIs look like
// "s3://bucket-name/path/to/object" like in the AWS CLI.
func New(client *s3.Client, bucketName string, opts *Options) (fs.FileSystem, error) {
	if client == nil {
		return nil, errors.New("s3fs: nil client")
	}
	if bucketName == "" || strings.Contains(bucketName, Separator) {
		return nil, fmt.Errorf("s3fs: invalid bucket name %q", bucketName)
	}
	if opts == nil {
		opts = &Options{}
	}
	if opts.MultipartUploadThreshold < 0 || opts.MultipartDownloadThreshold < 0 || opts.PartSize < 0 || opts.Concurrency < 0 {
		return nil, errors.New("s3fs: negative Options value")
	}
	prefix, id := Prefix+bucketName, bucketName
	if opts.Prefix != "" {
		// A separator before the "://" makes it a path, see fs.ParseRawURI
		if i := strings.Index(opts.Prefix, fs.PrefixSeparator); i <= 0 || strings.ContainsAny(opts.Prefix[:i], `/\`) || strings.HasSuffix(opts.Prefix, Separator) {
			return nil, fmt.Errorf("s3fs: Options.Prefix %q must begin with a scheme followed by %q and must not end with %q", fsimpl.RedactAddress(opts.Prefix), fs.PrefixSeparator, Separator)
		}
		// The prefix is unique among the registered file systems,
		// the bucket name is not if Prefix distinguishes endpoints
		prefix, id = opts.Prefix, opts.Prefix
	}
	partSize := cmp.Or(opts.PartSize, defaultPartSize)
	concurrency := cmp.Or(opts.Concurrency, defaultConcurrency)
	uploadThreshold := cmp.Or(opts.MultipartUploadThreshold, defaultMultipartUploadThreshold)
	clientOpts := client.Options()
	transfer := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = partSize
		o.MultipartUploadThreshold = uploadThreshold
		o.Concurrency = concurrency
		o.FailTimeout = failTimeout
		o.GetObjectType = tmtypes.GetObjectRanges
		// The transfer manager defaults to CRC32 checksums regardless
		// of the client, so a client configured for services that
		// don't support them could not complete a multipart upload
		o.RequestChecksumCalculation = clientOpts.RequestChecksumCalculation
		o.DisableChecksumValidation = clientOpts.ResponseChecksumValidation == aws.ResponseChecksumValidationWhenRequired
	})
	return &fileSystem{
		PathHelper:        fsimpl.PathHelper{URIPrefix: prefix, Rooted: true},
		client:            client,
		transfer:          transfer,
		bucketName:        bucketName,
		id:                id,
		readOnly:          opts.ReadOnly,
		uploadThreshold:   uploadThreshold,
		downloadThreshold: cmp.Or(opts.MultipartDownloadThreshold, defaultMultipartDownloadThreshold),
		concurrency:       concurrency,
		maxCopyObjectSize: maxCopyObjectSize,
		copyPartSize:      copyPartSize,
	}, nil
}

// NewAndRegister creates a file system for an S3 bucket with New
// and registers it. It returns an error wrapping fs.ErrAlreadyRegistered
// if another file system is registered with the same prefix,
// for example for a bucket with the same name, see Options.Prefix.
// Close the file system to unregister it, for example with
// fs.GetFileSystemByPrefixOrNil("s3://bucket-name").Close().
func NewAndRegister(client *s3.Client, bucketName string, opts *Options) error {
	f, err := New(client, bucketName, opts)
	if err != nil {
		return err
	}
	return fs.Register(f)
}

// NewLoadDefaultConfig creates a file system for an S3 bucket like New
// using the default AWS credential chain (environment variables,
// ~/.aws/credentials, IAM roles). ctx is used for loading the configuration.
// The file system is not registered, see fs.Register.
func NewLoadDefaultConfig(ctx context.Context, bucketName string, opts *Options) (fs.FileSystem, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return New(s3.NewFromConfig(cfg), bucketName, opts)
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

// ReadableWritable returns true and the negated Options.ReadOnly.
func (s *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !s.readOnly
}

// ValidateName implements fs.ValidateNameFileSystem: a name is an error
// wrapping os.ErrInvalid if it is not valid UTF-8, which S3 requires for
// keys, if it contains a backslash, which S3 recommends to avoid and
// S3-compatible servers reject, or a control character, which the XML of
// a listing can't hold, so the directory couldn't be listed, or if it is
// longer than the 1024 bytes of a key. Existing objects are read with
// every name.
func (s *fileSystem) ValidateName(name string) error {
	var reason string
	switch {
	case !utf8.ValidString(name):
		reason = "is not valid UTF-8"
	case strings.Contains(name, `\`):
		reason = "contains a backslash, which S3-compatible servers reject"
	case strings.ContainsFunc(name, unicode.IsControl):
		reason = "contains a control character, which the XML of a listing can't hold"
	case len(name) > 1024:
		reason = "is longer than the 1024 bytes of a key"
	default:
		return nil
	}
	return fmt.Errorf("%w: s3fs name %.200q %s", os.ErrInvalid, name, reason)
}

// RootDir returns the URI prefix ("s3://bucket-name" or Options.Prefix)
// followed by a slash as File.
func (s *fileSystem) RootDir() fs.File {
	return fs.File(s.URIPrefix + Separator)
}

// ID returns the bucket name, or Options.Prefix if it was set.
func (s *fileSystem) ID() string {
	return s.id
}

// Name returns "S3".
func (s *fileSystem) Name() string {
	return "S3"
}

// String returns the name and the prefix of the file system
// with a redacted password, if Options.Prefix had one.
func (s *fileSystem) String() string {
	return s.Name() + " " + fsimpl.RedactURI(s.URIPrefix)
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

// isPathKey reports if a key relative to a listed directory
// is the key of a path: not empty and without empty elements,
// so it doesn't start or end with a slash or contain "//".
// The paths of the other keys would be cleaned to different keys,
// a key "d//x.txt" for example would list d as its own child.
func isPathKey(relKey string) bool {
	return relKey != "" &&
		!strings.HasPrefix(relKey, Separator) &&
		!strings.HasSuffix(relKey, Separator) &&
		!strings.Contains(relKey, Separator+Separator)
}

// copySourceEscaper escapes the "+" that url.PathEscape keeps,
// because S3 decodes a "+" in the CopySource as space.
var copySourceEscaper = strings.NewReplacer("+", "%2B")

// copySource returns the URL-encoded CopySource value for an object key.
func (s *fileSystem) copySource(k string) string {
	segments := strings.Split(k, Separator)
	for i, segment := range segments {
		segments[i] = copySourceEscaper.Replace(url.PathEscape(segment))
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
	return hasErrorCode(err, "NotFound", "NoSuchKey")
}

// isAccessDenied reports whether err is a 403 Forbidden response.
func isAccessDenied(err error) bool {
	if respErr, ok := errors.AsType[interface {
		error
		HTTPStatusCode() int
	}](err); ok {
		return respErr.HTTPStatusCode() == http.StatusForbidden
	}
	return false
}

// hasErrorCode reports whether err is an S3 API error with one of codes.
func hasErrorCode(err error, codes ...string) bool {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	return ok && slices.Contains(codes, apiErr.ErrorCode())
}

// file returns the File of an object key or path.
func (s *fileSystem) file(keyOrPath string) fs.File {
	return fs.File(s.JoinCleanURI(keyOrPath))
}

// notExistError returns an error wrapping os.ErrNotExist for a file path,
// or fs.ErrIsDirectory if the path is a directory.
func (s *fileSystem) notExistError(filePath string) error {
	info, err := s.Stat(filePath)
	if err == nil && info.IsDir {
		return fs.NewErrIsDirectory(info.File)
	}
	return fs.NewErrDoesNotExist(s.file(filePath))
}

func (s *fileSystem) fileInfo(k string, size int64, modified time.Time) *fs.FileInfo {
	name := path.Base(k)
	return &fs.FileInfo{
		File:        s.file(k),
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
		File:        s.file(k),
		Name:        name,
		Exists:      true,
		IsDir:       true,
		IsHidden:    strings.HasPrefix(name, "."),
		Modified:    modified,
		Permissions: DefaultDirPermissions,
	}
}

// errTruncatedWithoutToken is returned for a truncated ListObjectsV2 page
// without a new continuation token, also with a token of an earlier page.
// S3 always sends a new one, the request for the next page without it
// would list a page again, endlessly for a cycle of tokens, and ending
// the listing would decide from an incomplete one.
var errTruncatedWithoutToken = errors.New("s3fs: truncated listing page without a new continuation token")

// maxListingPages limits the ListObjectsV2 pages that findObject reads.
// A store that keeps answering with truncated pages without a matching
// key, each with a new continuation token, would otherwise make Stat and
// Remove send paid requests without end, and they get no context that
// could stop them. A real listing reaches the decision long before.
const maxListingPages = 1000

// errListingTooLong is returned by findObject after maxListingPages pages.
var errListingTooLong = errors.New("s3fs: listing has too many pages without a decision")

// findObject returns the first object with the key prefix for which
// match returns true, or nil if there is none, reading pages of at most
// maxKeys keys. A page can have fewer keys than MaxKeys and still be
// truncated, even no key at all, which S3-compatible stores do, so it
// reads pages until one has a matching object or the listing ends.
// A truncated page without a new continuation token, one that no earlier
// page returned, is an error wrapping errTruncatedWithoutToken, and the
// page after maxListingPages pages an error wrapping errListingTooLong:
// Stat and Remove call it without a context that could end the listing.
// The pages after a truncated page without a match ask for 1000 keys,
// the maximum of S3, so that every request gets as far as it can.
func (s *fileSystem) findObject(ctx context.Context, client *s3.Client, prefix string, maxKeys int32, match func(key string) bool) (*types.Object, error) {
	input := &s3.ListObjectsV2Input{
		Bucket:  &s.bucketName,
		Prefix:  &prefix,
		MaxKeys: &maxKeys,
	}
	var tokens map[string]bool // the continuation tokens of the listed pages
	for pages := 1; ; pages++ {
		if pages > maxListingPages {
			return nil, fmt.Errorf("listing %s: %w", s.file(prefix), errListingTooLong)
		}
		page, err := client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, err
		}
		for i := range page.Contents {
			if match(aws.ToString(page.Contents[i].Key)) {
				return &page.Contents[i], nil
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return nil, nil
		}
		token := aws.ToString(page.NextContinuationToken)
		if token == "" || tokens[token] {
			return nil, fmt.Errorf("listing %s: %w", s.file(prefix), errTruncatedWithoutToken)
		}
		if tokens == nil {
			tokens = make(map[string]bool)
		}
		tokens[token] = true
		input.ContinuationToken = &token
		input.MaxKeys = aws.Int32(maxListKeys)
	}
}

// dirExists reports if k is a directory: a marker object "k/" exists
// or an object below it (implicit directory), checked with a ListObjectsV2
// request limited to one key, which reads more pages only while they are
// truncated without a key. modified is the LastModified time of the marker
// or zero for an implicit directory.
// The root directory with the empty key always exists.
func (s *fileSystem) dirExists(ctx context.Context, client *s3.Client, k string) (exists bool, modified time.Time, err error) {
	if k == "" {
		return true, time.Time{}, nil
	}
	marker := k + Separator
	// The marker is the first key with its prefix, S3 lists keys in order
	obj, err := s.findObject(ctx, client, marker, 1, func(string) bool { return true })
	if err != nil || obj == nil {
		return false, time.Time{}, err
	}
	if aws.ToString(obj.Key) == marker {
		modified = aws.ToTime(obj.LastModified)
	}
	return true, modified, nil
}

// checkNotDir returns an fs.ErrIsDirectory error if k is a directory,
// so that writing an object k doesn't turn a directory into a file.
// Credentials without the s3:ListBucket permission, like an upload-only
// IAM policy, can't see directories, for them the check passes.
func (s *fileSystem) checkNotDir(ctx context.Context, client *s3.Client, k string) error {
	isDir, _, err := s.dirExists(ctx, client, k)
	if isAccessDenied(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if isDir {
		return fs.NewErrIsDirectory(s.dirInfo(k, time.Time{}).File)
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Read operations

// Stat returns the FileInfo of an object or directory.
//
// Objects are looked up with HeadObject. A path that is not an object
// is a directory if a marker object with a trailing slash exists or
// if any object key starts with the path and a slash (implicit directory),
// checked with a ListObjectsV2 request limited to one key, which reads
// more pages only while they are truncated without a key.
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
	isDir, modified, err := s.dirExists(ctx, client, k)
	if err != nil {
		return nil, err
	}
	if !isDir {
		return nil, fs.NewErrDoesNotExist(s.file(k))
	}
	return s.dirInfo(k, modified), nil
}

// ListDir lists the objects and (marker or implicit) directories
// directly below dirPath using ListObjectsV2 with the "/" delimiter.
// It returns an error wrapping os.ErrNotExist for a missing directory
// and an fs.ErrIsNotDirectory for an object.
func (s *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return s.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive lists all objects below dirPath with a single
// paginated ListObjectsV2 request without delimiter.
// Directory markers are skipped, patterns match object names.
// It returns an error wrapping os.ErrNotExist for a missing directory
// and an fs.ErrIsNotDirectory for an object.
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
	// Any key below the prefix, the marker included, makes it a directory
	found := prefix == ""
	paginator := s3.NewListObjectsV2Paginator(client, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		found = found || len(page.CommonPrefixes) > 0 || len(page.Contents) > 0
		for _, commonPrefix := range page.CommonPrefixes {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := strings.TrimSuffix(strings.TrimPrefix(aws.ToString(commonPrefix.Prefix), prefix), Separator)
			if !isPathKey(name) {
				continue
			}
			match, err := fsimpl.MatchAnyPattern(name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			err = callback(s.dirInfo(prefix+name, time.Time{}))
			if err != nil {
				return err
			}
		}
		for _, obj := range page.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			k := aws.ToString(obj.Key)
			// Directory markers end with a slash and are skipped
			if !isPathKey(strings.TrimPrefix(k, prefix)) {
				continue
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
	if !found {
		info, err := s.Stat(dirPath)
		if err != nil {
			return err
		}
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
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
	if k == "" {
		return nil, fs.NewErrIsDirectory(s.RootDir())
	}
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
// Objects of at least Options.MultipartDownloadThreshold bytes
// are downloaded with concurrent range requests.
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
	if k == "" {
		return nil, fs.NewErrIsDirectory(s.RootDir())
	}
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
	if size >= s.downloadThreshold {
		if size > math.MaxInt {
			return nil, fmt.Errorf("s3fs: %s is too large to read into memory", s.file(k))
		}
		buf := make(writeAtBuffer, size)
		_, err = s.transfer.DownloadObject(ctx, &transfermanager.DownloadObjectInput{
			Bucket:   &s.bucketName,
			Key:      &k,
			WriterAt: buf,
			// Every range request must read the same version of the object
			IfMatch: head.ETag,
		})
		if err != nil {
			if isNotFound(err) {
				return nil, s.notExistError(filePath)
			}
			return nil, err
		}
		return buf, nil
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
	// The size of HeadObject is below the multipart download threshold,
	// which bounds the buffer. It is only a hint, because the object can
	// be replaced after HeadObject. ctx cancels reading the body.
	hint := bytes.MinRead
	if size > 0 && size <= math.MaxInt-bytes.MinRead {
		hint += int(size)
	}
	buf := bytes.NewBuffer(make([]byte, 0, hint))
	if _, err := buf.ReadFrom(out.Body); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeAtBuffer is the io.WriterAt for the concurrent
// range downloads of an object with a known size.
// The ranges don't overlap, so no lock is needed.
type writeAtBuffer []byte

func (b writeAtBuffer) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(b)-len(p)) {
		return 0, errors.New("s3fs: downloaded range outside of the object size")
	}
	return copy(b[off:], p), nil
}

///////////////////////////////////////////////////////////////////////////////
// Write operations

// WriteAll creates or overwrites an object.
// Data of at least Options.MultipartUploadThreshold bytes
// is uploaded in parts. It returns an fs.ErrIsDirectory error
// for a directory. perm is ignored, S3 access is controlled by IAM policies.
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
	if err := s.checkNotDir(ctx, client, k); err != nil {
		return err
	}
	if int64(len(data)) >= s.uploadThreshold {
		return s.upload(ctx, k, bytes.NewReader(data))
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
		Body:   bytes.NewReader(data),
	})
	return err
}

// upload uploads the data of body to the object k, in parts if
// it has at least Options.MultipartUploadThreshold bytes.
func (s *fileSystem) upload(ctx context.Context, k string, body io.Reader) error {
	_, err := s.transfer.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
		Body:   body,
	})
	return err
}

// OpenWriter returns a writer that streams the written data to an upload
// started by the first Write: data up to Options.MultipartUploadThreshold
// is buffered and uploaded with a single request on Close, larger data is
// uploaded in parts of Options.PartSize while it is written. The object
// only appears (or changes) when Close completes the upload.
// The writer implements fs.WriteAborter, Abort cancels the upload.
// It returns an fs.ErrIsDirectory error for a directory.
func (s *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	client, err := s.getWriteClient()
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	k := key(filePath)
	if err := s.checkNotDir(context.Background(), client, k); err != nil {
		return nil, err
	}
	return newUploadWriter(
		func(ctx context.Context, body io.Reader) error {
			return s.upload(ctx, k, body)
		},
		func() error {
			_, err := s.getWriteClient()
			return err
		},
	), nil
}

// OpenReadWriter downloads the object (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
// The returned value implements fs.WriteAborter.
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
// modification time of an existing object by copying it onto itself,
// because S3 can't update LastModified in place. The copy keeps the
// Content-Type and the other content headers, the user metadata, the
// storage class and the server-side encryption of the object, objects
// above 5 GiB are copied in parts. It doesn't keep ACLs, and for objects
// above 5 GiB it doesn't keep the tags.
//
// It returns an fs.ErrUnsupported error if the service refuses to copy an
// object onto itself, like some S3-compatible services do.
// Touch of a directory does nothing.
func (s *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	k := key(filePath)
	if k == "" {
		return nil // Root directory
	}
	ctx := context.Background()
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &k,
	})
	if err != nil {
		if !isNotFound(err) {
			return err
		}
		isDir, _, err := s.dirExists(ctx, client, k)
		if err != nil || isDir {
			return err
		}
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &s.bucketName,
			Key:    &k,
			Body:   bytes.NewReader(nil),
		})
		return err
	}
	err = s.copyObject(ctx, client, k, k, head, true)
	switch {
	case hasErrorCode(err, "PreconditionFailed"):
		// The object was replaced after HeadObject,
		// so it has a new modification time anyway
		return nil
	case hasErrorCode(err, "InvalidRequest"):
		return fmt.Errorf("%w: %w", fs.NewErrUnsupported(s, "Touch of an existing object"), err)
	}
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
// It returns an error wrapping os.ErrNotExist for a missing path,
// an fs.ErrDirectoryNotEmpty for a directory with content,
// and an error wrapping os.ErrInvalid for the root directory.
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
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, s)
	}
	// The directory is empty if no object but its marker has the prefix.
	// A truncated page can have only the marker or no key at all,
	// so the listing is read until it has another object or ends.
	marker := k + Separator
	content, err := s.findObject(ctx, client, marker, 2, func(key string) bool { return key != marker })
	if err != nil {
		return err
	}
	if content != nil {
		return fs.NewErrDirectoryNotEmpty(info.File)
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucketName,
		Key:    &marker,
	})
	return err
}

// RemoveAll deletes the object at filePath and every object below it
// (including directory markers) with batched DeleteObjects requests.
// A missing path is not an error. The root directory can't be removed,
// that would delete every object of the bucket, it is an error
// wrapping os.ErrInvalid.
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
	k := key(filePath)
	if k == "" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, s)
	}
	batch := []types.ObjectIdentifier{{Key: aws.String(k)}}
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
				return fmt.Errorf("deleting %s: %s: %s", s.file(aws.ToString(e.Key)), code, aws.ToString(e.Message))
			}
		}
		batch = batch[:0]
		return nil
	}
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: &s.bucketName,
		Prefix: aws.String(k + Separator),
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

// CopyFile copies an object within the bucket server-side,
// objects above 5 GiB in parts. Copying an object onto itself is a no-op.
// It returns an fs.ErrIsDirectory error if srcFile or destFile is a directory.
func (s *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	client, err := s.getWriteClient()
	if err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	srcKey, destKey := key(srcFile), key(destFile)
	if srcKey == destKey {
		return nil
	}
	if srcKey == "" || destKey == "" {
		return fs.NewErrIsDirectory(s.RootDir())
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &srcKey,
	})
	if err != nil {
		if isNotFound(err) {
			return s.notExistError(srcFile)
		}
		return err
	}
	if err := s.checkNotDir(ctx, client, destKey); err != nil {
		return err
	}
	err = s.copyObject(ctx, client, srcKey, destKey, head, false)
	if isNotFound(err) {
		return s.notExistError(srcFile)
	}
	return err
}

// copyObject copies the object srcKey described by head to destKey,
// with a single CopyObject request up to maxCopyObjectSize
// and in parts with UploadPartCopy above.
//
// With touch true the copy replaces the metadata, which a copy of an
// object onto itself needs, with the metadata of head, and fails with a
// "PreconditionFailed" error if the object changed after HeadObject.
func (s *fileSystem) copyObject(ctx context.Context, client *s3.Client, srcKey, destKey string, head *s3.HeadObjectOutput, touch bool) error {
	if aws.ToInt64(head.ContentLength) > s.maxCopyObjectSize {
		return s.copyObjectInParts(ctx, client, srcKey, destKey, head)
	}
	input := &s3.CopyObjectInput{
		Bucket:     &s.bucketName,
		CopySource: aws.String(s.copySource(srcKey)),
		Key:        &destKey,
		// Without them the copy would get the defaults of the bucket
		StorageClass:         head.StorageClass,
		ServerSideEncryption: head.ServerSideEncryption,
		SSEKMSKeyId:          head.SSEKMSKeyId,
		BucketKeyEnabled:     head.BucketKeyEnabled,
	}
	if touch {
		input.CopySourceIfMatch = head.ETag
		input.MetadataDirective = types.MetadataDirectiveReplace
		input.Metadata = head.Metadata
		input.ContentType = head.ContentType
		input.ContentEncoding = head.ContentEncoding
		input.ContentDisposition = head.ContentDisposition
		input.ContentLanguage = head.ContentLanguage
		input.CacheControl = head.CacheControl
		input.Expires = expires(head)
		input.WebsiteRedirectLocation = head.WebsiteRedirectLocation
	}
	_, err := client.CopyObject(ctx, input)
	return err
}

// expires returns the parsed Expires header of head or nil.
func expires(head *s3.HeadObjectOutput) *time.Time {
	if head.ExpiresString == nil {
		return nil
	}
	t, err := http.ParseTime(*head.ExpiresString)
	if err != nil {
		return nil
	}
	return &t
}

// copyObjectInParts copies the object srcKey described by head to destKey
// with a multipart upload of UploadPartCopy requests, which is needed
// for objects above the 5 GiB of CopyObject. A multipart upload doesn't
// copy the metadata, so the metadata of head is set explicitly.
// The parts are copied with Options.Concurrency requests in parallel.
// A failed copy is aborted, so it leaves no parts behind.
func (s *fileSystem) copyObjectInParts(ctx context.Context, client *s3.Client, srcKey, destKey string, head *s3.HeadObjectOutput) (err error) {
	size := aws.ToInt64(head.ContentLength)
	partSize := max(s.copyPartSize, (size+maxUploadParts-1)/maxUploadParts)
	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:                  &s.bucketName,
		Key:                     &destKey,
		Metadata:                head.Metadata,
		ContentType:             head.ContentType,
		ContentEncoding:         head.ContentEncoding,
		ContentDisposition:      head.ContentDisposition,
		ContentLanguage:         head.ContentLanguage,
		CacheControl:            head.CacheControl,
		Expires:                 expires(head),
		WebsiteRedirectLocation: head.WebsiteRedirectLocation,
		StorageClass:            head.StorageClass,
		ServerSideEncryption:    head.ServerSideEncryption,
		SSEKMSKeyId:             head.SSEKMSKeyId,
		BucketKeyEnabled:        head.BucketKeyEnabled,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failTimeout)
		defer cancel()
		_, abortErr := client.AbortMultipartUpload(abortCtx, &s3.AbortMultipartUploadInput{
			Bucket:   &s.bucketName,
			Key:      &destKey,
			UploadId: created.UploadId,
		})
		err = errors.Join(err, abortErr)
	}()

	numParts := (size + partSize - 1) / partSize
	parts := make([]types.CompletedPart, numParts)
	partCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	partNums := make(chan int64)
	var wg sync.WaitGroup
	for range s.concurrency {
		wg.Go(func() {
			for i := range partNums {
				start := i * partSize
				end := min(start+partSize, size) - 1
				partNum := aws.Int32(int32(i + 1)) // #nosec G115 -- at most maxUploadParts
				out, err := client.UploadPartCopy(partCtx, &s3.UploadPartCopyInput{
					Bucket:            &s.bucketName,
					Key:               &destKey,
					UploadId:          created.UploadId,
					PartNumber:        partNum,
					CopySource:        aws.String(s.copySource(srcKey)),
					CopySourceRange:   aws.String(fmt.Sprintf("bytes=%d-%d", start, end)),
					CopySourceIfMatch: head.ETag,
				})
				if err != nil {
					cancel(err)
					continue
				}
				parts[i] = completedPart(partNum, out.CopyPartResult)
			}
		})
	}
feed:
	for i := range numParts {
		select {
		case partNums <- i:
		case <-partCtx.Done():
			break feed
		}
	}
	close(partNums)
	wg.Wait()
	if err := context.Cause(partCtx); err != nil {
		return err
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          &s.bucketName,
		Key:             &destKey,
		UploadId:        created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return err
}

// completedPart returns the CompletedPart of a copied part
// with the checksums that CompleteMultipartUpload needs
// if the client calculated them.
func completedPart(partNum *int32, result *types.CopyPartResult) types.CompletedPart {
	if result == nil {
		return types.CompletedPart{PartNumber: partNum}
	}
	return types.CompletedPart{
		PartNumber:        partNum,
		ETag:              result.ETag,
		ChecksumCRC32:     result.ChecksumCRC32,
		ChecksumCRC32C:    result.ChecksumCRC32C,
		ChecksumCRC64NVME: result.ChecksumCRC64NVME,
		ChecksumMD5:       result.ChecksumMD5,
		ChecksumSHA1:      result.ChecksumSHA1,
		ChecksumSHA256:    result.ChecksumSHA256,
		ChecksumSHA512:    result.ChecksumSHA512,
		ChecksumXXHASH128: result.ChecksumXXHASH128,
		ChecksumXXHASH3:   result.ChecksumXXHASH3,
		ChecksumXXHASH64:  result.ChecksumXXHASH64,
	}
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards, also the Close of writers
// that were opened before. Close is idempotent.
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
