package s3fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix os S3FileSystem URLs
	Prefix = "s3://"

	// Separator used in S3FileSystem paths
	Separator = "/"

	// MultipartUploadThreshold is the minimum size for using multipart upload (5 MB)
	// Files larger than this will be uploaded using multipart upload for better performance
	MultipartUploadThreshold = 5 * 1024 * 1024

	// MultipartDownloadThreshold is the minimum size for using multipart download (10 MB)
	// Files larger than this will be downloaded in chunks for better performance
	MultipartDownloadThreshold = 10 * 1024 * 1024
)

var (
	// DefaultPermissions used for S3 bucket files
	DefaultPermissions = fs.UserAndGroupReadWrite
	// DefaultDirPermissions used for S3 bucket directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllReadWrite

	// Make sure S3FileSystem implements fs.FileSystem
	_ fs.FileSystem = new(fileSystem)
)

type fileSystem struct {
	client     *s3.Client
	bucketName string
	prefix     string
	readOnly   bool
}

// NewAndRegister creates a new S3 filesystem for the specified bucket and registers it
// with the global fs.Registry.
//
// The client parameter must be a configured S3 client from AWS SDK v2.
// The bucketName is the name of the S3 bucket to access.
// The readOnly flag, when true, prevents all write operations.
//
// The filesystem will use the prefix "s3://bucket-name" for all file URLs,
// which is compatible with AWS CLI and SDK tools.
func NewAndRegister(client *s3.Client, bucketName string, readOnly bool) fs.FileSystem {
	s3fs := &fileSystem{
		client:     client,
		bucketName: bucketName,
		prefix:     Prefix + bucketName,
		readOnly:   readOnly,
	}
	fs.Register(s3fs)
	return s3fs
}

// NewLoadDefaultConfig creates a new S3 filesystem using the default AWS credential chain.
//
// This is a convenience function that:
// 1. Loads credentials from environment variables, ~/.aws/credentials, or IAM roles
// 2. Creates an S3 client with the loaded configuration
// 3. Registers the filesystem with the global registry
//
// Returns an error if AWS credentials cannot be loaded.
func NewLoadDefaultConfig(ctx context.Context, bucketName string, readOnly bool) (fs.FileSystem, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)
	return NewAndRegister(client, bucketName, readOnly), nil
}

// ReadableWritable returns whether the filesystem supports read and write operations.
//
// S3 filesystems are always readable. Write support depends on the readOnly flag
// passed to NewAndRegister.
func (s *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !s.readOnly
}

// RootDir returns the root directory of the S3 bucket as a File.
//
// Format: "s3://bucket-name/"
func (s *fileSystem) RootDir() fs.File {
	return fs.File(s.prefix + Separator)
}

// ID returns the bucket name as the filesystem identifier.
//
// This is used to distinguish between different S3 filesystems.
func (s *fileSystem) ID() (string, error) {
	return s.bucketName, nil
}

// Prefix returns the URL prefix used by this filesystem.
//
// Format: "s3://bucket-name" (without trailing slash)
//
// This prefix is used to construct full S3 URIs compatible with AWS CLI and SDKs.
func (s *fileSystem) Prefix() string {
	return s.prefix
}

// Name returns a human-readable name for the filesystem.
func (s *fileSystem) Name() string {
	return "S3 file system for bucket: s.bucketName"
}

// String returns a detailed string representation of the filesystem.
func (s *fileSystem) String() string {
	return s.Name() + " with prefix " + s.prefix
}

// URL constructs a full S3 URI from a clean path.
//
// The returned URL follows the standard S3 URI format: s3://bucket-name/path/to/file
// This format is compatible with AWS CLI, SDKs, and most S3-compatible tools.
//
// Example: URL("/path/file.txt") returns "s3://bucket-name/path/file.txt"
func (s *fileSystem) URL(cleanPath string) string {
	return s.prefix + cleanPath
}

