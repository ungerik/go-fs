// Package dropboxfs implements a go-fs file system for Dropbox.
//
// The file system is identified by the Dropbox account id, which the
// constructor fetches, so the same account always gets the same prefix
// "dropbox://<account id>". Metadata is cached for a configurable time
// to save API calls; every write operation invalidates the affected
// cache entries.
//
// The mute option controls whether file modifications trigger user
// notifications; set it for automated operations that should not spam
// the account owner.
package dropboxfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/users"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	// Prefix of Dropbox file system URIs, followed by the account id
	Prefix = "dropbox://"

	// Separator used in Dropbox file system paths
	Separator = "/"
)

var (
	// DefaultPermissions reported for Dropbox files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for Dropbox directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllExecute

	// Compile-time interface checks
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ExistsFileSystem           = new(fileSystem)
	_ fs.ReadAllFileSystem          = new(fileSystem)
	_ fs.WriteAllFileSystem         = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.TouchFileSystem            = new(fileSystem)
	_ fs.CopyFileSystem             = new(fileSystem)
	_ fs.MoveFileSystem             = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
)

// fileSystem implements fs.FileSystem for a Dropbox account.
type fileSystem struct {
	fsimpl.PathHelper

	accountID   string
	filesClient files.Client
	cache       *fileInfoCache
	mute        bool // If true, file modifications won't trigger user notifications
	closed      atomic.Bool
}

// NewAndRegister fetches the account of the accessToken,
// creates a file system with the prefix "dropbox://<account id>"
// and registers it.
//
// Metadata is cached for cacheTimeout, zero disables the cache.
// If mute is true, users are not notified of file changes
// made through the file system.
func NewAndRegister(ctx context.Context, accessToken string, cacheTimeout time.Duration, mute bool) (fs.FileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := dropbox.Config{
		Token:    accessToken,
		LogLevel: dropbox.LogOff,
	}
	account, err := users.New(config).GetCurrentAccount()
	if err != nil {
		return nil, fmt.Errorf("dropboxfs: getting the current account: %w", err)
	}
	dbfs := newFileSystem(account.AccountId, files.New(config), cacheTimeout, mute)
	fs.Register(dbfs)
	return dbfs, nil
}

func newFileSystem(accountID string, filesClient files.Client, cacheTimeout time.Duration, mute bool) *fileSystem {
	return &fileSystem{
		PathHelper:  fsimpl.PathHelper{URIPrefix: Prefix + strings.TrimPrefix(accountID, "dbid:"), Rooted: true},
		accountID:   accountID,
		filesClient: filesClient,
		cache:       newFileInfoCache(cacheTimeout),
		mute:        mute,
	}
}

///////////////////////////////////////////////////////////////////////////////
// Errors

// lookupNotFound reports whether a LookupError means "path not found".
func lookupNotFound(l *files.LookupError) bool {
	return l != nil && l.Tag == files.LookupErrorNotFound
}

// isNotExistError reports whether err is a typed Dropbox API error that
// means a file or folder does not exist, as opposed to a transport or
// authorization error (network outage, rate limit, 5xx, expired token).
//
// The distinction matters: treating a transient error as "does not exist"
// would make existing files appear to vanish, which is dangerous for
// callers that check existence before overwriting or deleting.
func isNotExistError(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := errors.AsType[files.GetMetadataAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.Path)
	}
	if e, ok := errors.AsType[files.DownloadAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.Path)
	}
	if e, ok := errors.AsType[files.ListFolderAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.Path)
	}
	if e, ok := errors.AsType[files.DeleteAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.PathLookup)
	}
	if e, ok := errors.AsType[files.MoveAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.FromLookup)
	}
	if e, ok := errors.AsType[files.CopyAPIError](err); ok {
		return e.EndpointError != nil && lookupNotFound(e.EndpointError.FromLookup)
	}
	return false
}

// isConflictError reports whether err is a typed Dropbox API error
// that means the target path already exists.
func isConflictError(err error) bool {
	if e, ok := errors.AsType[files.CreateFolderAPIError](err); ok {
		return e.EndpointError != nil && e.EndpointError.Path != nil && e.EndpointError.Path.Tag == files.WriteErrorConflict
	}
	return false
}

// wrapErr converts typed Dropbox "not found" and "conflict" errors
// for filePath to the fs error types. All other errors (including
// transport and authorization errors) are returned unchanged.
func (dbfs *fileSystem) wrapErr(filePath string, err error) error {
	switch {
	case err == nil:
		return nil
	case isNotExistError(err):
		return fs.NewErrDoesNotExist(dbfs.file(filePath))
	case isConflictError(err):
		return fs.NewErrAlreadyExists(dbfs.file(filePath))
	}
	return err
}

func (dbfs *fileSystem) checkClosed() error {
	if dbfs.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	return nil
}

