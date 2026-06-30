// Package dropboxfs provides a filesystem implementation for Dropbox using the Dropbox API.
//
// Configuration Options:
//   - mute: Controls whether file modifications trigger user notifications.
//     When set to true, users won't be notified of file changes made through this filesystem.
//     This is useful for automated operations where you don't want to spam users with notifications.
//     The mute setting is applied to all file upload operations (WriteAll, OpenWriter, etc.).
package dropboxfs

import (
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"path"
	"strings"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/users"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of DropboxFileSystem URLs
	Prefix = "dropbox://"
	// Separator used in DropboxFileSystem paths
	Separator = "/"
)

var (
	// DefaultPermissions used for Dropbox files
	DefaultPermissions = fs.UserAndGroupReadWrite
	// DefaultDirPermissions used for Dropbox directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllExecute

	// Make sure DropboxFileSystem implements fs.FileSystem
	_ fs.FileSystem = new(fileSystem)
)

// fileSystem implements fs.FileSystem for a Dropbox app.
type fileSystem struct {
	id            string
	prefix        string
	config        dropbox.Config
	filesClient   files.Client
	usersClient   users.Client
	fileInfoCache *fs.FileInfoCache
	mute          bool // If true, file modifications won't trigger user notifications
	closed        bool // Set by Close, guards against using a closed file system
}

// NewAndRegister returns a new fs.FileSystem for a Dropbox with
// the passed accessToken and registers it.
//
// The mute parameter controls whether file modifications trigger user notifications.
// If mute is true, users won't be notified of file changes made through this filesystem.
func NewAndRegister(accessToken string, cacheTimeout time.Duration, mute bool) fs.FileSystem {
	config := dropbox.Config{
		Token:    accessToken,
		LogLevel: dropbox.LogOff,
	}

	dbfs := &fileSystem{
		prefix:        Prefix + fsimpl.RandomString(),
		config:        config,
		filesClient:   files.New(config),
		usersClient:   users.New(config),
		fileInfoCache: fs.NewFileInfoCache(cacheTimeout),
		mute:          mute,
	}
	fs.Register(dbfs)
	return dbfs
}

// isNotExistError reports whether err is a Dropbox API error that means a
// file or folder does not exist, as opposed to a transport or authorization
// error (network outage, rate limit, 5xx, expired token, ...).
//
// This distinction is critical: treating a transient error as "does not
// exist" would make existing files appear to vanish, which is dangerous for
// callers that check existence before overwriting or deleting.
func isNotExistError(err error) bool {
	if err == nil {
		return false
	}
	// Typed detection for the get_metadata route used by info():
	// only a LookupError with the not_found tag means the path is missing.
	var metaErr files.GetMetadataAPIError
	if errors.As(err, &metaErr) {
		return metaErr.EndpointError != nil &&
			metaErr.EndpointError.Path != nil &&
			metaErr.EndpointError.Path.Tag == files.LookupErrorNotFound
	}
	// Fallback to matching the Dropbox error_summary string for the other
	// routes (download, upload, move, copy, delete) whose typed errors all
	// embed a LookupError that stringifies as ".../not_found/...". Transport
	// and authorization errors do not contain "not_found".
	errMsg := err.Error()
	return strings.Contains(errMsg, "path/not_found") || strings.Contains(errMsg, "not_found")
}

// wrapErrNotExist converts Dropbox API "not_found" errors to fs.ErrDoesNotExist.
// All other errors (including transport and authorization errors) are returned
// unchanged so that callers do not mistake them for a missing file.
func (dbfs *fileSystem) wrapErrNotExist(filePath string, err error) error {
	if isNotExistError(err) {
		return fs.NewErrDoesNotExist(dbfs.File(filePath))
	}
	return err
}