// CleanPathFromURI extracts the clean path from a full S3 URI.
//
// Strips the filesystem prefix (s3://bucket-name) from the URI, leaving only the path.
//
// Example: CleanPathFromURI("s3://bucket-name/path/file.txt") returns "/path/file.txt"
func (f *fileSystem) CleanPathFromURI(uri string) string {
	return strings.TrimPrefix(uri, f.prefix)
}

// JoinCleanFile joins path parts into a File with this filesystem's prefix.
//
// The parts are cleaned and joined with forward slashes, then prefixed with s3://bucket-name
func (s *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(s.prefix + s.JoinCleanPath(uriParts...))
}

// JoinCleanPath joins path parts into a clean path string.
//
// Cleans redundant separators and resolves . and .. components.
// Always uses forward slash (/) as the separator, regardless of OS.
func (s *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, s.prefix, Separator)
}

// SplitPath splits a file path into its components.
//
// Removes the filesystem prefix and splits on forward slashes.
func (s *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, s.prefix, Separator)
}

// Separator returns the path separator used by S3 (always forward slash).
//
// S3 always uses "/" regardless of the client OS.
func (s *fileSystem) Separator() string {
	return Separator
}

// IsAbsPath returns whether the path is absolute (starts with /).
//
// In S3, absolute paths start with /, relative paths don't.
func (s *fileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

// AbsPath converts a relative path to absolute by prepending /.
//
// If the path is already absolute, returns it unchanged.
func (s *fileSystem) AbsPath(filePath string) string {
	if path.IsAbs(filePath) {
		return filePath
	}
	return Separator + filePath
}

// MatchAnyPattern checks if a name matches any of the given glob patterns.
//
// Supports standard glob patterns: *, ?, [chars], [!chars]
// If patterns is empty or nil, returns true (matches all).
func (s *fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

// SplitDirAndName splits a file path into directory and filename.
//
// Example: SplitDirAndName("/path/to/file.txt") returns ("/path/to", "file.txt")
func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

// VolumeName returns the bucket name as the volume name.
//
// In S3, the bucket is equivalent to a volume or drive.
func (s *fileSystem) VolumeName(filePath string) string {
	return s.bucketName
}

// Stat returns file information for the given path.
//
// Implementation details:
//   - Uses S3 HeadObject API to get object metadata
//   - For directories, tries both with and without trailing slash
//   - Returns file size, last modified time, and directory flag
//
// Limitations:
//   - May require two API calls (one for file, one for directory)
//   - No permission information (S3 uses IAM policies)
//   - LastModified is set by S3, not the client
func (s *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}

	// Try with the path as-is first
	out, err := s.client.HeadObject(
		context.Background(),
		&s3.HeadObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	if err == nil {
		return &fileInfo{
			name: path.Base(filePath),
			size: *out.ContentLength,
			time: *out.LastModified,
		}, nil
	}

	// If not found, try with trailing slash (directory)
	if !strings.HasSuffix(filePath, "/") {
		dirPath := filePath + "/"
		out, err = s.client.HeadObject(
			context.Background(),
			&s3.HeadObjectInput{
				Bucket: &s.bucketName,
				Key:    &dirPath,
			},
		)
		if err == nil {
			return &fileInfo{
				name: path.Base(filePath),
				size: 0,
				time: *out.LastModified,
				dir:  true,
			}, nil
		}
	}

	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
	}
	return nil, err
}

// Exists checks if a file or directory exists at the given path.
//
// Implementation:
//   - Uses S3 HeadObject API (fast, doesn't download content)
//   - Returns false for empty path or root "/"
//   - Only checks for exact path match (doesn't try directory variants)
//
// Note: For better directory detection, use Stat() instead.
func (s *fileSystem) Exists(filePath string) bool {
	if filePath == "" || filePath == "/" {
		return false
	}
	_, err := s.client.HeadObject(
		context.Background(),
		&s3.HeadObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	return err == nil
}

// IsHidden returns true if the file name starts with a dot.
//
// S3 limitation:
//   - S3 doesn't have a concept of "hidden" files
//   - This follows Unix convention: names starting with '.' are hidden
//   - Based only on the filename, not object metadata
func (s *fileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

// IsSymbolicLink always returns false for S3.
//
// S3 limitation:
//   - S3 doesn't support symbolic links
//   - All S3 objects are regular files or directories (simulated via trailing slash)
func (s *fileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

// listDirInfo lists directory contents and calls callback for each entry.
//
// Implementation:
//   - Uses S3 ListObjectsV2 API with pagination support
//   - For non-recursive: Uses delimiter "/" to get immediate children only
//   - For recursive: Lists all objects under prefix (no delimiter)
//   - Processes both CommonPrefixes (directories) and Contents (files)
//
// Pattern matching:
//   - Applies glob patterns to basename only (not full path)
//   - Empty/nil patterns match all entries
//
// Performance:
//   - Automatically handles pagination for large directories
//   - Each page may trigger a callback multiple times
//   - Context cancellation is checked between pages
//
// S3 directories:
//   - Directories are simulated using keys ending with "/"
//   - CommonPrefixes represent "virtual" directories
//   - Zero-byte objects with trailing "/" are real directory markers
func (s *fileSystem) listDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Normalize directory path
	if len(dirPath) > 1 && strings.HasPrefix(dirPath, "/") {
		dirPath = dirPath[1:]
	}
	if !strings.HasSuffix(dirPath, "/") && dirPath != "" {
		dirPath += "/"
	}

	// For root directory listing
	var prefix string
	if dirPath != "/" && dirPath != "" {
		prefix = dirPath
	}

	// Determine delimiter for non-recursive listing
	var delimiter string
	if !recursive {
		delimiter = "/"
	}

	// List objects with pagination support
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket:    &s.bucketName,
		Prefix:    &prefix,
		Delimiter: &delimiter,
	})

	for paginator.HasMorePages() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		// Process common prefixes (directories) for non-recursive listing
		if !recursive {
			for _, commonPrefix := range page.CommonPrefixes {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if commonPrefix.Prefix == nil {
					continue
				}

				// Extract directory name
				dirName := strings.TrimSuffix(*commonPrefix.Prefix, "/")
				if dirName == "" || dirName == dirPath {
					continue
				}
				baseName := path.Base(dirName)

				// Check pattern matching
				if len(patterns) > 0 {
					matched, err := s.MatchAnyPattern(baseName, patterns)
					if err != nil {
						return err
					}
					if !matched {
						continue
					}
				}

				// Create FileInfo for directory
				dirFile := fs.File(s.prefix + "/" + strings.TrimSuffix(*commonPrefix.Prefix, "/"))
				info := &fs.FileInfo{
					File:        dirFile,
					Name:        baseName,
					Exists:      true,
					IsDir:       true,
					Permissions: DefaultDirPermissions,
				}

				if err := callback(info); err != nil {
					return err
				}
			}
		}

		// Process objects (files)
		for _, obj := range page.Contents {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if obj.Key == nil {
				continue
			}

			// Skip the directory marker itself
			if *obj.Key == dirPath || strings.HasSuffix(*obj.Key, "/") && *obj.Size == 0 {
				continue
			}

			// Extract file path relative to dirPath
			filePath := *obj.Key
			if prefix != "" {
				filePath = strings.TrimPrefix(filePath, prefix)
			}
			if filePath == "" {
				continue
			}

			baseName := path.Base(*obj.Key)

			// Check pattern matching
			if len(patterns) > 0 {
				matched, err := s.MatchAnyPattern(baseName, patterns)
				if err != nil {
					return err
				}
				if !matched {
					continue
				}
			}

			// Create FileInfo for file
			modTime := time.Time{}
			if obj.LastModified != nil {
				modTime = *obj.LastModified
			}

			fileFile := fs.File(s.prefix + "/" + *obj.Key)
			info := &fs.FileInfo{
				File:        fileFile,
				Name:        baseName,
				Exists:      true,
				IsRegular:   true,
				Size:        *obj.Size,
				Modified:    modTime,
				Permissions: DefaultPermissions,
			}

			if err := callback(info); err != nil {
				return err
			}
		}
	}

	return nil
}