// check returns the first error of the closed check and the context.
func (dbfs *fileSystem) check(ctx context.Context) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	return ctx.Err()
}

///////////////////////////////////////////////////////////////////////////////
// Metadata

func (dbfs *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (dbfs *fileSystem) RootDir() fs.File {
	return fs.File(dbfs.URIPrefix + Separator)
}

// ID returns the Dropbox account id.
func (dbfs *fileSystem) ID() string {
	return dbfs.accountID
}

func (dbfs *fileSystem) Name() string {
	return "Dropbox file system"
}

func (dbfs *fileSystem) String() string {
	return dbfs.Name() + " with prefix " + dbfs.Prefix()
}

func (dbfs *fileSystem) file(filePath string) fs.File {
	return fs.File(dbfs.JoinCleanURI(filePath))
}

// apiPath returns the path for the Dropbox API,
// which uses the empty string for the root.
func apiPath(filePath string) string {
	filePath = path.Clean(filePath)
	if filePath == Separator || filePath == "." {
		return ""
	}
	return filePath
}

func (dbfs *fileSystem) rootInfo() *fs.FileInfo {
	return &fs.FileInfo{
		File:        dbfs.RootDir(),
		Name:        Separator,
		Exists:      true,
		IsDir:       true,
		Permissions: DefaultDirPermissions,
	}
}

// metadataToFileInfo converts Dropbox file or folder metadata to a FileInfo.
// Deleted and unknown metadata yields nil.
func (dbfs *fileSystem) metadataToFileInfo(meta files.IsMetadata) *fs.FileInfo {
	switch m := meta.(type) {
	case *files.FileMetadata:
		return &fs.FileInfo{
			File:        dbfs.file(m.PathDisplay),
			Name:        m.Name,
			Exists:      true,
			IsRegular:   true,
			IsHidden:    strings.HasPrefix(m.Name, "."),
			Size:        int64(m.Size), //#nosec G115 -- int64 limit will not be exceeded in real world use cases
			Modified:    m.ServerModified,
			Permissions: DefaultPermissions,
		}
	case *files.FolderMetadata:
		return &fs.FileInfo{
			File:        dbfs.file(m.PathDisplay),
			Name:        m.Name,
			Exists:      true,
			IsDir:       true,
			IsHidden:    strings.HasPrefix(m.Name, "."),
			Permissions: DefaultDirPermissions,
		}
	}
	return nil
}

// Stat returns the FileInfo of filePath from the cache or the API.
func (dbfs *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := apiPath(filePath)
	if p == "" {
		return dbfs.rootInfo(), nil
	}
	if info, ok := dbfs.cache.Get(p); ok {
		return info, nil
	}
	meta, err := dbfs.filesClient.GetMetadata(files.NewGetMetadataArg(p))
	if err != nil {
		return nil, dbfs.wrapErr(filePath, err)
	}
	info := dbfs.metadataToFileInfo(meta)
	if info == nil {
		return nil, fs.NewErrDoesNotExist(dbfs.file(filePath))
	}
	dbfs.cache.Put(p, info)
	return info, nil
}

// Exists uses the cached metadata when available.
func (dbfs *fileSystem) Exists(filePath string) (bool, error) {
	_, err := dbfs.Stat(filePath)
	switch {
	case err == nil:
		return true, nil
	case isNotExist(err):
		return false, nil
	}
	return false, err
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrDoesNotExist{}) || errors.As(err, new(fs.ErrDoesNotExist))
}

func (dbfs *fileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return dbfs.listDir(ctx, dirPath, patterns, callback, false)
}

// ListDirRecursive lists all files (not directories) below dirPath
// with a single recursive ListFolder request.
func (dbfs *fileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return dbfs.listDir(ctx, dirPath, patterns, callback, true)
}