// checkClosed returns fs.ErrFileSystemClosed if the file system has been
// closed, else nil. It is called at the start of every method that uses the
// Dropbox API clients to avoid operating on a closed file system.
func (dbfs *fileSystem) checkClosed() error {
	if dbfs.closed {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// ReadableWritable returns true for both readable and writable operations.
// Dropbox filesystems support both reading and writing files through the API.
func (dbfs *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

// RootDir returns the root directory of the Dropbox filesystem.
// This represents the root of the user's Dropbox account, not the API root.
func (dbfs *fileSystem) RootDir() fs.File {
	return fs.File(dbfs.prefix + Separator)
}

// ID returns the Dropbox account ID for this filesystem.
// The account ID is fetched from the Dropbox API on first call and cached.
// This requires a valid access token and network connectivity.
func (dbfs *fileSystem) ID() (string, error) {
	if err := dbfs.checkClosed(); err != nil {
		return "", err
	}
	if dbfs.id == "" {
		account, err := dbfs.usersClient.GetCurrentAccount()
		if err != nil {
			return "", err
		}
		dbfs.id = account.AccountId
	}
	return dbfs.id, nil
}

// Prefix returns the URI prefix for this Dropbox filesystem.
// This is used to identify files belonging to this filesystem instance.
func (dbfs *fileSystem) Prefix() string {
	return dbfs.prefix
}

// Name returns the human-readable name of this filesystem.
// Always returns "Dropbox file system" for Dropbox filesystems.
func (dbfs *fileSystem) Name() string {
	return "Dropbox file system"
}

// String implements the fmt.Stringer interface.
// Returns a descriptive string including the filesystem name and prefix.
func (dbfs *fileSystem) String() string {
	return dbfs.Name() + " with prefix " + dbfs.Prefix()
}

// File creates a File from a path string.
// The path is cleaned and joined with the filesystem prefix.
func (dbfs *fileSystem) File(filePath string) fs.File {
	return dbfs.JoinCleanFile(filePath)
}

// JoinCleanFile joins multiple path parts into a clean File path.
// All parts are cleaned and joined with the filesystem prefix.
func (dbfs *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(dbfs.prefix + dbfs.JoinCleanPath(uriParts...))
}

// URL creates a full URI from a clean path.
// Combines the filesystem prefix with the provided path.
func (dbfs *fileSystem) URL(cleanPath string) string {
	return dbfs.prefix + cleanPath
}

// CleanPathFromURI extracts the clean path from a full URI.
// Removes the filesystem prefix to get the actual Dropbox path.
func (dbfs *fileSystem) CleanPathFromURI(uri string) string {
	return strings.TrimPrefix(uri, dbfs.prefix)
}

// JoinCleanPath joins multiple path parts into a clean path string.
// Uses "/" as the separator, which is standard for Dropbox paths.
func (dbfs *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, dbfs.prefix)
}

// SplitPath splits a file path into its component parts.
// Uses "/" as the separator for Dropbox paths.
func (dbfs *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, dbfs.prefix, Separator)
}

// Separator returns the path separator used by Dropbox.
// Always returns "/" as Dropbox uses Unix-style paths.
func (dbfs *fileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern checks if a filename matches any of the given patterns.
// Uses standard shell-style pattern matching (e.g., "*.txt", "file.*").
func (*fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

// SplitDirAndName splits a file path into directory and filename components.
// Uses "/" as the separator for Dropbox paths.
func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

// IsAbsPath checks if a path is absolute.
// Uses Go's standard path.IsAbs which considers paths starting with "/" as absolute.
func (dbfs *fileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

// AbsPath converts a relative path to an absolute path.
// Prepends "/" to relative paths and cleans the result.
func (dbfs *fileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

// metadataToFileInfo converts Dropbox metadata to fs.FileInfo.
// Handles FileMetadata, FolderMetadata, and DeletedMetadata types.
// Files starting with "." are considered hidden, following Unix conventions.
func metadataToFileInfo(meta files.IsMetadata) *fs.FileInfo {
	var info fs.FileInfo

	switch m := meta.(type) {
	case *files.FileMetadata:
		info.Name = m.Name
		info.Exists = true
		info.IsRegular = true
		info.IsDir = false
		info.IsHidden = len(m.Name) > 0 && m.Name[0] == '.'
		info.Size = int64(m.Size) //#nosec G115 -- int64 limit will not be exceeded in real world use cases
		info.Modified = m.ServerModified
		info.Permissions = DefaultPermissions
	case *files.FolderMetadata:
		info.Name = m.Name
		info.Exists = true
		info.IsRegular = false
		info.IsDir = true
		info.IsHidden = len(m.Name) > 0 && m.Name[0] == '.'
		info.Size = 0
		info.Permissions = DefaultDirPermissions
	case *files.DeletedMetadata:
		info.Name = m.Name
		info.Exists = false
	default:
		// Unknown metadata type
		info.Exists = false
	}

	return &info
}

// info returns FileInfo for a given path.
// Uses caching to avoid repeated API calls for the same path.
// The root folder ("/" or "") is handled specially as it's not supported by the Dropbox API.
//
// A returned error means the existence of filePath could not be determined
// (transport error, rate limit, 5xx, expired token, ...). A genuinely missing
// path is reported as a FileInfo with Exists==false and a nil error, so that
// transient failures are never mistaken for a non-existent file.
func (dbfs *fileSystem) info(filePath string) (*fs.FileInfo, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}

	// The root folder is unsupported by the API
	if filePath == "" || filePath == "/" {
		return &fs.FileInfo{
			Name:        "",
			Exists:      true,
			IsRegular:   false,
			IsDir:       true,
			Permissions: DefaultDirPermissions,
		}, nil
	}

	if cachedInfo, ok := dbfs.fileInfoCache.Get(filePath); ok {
		return cachedInfo, nil
	}

	arg := files.NewGetMetadataArg(filePath)
	meta, err := dbfs.filesClient.GetMetadata(arg)
	if err != nil {
		dbfs.fileInfoCache.Delete(filePath)
		if isNotExistError(err) {
			// The path genuinely does not exist.
			return new(fs.FileInfo), nil
		}
		// A transient error: existence is unknown, propagate the error
		// instead of pretending the file does not exist.
		return nil, err
	}

	info := metadataToFileInfo(meta)
	if dbfs.fileInfoCache != nil {
		dbfs.fileInfoCache.Put(filePath, info)
	}
	return info, nil
}

// Stat returns file information for a given path.
// Returns fs.ErrDoesNotExist if the file or directory doesn't exist.
// Any other error (transport, rate limit, authorization, ...) is returned
// unchanged instead of being reported as a missing file.
func (dbfs *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info, err := dbfs.info(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

// Exists checks if a file or directory exists.
// Uses cached information when available to avoid API calls.
// If existence cannot be determined (transport error, rate limit, ...),
// Exists conservatively returns false because the FileSystem interface does
// not allow returning an error here; use Stat to observe such errors.
func (dbfs *fileSystem) Exists(filePath string) bool {
	info, err := dbfs.info(filePath)
	return err == nil && info.Exists
}

// IsHidden checks if a file is hidden.
// Files starting with "." are considered hidden, following Unix conventions.
func (dbfs *fileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

// IsSymbolicLink always returns false.
// Dropbox does not support symbolic links.
func (dbfs *fileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

// listDirInfo lists directory contents with optional recursion and pattern matching.
// Uses the Dropbox ListFolder API with pagination support.
// The root directory ("/") is converted to empty string for the API.
// Results are cached for performance and callback is called for each matching entry.
func (dbfs *fileSystem) listDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	info, err := dbfs.info(dirPath)
	if err != nil {
		return err
	}
	if !info.Exists {
		return fs.NewErrDoesNotExist(dbfs.File(dirPath))
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(dbfs.File(dirPath))
	}

	// Empty string for root
	if dirPath == "/" {
		dirPath = ""
	}

	arg := files.NewListFolderArg(dirPath)
	arg.Recursive = recursive

	result, err := dbfs.filesClient.ListFolder(arg)
	if err != nil {
		return dbfs.wrapErrNotExist(dirPath, err)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		for _, entry := range result.Entries {
			meta := metadataToFileInfo(entry)
			if meta.Exists {
				match, err := fsimpl.MatchAnyPattern(meta.Name, patterns)
				if err != nil {
					return err
				}
				if match {
					if dbfs.fileInfoCache != nil {
						// Extract path from metadata
						var fullPath string
						switch m := entry.(type) {
						case *files.FileMetadata:
							fullPath = m.PathDisplay
						case *files.FolderMetadata:
							fullPath = m.PathDisplay
						}
						if fullPath != "" {
							dbfs.fileInfoCache.Put(fullPath, meta)
						}
					}

					err = callback(meta)
					if err != nil {
						return err
					}
				}
			}
		}

		if !result.HasMore {
			break
		}

		// Continue with cursor
		continueArg := files.NewListFolderContinueArg(result.Cursor)
		result, err = dbfs.filesClient.ListFolderContinue(continueArg)
		if err != nil {
			return err
		}
	}

	return nil
}

// ListDirInfo lists directory contents non-recursively.
// Calls the callback function for each file and directory in the specified directory.
func (dbfs *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, false)
}

// ListDirInfoRecursive lists directory contents recursively.
// Calls the callback function for each file and directory in the specified directory and all subdirectories.
func (dbfs *fileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, true)
}

// Touch creates an empty file if it doesn't exist.
// Note: Dropbox doesn't support updating modification times, so Touch only creates new files.
// Returns an error if the file already exists.
func (dbfs *fileSystem) Touch(filePath string, perm []fs.Permissions) error {
	info, err := dbfs.info(filePath)
	if err != nil {
		return err
	}
	if info.Exists {
		return errors.New("Touch can't change time on Dropbox")
	}
	return dbfs.WriteAll(context.Background(), filePath, nil, perm)
}

// MakeDir creates a directory at the specified path.
// Uses the Dropbox CreateFolderV2 API to create directories.
func (dbfs *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	arg := files.NewCreateFolderArg(dirPath)
	_, err := dbfs.filesClient.CreateFolderV2(arg)
	return dbfs.wrapErrNotExist(dirPath, err)
}

// ReadAll reads the entire contents of a file from Dropbox.
// Uses the Dropbox Download API to fetch file contents.
// The file is downloaded as a stream and read into memory.
func (dbfs *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	arg := files.NewDownloadArg(filePath)
	_, body, err := dbfs.filesClient.Download(arg)
	if err != nil {
		return nil, dbfs.wrapErrNotExist(filePath, err)
	}
	defer body.Close()

	return fs.ReadAllContext(ctx, body)
}

// WriteAll writes data to a file in Dropbox.
// The mute configuration from the filesystem is applied to control user notifications.
func (dbfs *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	arg := files.NewUploadArg(filePath)
	arg.Mode = &files.WriteMode{Tagged: dropbox.Tagged{Tag: "overwrite"}}
	arg.Mute = dbfs.mute

	_, err := dbfs.filesClient.Upload(arg, bytes.NewReader(data))
	return dbfs.wrapErrNotExist(filePath, err)
}

// OpenReader opens a file for reading.
// Downloads the entire file content into memory and returns a read-only file buffer.
// This is not suitable for very large files due to memory usage.
func (dbfs *fileSystem) OpenReader(filePath string) (iofs.File, error) {
	info, err := dbfs.Stat(filePath)
	if err != nil {
		return nil, err
	}
	data, err := dbfs.ReadAll(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	return fsimpl.NewReadonlyFileBuffer(data, info), nil
}

// OpenWriter opens a file for writing.
// Creates an in-memory buffer that uploads to Dropbox when closed.
// Requires the parent directory to exist.
func (dbfs *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	dirInfo, err := dbfs.info(path.Dir(filePath))
	if err != nil {
		return nil, err
	}
	if !dirInfo.IsDir {
		return nil, fs.NewErrIsNotDirectory(dbfs.File(path.Dir(filePath)))
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return dbfs.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// OpenReadWriter opens a file for both reading and writing.
// Downloads the entire file content into memory and returns a read-write-seek buffer.
// Uploads to Dropbox when closed. Not suitable for very large files.
func (dbfs *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	data, err := dbfs.ReadAll(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(data, func() error {
		return dbfs.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// CopyFile copies a file from srcFile to destFile within Dropbox.
// Uses the Dropbox CopyV2 API for server-side copying (no data transfer required).
func (dbfs *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	arg := files.NewRelocationArg(srcFile, destFile)
	_, err := dbfs.filesClient.CopyV2(arg)
	return dbfs.wrapErrNotExist(srcFile, err)
}

// Move moves (renames) a file or directory from filePath to destPath.
// Uses the Dropbox MoveV2 API for server-side moving (no data transfer required).
//
// When filePath and destPath resolve to the same location after path
// cleaning, Move returns nil without calling the Dropbox API, matching
// the no-op behavior required by the [fs.MoveFileSystem] contract.
// (Dropbox MoveV2 would otherwise reject the request with a "to/conflict"
// error.)
func (dbfs *fileSystem) Move(filePath string, destPath string) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	filePath = path.Clean(filePath)
	destPath = path.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	arg := files.NewRelocationArg(filePath, destPath)
	_, err := dbfs.filesClient.MoveV2(arg)
	return dbfs.wrapErrNotExist(filePath, err)
}

// Remove deletes a file or directory from Dropbox.
// Uses the Dropbox DeleteV2 API to remove files and directories.
func (dbfs *fileSystem) Remove(filePath string) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	arg := files.NewDeleteArg(filePath)
	_, err := dbfs.filesClient.DeleteV2(arg)
	return dbfs.wrapErrNotExist(filePath, err)
}

// Close closes the filesystem and unregisters it from the global registry.
// After Close all methods return fs.ErrFileSystemClosed instead of using the
// Dropbox API clients. Calling Close more than once is a safe no-op.
func (dbfs *fileSystem) Close() error {
	if dbfs.closed {
		return nil // already closed
	}
	fs.Unregister(dbfs)
	dbfs.closed = true
	return nil
}