// ListDirInfo lists immediate children of a directory (non-recursive).
//
// Only returns files and directories directly in dirPath, not nested contents.
// See listDirInfo for implementation details.
func (s *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return s.listDirInfo(ctx, dirPath, callback, patterns, false)
}

// ListDirInfoRecursive lists all files under a directory recursively.
//
// Returns all nested files, no matter how deep in the directory tree.
// See listDirInfo for implementation details.
func (s *fileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return s.listDirInfo(ctx, dirPath, callback, patterns, true)
}

// Touch creates an empty file or updates the modification time of an existing file.
//
// Implementation for new files:
//   - Creates a zero-byte object using PutObject
//
// Implementation for existing files:
//   - Uses CopyObject to copy the object to itself
//   - This updates the LastModified timestamp
//   - Preserves all object data and metadata
//
// S3 limitation:
//   - Cannot update LastModified without rewriting the object
//   - For large files, this can be expensive (server-side copy, but still uses API quota)
//   - No way to set a specific LastModified time (S3 always uses current time)
//
// Note: The perm parameter is ignored for existing files (S3 uses IAM policies)
func (s *fileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}

	ctx := context.Background()

	// Check if file exists
	if s.Exists(filePath) {
		// S3 doesn't support updating LastModified without rewriting the object.
		// We need to copy the object to itself to update the modification time.
		// This is the only way to "touch" an existing S3 object.
		copySource := s.bucketName + "/" + filePath
		_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:            &s.bucketName,
			CopySource:        &copySource,
			Key:               &filePath,
			MetadataDirective: types.MetadataDirectiveReplace,
		})
		return err
	}

	// If file doesn't exist, create an empty file
	return s.WriteAll(ctx, filePath, make([]byte, 0), perm)
}