func (dbfs *fileSystem) listDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error, recursive bool) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	arg := files.NewListFolderArg(apiPath(dirPath))
	arg.Recursive = recursive
	result, err := dbfs.filesClient.ListFolder(arg)
	if err != nil {
		if isNotExistError(err) {
			// A file is reported as not found by ListFolder
			if info, statErr := dbfs.Stat(dirPath); statErr == nil && !info.IsDir {
				return fs.NewErrIsNotDirectory(info.File)
			}
		}
		return dbfs.wrapErr(dirPath, err)
	}
	for {
		for _, entry := range result.Entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			info := dbfs.metadataToFileInfo(entry)
			if info == nil || (recursive && info.IsDir) {
				continue
			}
			dbfs.cache.Put(apiPath(info.File.Path()), info)
			match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
			if err != nil {
				return err
			}
			if !match {
				continue
			}
			err = callback(info)
			if err != nil {
				return err
			}
		}
		if !result.HasMore {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err = dbfs.filesClient.ListFolderContinue(files.NewListFolderContinueArg(result.Cursor))
		if err != nil {
			return err
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// OpenReader streams the download of the file.
func (dbfs *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	_, body, err := dbfs.filesClient.Download(files.NewDownloadArg(apiPath(filePath)))
	if err != nil {
		return nil, dbfs.wrapErr(filePath, err)
	}
	return body, nil
}

// ReadAll downloads the complete content of the file.
func (dbfs *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := dbfs.check(ctx); err != nil {
		return nil, err
	}
	body, err := dbfs.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return fs.ReadAllContext(ctx, body)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads data as the file, overwriting an existing one.
func (dbfs *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := apiPath(filePath)
	arg := files.NewUploadArg(p)
	arg.Mode = &files.WriteMode{Tagged: dropbox.Tagged{Tag: files.WriteModeOverwrite}}
	arg.Mute = dbfs.mute
	meta, err := dbfs.filesClient.Upload(arg, bytes.NewReader(data))
	if err != nil {
		dbfs.cache.Delete(p)
		return dbfs.wrapErr(filePath, err)
	}
	dbfs.cache.Put(p, dbfs.metadataToFileInfo(meta))
	return nil
}

// OpenWriter returns a writer that buffers the data in memory
// and uploads it on Close.
func (dbfs *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
		return dbfs.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// OpenReadWriter downloads the file (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
func (dbfs *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	data, err := dbfs.ReadAll(context.Background(), filePath)
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(data, func(data []byte) error {
		return dbfs.WriteAll(context.Background(), filePath, data, perm)
	}), nil
}

// Touch creates an empty file if it does not exist.
// Dropbox can't update the modification time of an existing file,
// so Touch returns an ErrUnsupported error for an existing path.
func (dbfs *fileSystem) Touch(filePath string, perm fs.Permissions) error {
	exists, err := dbfs.Exists(filePath)
	if err != nil {
		return err
	}
	if exists {
		return fs.NewErrUnsupported(dbfs, "Touch of an existing file")
	}
	return dbfs.WriteAll(context.Background(), filePath, nil, perm)
}

// MakeDir creates a folder. It returns an error wrapping
// os.ErrExist if the path already exists.
func (dbfs *fileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	p := apiPath(dirPath)
	if p == "" {
		return fs.NewErrAlreadyExists(dbfs.RootDir())
	}
	result, err := dbfs.filesClient.CreateFolderV2(files.NewCreateFolderArg(p))
	if err != nil {
		return dbfs.wrapErr(dirPath, err)
	}
	dbfs.cache.Put(p, dbfs.metadataToFileInfo(result.Metadata))
	return nil
}

// CopyFile copies a file server-side. Copying a file onto itself is a no-op.
func (dbfs *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	src, dest := apiPath(srcFile), apiPath(destFile)
	if src == dest {
		return nil
	}
	dbfs.cache.Delete(dest)
	_, err := dbfs.filesClient.CopyV2(files.NewRelocationArg(src, dest))
	return dbfs.wrapErr(srcFile, err)
}

// Move moves or renames a file or folder server-side.
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
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	src, dest := apiPath(filePath), apiPath(destPath)
	if src == dest {
		return nil
	}
	dbfs.cache.Clear()
	_, err := dbfs.filesClient.MoveV2(files.NewRelocationArg(src, dest))
	return dbfs.wrapErr(filePath, err)
}

// Remove deletes a file or an empty folder.
// It returns an error wrapping os.ErrNotExist for a missing path
// and refuses to delete a folder with content.
func (dbfs *fileSystem) Remove(filePath string) error {
	info, err := dbfs.Stat(filePath)
	if err != nil {
		return err
	}
	p := apiPath(filePath)
	if info.IsDir {
		if p == "" {
			return fmt.Errorf("can't remove root directory of %s", dbfs)
		}
		arg := files.NewListFolderArg(p)
		arg.Limit = 1
		result, err := dbfs.filesClient.ListFolder(arg)
		if err != nil {
			return dbfs.wrapErr(filePath, err)
		}
		if len(result.Entries) > 0 || result.HasMore {
			return fmt.Errorf("directory not empty: %s", info.File)
		}
	}
	dbfs.cache.Delete(p)
	_, err = dbfs.filesClient.DeleteV2(files.NewDeleteArg(p))
	return dbfs.wrapErr(filePath, err)
}

// RemoveAll deletes a file or a folder with all its content.
// A missing path is not an error.
func (dbfs *fileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	dbfs.cache.Clear()
	_, err := dbfs.filesClient.DeleteV2(files.NewDeleteArg(apiPath(filePath)))
	if err != nil && isNotExistError(err) {
		return nil
	}
	return err
}

// Close unregisters the file system. Every method returns
// fs.ErrFileSystemClosed afterwards. Close is idempotent.
func (dbfs *fileSystem) Close() error {
	if dbfs.closed.Swap(true) {
		return nil // already closed
	}
	fs.Unregister(dbfs)
	return nil
}