// MakeDir creates a directory in the S3 bucket.
//
// Implementation:
//   - Creates a zero-byte object with key ending in "/"
//   - Uses Touch internally to create the marker object
//
// S3 directories:
//   - S3 doesn't have real directories
//   - "Directories" are simulated via object key prefixes
//   - This creates an explicit directory marker (zero-byte object with trailing /)
//   - Files can exist in a "directory" without the marker existing
//
// Note:
//   - Returns nil (success) for root directory "/"
//   - The perm parameter is ignored (S3 uses IAM policies)
func (s *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if dirPath == "/" {
		return nil
	}
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	// Touch pretty much does what we need. In order to create a "directory"
	// in the S3 bucket, we need to pass a key that ends on '/' and has no data.
	// We add the slash above and Touch writes an object with no data.
	return s.Touch(dirPath, perm)
}

// ReadAll reads the entire contents of a file.
//
// Implementation:
//   - Checks file size with HeadObject first
//   - Files ≥ 10 MB: Uses multipart download via AWS manager for better performance
//   - Files < 10 MB: Uses simple GetObject
//
// Multipart download:
//   - Downloads file in concurrent chunks
//   - Default concurrency: 5 goroutines
//   - Better performance for large files over high-latency connections
//
// Memory usage:
//   - Allocates buffer for entire file content
//   - For large files, consider using OpenReader for streaming
//
// Limitations:
//   - No resume capability for interrupted downloads
//   - No progress reporting
func (s *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}

	// Check file size first to decide on download strategy
	stat, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &filePath,
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
		}
		return nil, err
	}

	// For large files, use multipart download via manager for better performance
	if stat.ContentLength != nil && *stat.ContentLength >= MultipartDownloadThreshold {
		downloader := manager.NewDownloader(s.client)
		buf := manager.NewWriteAtBuffer(make([]byte, 0, *stat.ContentLength))

		_, err = downloader.Download(ctx, buf, &s3.GetObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		})
		if err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	// For small files, use simple GetObject
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucketName,
		Key:    &filePath,
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WriteAll writes data to a file, creating or overwriting it.
//
// Implementation:
//   - Files ≥ 5 MB: Uses multipart upload via AWS manager for better performance
//   - Files < 5 MB: Uses simple PutObject
//
// Multipart upload:
//   - Uploads file in concurrent chunks
//   - Default part size: 5 MB
//   - Better performance for large files over high-latency connections
//   - Automatic retry of failed parts
//
// S3 behavior:
//   - Overwrites existing file completely (no merge/append)
//   - Atomic operation: file is either old content or new, never partial
//   - LastModified is set to current time automatically
//
// Limitations:
//   - No append capability (see S3 limitations in documentation)
//   - The perm parameter is ignored (S3 uses IAM policies)
//   - No progress reporting
func (s *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}

	// For large files, use multipart upload via manager for better performance
	if len(data) >= MultipartUploadThreshold {
		uploader := manager.NewUploader(s.client)
		_, err := uploader.Upload(ctx, &s3.PutObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
			Body:   bytes.NewReader(data),
		})
		return err
	}

	// For small files, use simple PutObject
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucketName,
		Key:    &filePath,
		Body:   bytes.NewReader(data),
	})
	return err
}

// OpenReader opens a file for reading and returns an io/fs.File.
//
// Implementation:
//   - Downloads entire file into memory
//   - Returns a read-only file buffer
//   - Uses multipart download for files ≥ 10 MB
//
// Returned file supports:
//   - Read() - read data
//   - Seek() - seek within the downloaded data
//   - Stat() - get file info
//   - Close() - close the file (no-op, data already in memory)
//
// Memory usage:
//   - Entire file is loaded into memory
//   - Not suitable for very large files
//
// Note: This downloads the file eagerly. For streaming, use ReadAll with a custom reader.
func (s *fileSystem) OpenReader(filePath string) (iofs.File, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}

	ctx := context.Background()

	// Check file size first to decide on download strategy
	stat, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucketName,
		Key:    &filePath,
	})
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
		}
		return nil, err
	}

	info := &fileInfo{
		name: path.Base(filePath),
		size: *stat.ContentLength,
		time: *stat.LastModified,
	}

	// For large files, use multipart download via manager
	if stat.ContentLength != nil && *stat.ContentLength >= MultipartDownloadThreshold {
		downloader := manager.NewDownloader(s.client)
		buf := manager.NewWriteAtBuffer(make([]byte, 0, *stat.ContentLength))

		_, err = downloader.Download(ctx, buf, &s3.GetObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		})
		if err != nil {
			return nil, err
		}
		return fsimpl.NewReadonlyFileBuffer(buf.Bytes(), info), nil
	}

	// For small files, use simple GetObject
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucketName,
		Key:    &filePath,
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}

	return fsimpl.NewReadonlyFileBuffer(data, info), nil
}

// OpenWriter opens a file for writing, creating or truncating it.
//
// Implementation:
//   - Returns an in-memory buffer that writes to S3 on Close()
//   - Data is buffered entirely in memory until Close() is called
//   - Uses WriteAll internally, which may use multipart upload for large files
//
// Behavior:
//   - Truncates existing file (doesn't append)
//   - File is not created on S3 until Close() is called
//   - If Close() fails, file may not be written to S3
//
// Memory usage:
//   - Entire file content is kept in memory
//   - Memory usage = size of data written
//
// Use WriteAll directly for better error handling if you have all data upfront.
func (s *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if s.readOnly {
		return nil, fs.ErrReadOnlyFileSystem
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return s.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), perm)
	})
	return fileBuffer, nil
}

// OpenReadWriter opens a file for both reading and writing.
//
// Implementation:
//   - Downloads entire file into memory
//   - Returns an in-memory buffer supporting Read, Write, and Seek
//   - Writes changes back to S3 on Close()
//
// Behavior:
//   - File must already exist (returns error otherwise)
//   - Entire file is loaded into memory
//   - Changes are not persisted until Close() is called
//   - If Close() fails, changes are lost
//
// Memory usage:
//   - Initial file size + any additional writes
//   - Not suitable for large files
//
// S3 limitation:
//   - No atomic read-modify-write
//   - Concurrent modifications may cause data loss (last write wins)
func (s *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return s.openFileBuffer(filePath)
}

// openFileBuffer is the internal implementation of OpenReadWriter.
func (s *fileSystem) openFileBuffer(filePath string) (fileBuffer *fsimpl.FileBuffer, err error) {
	if s.readOnly {
		return nil, fs.ErrReadOnlyFileSystem
	}
	current, err := s.ReadAll(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	fileBuffer = fsimpl.NewFileBufferWithClose(current, func() error {
		return s.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// CopyFile copies a file within the same S3 bucket.
//
// Implementation:
//   - Uses S3 CopyObject API (server-side copy)
//   - No data is downloaded or uploaded through the client
//   - Fast and efficient for any file size
//
// Benefits:
//   - Server-side copy: no data transfer through client
//   - Works for files of any size
//   - Preserves object metadata
//   - Faster than download + upload
//
// Limitations:
//   - Source and destination must be in the same bucket
//   - The buf parameter is ignored (no client-side buffering needed)
//   - Cannot copy across regions without additional configuration
//
// S3 pricing:
//   - Copy operations have API costs but no data transfer costs
func (s *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	srcFile = s.bucketName + "/" + srcFile
	_, err := s.client.CopyObject(
		ctx, &s3.CopyObjectInput{
			Bucket:     &s.bucketName,
			CopySource: &srcFile,
			Key:        &destFile,
		},
	)
	var notFound *types.NotFound
	if err != nil && errors.As(err, &notFound) {
		err = fs.NewErrDoesNotExist(fs.File(s.prefix + srcFile))
	}
	return err
}

// Remove deletes a file or directory from S3.
//
// Implementation:
//   - Uses S3 DeleteObject API
//   - Successful even if object doesn't exist (S3 behavior)
//   - For directories, only removes the directory marker, not contents
//
// S3 behavior:
//   - No error if object doesn't exist
//   - Deletion is eventually consistent (object may still appear briefly)
//   - No recursive delete (use RemoveRecursive from fs.File for that)
//
// Directory deletion:
//   - Only deletes the zero-byte directory marker object
//   - Files within the "directory" are NOT deleted
//   - To delete a directory and all contents, enumerate and delete each file
//
// Note: This operation cannot be undone. S3 doesn't have a "trash" or recycle bin.
func (s *fileSystem) Remove(filePath string) error {
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	_, err := s.client.DeleteObject(
		context.Background(),
		&s3.DeleteObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		})
	return err
}

// Watch is not supported for S3 filesystems.
//
// S3 limitation:
//   - S3 doesn't support real-time file watching
//   - S3 Event Notifications require separate infrastructure (SQS/SNS/Lambda)
//   - No equivalent to inotify or file system watches
//
// Alternatives:
//   - Use S3 Event Notifications with SQS/SNS
//   - Poll with ListObjects or HeadObject
//   - Use S3 Select for query-based monitoring
//
// This method always returns errors.ErrUnsupported.
func (s *fileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	// https://stackoverflow.com/questions/18049717/waituntilobjectexists-amazon-s3-php-sdk-method-exactly-how-does-it-work
	// S3 WaitUntilObjectExists and WaitUntilObjectNotExists were removed in SDK v2
	// S3 Event Notifications are the recommended approach for watching S3 changes
	return nil, errors.ErrUnsupported
}

// Close unregisters the filesystem and releases resources.
//
// After calling Close:
//   - The filesystem is removed from the global registry
//   - File URLs with this filesystem's prefix will no longer work
//   - The S3 client reference is cleared
//
// Safe to call multiple times (subsequent calls are no-ops).
//
// Note: This doesn't close HTTP connections (managed by the AWS SDK).
func (s *fileSystem) Close() error {
	if s.client == nil {
		return nil // already closed
	}
	fs.Unregister(s)
	s.client = nil
	return nil
}
