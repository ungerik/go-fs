// Package dropboxfs implements a go-fs file system for Dropbox.
//
// The file system is identified by the Dropbox account id, which New
// fetches, so the same account always gets the same prefix
// "dropbox://<account id>" (the id without its "dbid:" part).
// Metadata is cached for Options.CacheTimeout to save API calls;
// every write operation invalidates the affected cache entries.
//
// Dropbox paths are case-insensitive: "/Docs/a.txt" and "/docs/A.TXT"
// are the same file. FileInfos have the names in the case Dropbox reports,
// usually the one they were created with.
//
// Files of up to 150 MiB are written with one upload request, larger
// ones with an upload session in chunks. Writers from OpenWriter upload
// files larger than one chunk (32 MiB) while they are written, so they
// hold at most one chunk in memory.
//
// Options.Mute controls whether file modifications trigger user
// notifications; set it for automated operations that should not spam
// the account owner.
package dropboxfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

	// DefaultPermissions reported for Dropbox files
	DefaultPermissions = fs.UserAndGroupReadWrite

	// DefaultDirPermissions reported for Dropbox directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite | fs.AllExecute
)

// Compile-time interface checks
var (
	_ fs.FileSystem                 = new(fileSystem)
	_ fs.WriteFileSystem            = new(fileSystem)
	_ fs.ValidateNameFileSystem     = new(fileSystem)
	_ fs.WriteAllFileSystem         = new(fileSystem)
	_ fs.ReadWriterFileSystem       = new(fileSystem)
	_ fs.CopyFileSystem             = new(fileSystem)
	_ fs.MoveFileSystem             = new(fileSystem)
	_ fs.RemoveAllFileSystem        = new(fileSystem)
	_ fs.ListDirRecursiveFileSystem = new(fileSystem)
)

// Options for a Dropbox file system. A nil *Options uses the defaults.
type Options struct {
	// CacheTimeout is the time file metadata is cached,
	// zero disables the cache.
	CacheTimeout time.Duration

	// Mute keeps Dropbox from notifying the account owner
	// of the file modifications made through the file system.
	Mute bool

	// HTTPClient sends all API requests instead of a client that
	// authenticates them with the access token passed to New,
	// which is ignored then.
	//
	// Dropbox issues short-lived access tokens, so long running programs
	// need a client that refreshes them, like the one returned by the
	// Client method of an golang.org/x/oauth2 Config for a token
	// with a refresh token.
	HTTPClient *http.Client
}

// fileSystem implements fs.FileSystem for a Dropbox account.
type fileSystem struct {
	fsimpl.PathHelper

	accountID string
	client    files.ContextClient
	cache     *fileInfoCache
	mute      bool // If true, file modifications won't trigger user notifications
	maxUpload int  // maxUploadSize, smaller in tests
	chunkSize int  // uploadChunkSize, smaller in tests
	closed    atomic.Bool
}

// New fetches the account of the accessToken and returns a file system
// with the prefix "dropbox://<account id>" for it, without registering it.
// ctx is used for the request of the account.
//
// With Options.HTTPClient the accessToken is ignored and may be empty.
func New(ctx context.Context, accessToken string, opts *Options) (fs.FileSystem, error) {
	config, err := newConfig(accessToken, opts)
	if err != nil {
		return nil, err
	}
	return newFromConfig(ctx, config, opts)
}

// NewAndRegister creates a file system for the account of the accessToken
// like New and registers it, so that fs.File values with its prefix
// "dropbox://<account id>" work. It is meant for callers that know the
// account id and work with URIs; fs.GetFileSystem returns the file system
// for the prefix, closing it unregisters it again. Use New and fs.Register
// to get the file system and its prefix.
func NewAndRegister(ctx context.Context, accessToken string, opts *Options) error {
	dbfs, err := New(ctx, accessToken, opts)
	if err != nil {
		return err
	}
	if err := fs.Register(dbfs); err != nil {
		return errors.Join(err, dbfs.Close())
	}
	return nil
}

// newConfig returns the SDK configuration for accessToken and opts.
func newConfig(accessToken string, opts *Options) (dropbox.Config, error) {
	config := dropbox.Config{
		Token:    accessToken,
		LogLevel: dropbox.LogOff,
	}
	if opts != nil {
		config.Client = opts.HTTPClient
	}
	if config.Token == "" && config.Client == nil {
		return config, errors.New("dropboxfs: empty access token and no Options.HTTPClient")
	}
	return config, nil
}

// newFromConfig fetches the current account with config
// and returns a file system for it.
func newFromConfig(ctx context.Context, config dropbox.Config, opts *Options) (*fileSystem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	account, err := users.NewContext(config).GetCurrentAccountContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("dropboxfs: getting the current account: %w", err)
	}
	return newFileSystem(account.AccountId, files.NewContext(config), opts), nil
}

func newFileSystem(accountID string, client files.ContextClient, opts *Options) *fileSystem {
	if opts == nil {
		opts = new(Options)
	}
	return &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + strings.TrimPrefix(accountID, "dbid:"), Rooted: true},
		accountID:  accountID,
		client:     client,
		cache:      newFileInfoCache(opts.CacheTimeout),
		mute:       opts.Mute,
		maxUpload:  maxUploadSize,
		chunkSize:  uploadChunkSize,
	}
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

// ValidateName implements fs.ValidateNameFileSystem: a name that is not
// valid UTF-8 is an error wrapping os.ErrInvalid, because the JSON of the
// Dropbox API would replace its invalid bytes with U+FFFD and the file
// would get another name. Existing files are read with every name.
func (dbfs *fileSystem) ValidateName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: dropboxfs name %.200q is not valid UTF-8", os.ErrInvalid, name)
	}
	return nil
}

func (dbfs *fileSystem) RootDir() fs.File {
	return fs.File(dbfs.URIPrefix + Separator)
}

// ID returns the Dropbox account id.
func (dbfs *fileSystem) ID() string {
	return dbfs.accountID
}

func (dbfs *fileSystem) Name() string {
	return "Dropbox"
}

// String returns the name and the prefix,
// which contains no secret, only the account id.
func (dbfs *fileSystem) String() string {
	return dbfs.Name() + " " + dbfs.Prefix()
}

func (dbfs *fileSystem) file(filePath string) fs.File {
	return fs.File(dbfs.JoinCleanURI(filePath))
}

// apiPath returns the path for the Dropbox API,
// which uses the empty string for the root.
func apiPath(filePath string) string {
	filePath = path.Clean("/" + filePath)
	if filePath == Separator {
		return ""
	}
	return filePath
}

// isInside reports whether the API path p is below the API
// directory path dir, compared case-insensitively like Dropbox does.
func isInside(dir, p string) bool {
	return dir == "" || strings.HasPrefix(strings.ToLower(p), strings.ToLower(dir)+"/")
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
			Modified:    time.Time(m.ServerModified),
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

// metadata returns the metadata of the API path p from the server,
// never from the cache, and puts its FileInfo into the cache.
func (dbfs *fileSystem) metadata(ctx context.Context, p, filePath string) (files.IsMetadata, *fs.FileInfo, error) {
	if err := dbfs.check(ctx); err != nil {
		return nil, nil, err
	}
	if p == "" {
		return nil, dbfs.rootInfo(), nil
	}
	meta, err := dbfs.client.GetMetadataContext(ctx, files.NewGetMetadataArg(p))
	if err != nil {
		if isNotExistError(err) {
			dbfs.cache.Delete(p)
		}
		return nil, nil, dbfs.wrapErr(filePath, err)
	}
	info := dbfs.metadataToFileInfo(meta)
	if info == nil {
		dbfs.cache.Delete(p)
		return nil, nil, fs.NewErrDoesNotExist(dbfs.file(filePath))
	}
	dbfs.cache.Put(p, info)
	return meta, info, nil
}

// stat returns the FileInfo of filePath from the cache or the API.
func (dbfs *fileSystem) stat(ctx context.Context, filePath string) (*fs.FileInfo, error) {
	if err := dbfs.check(ctx); err != nil {
		return nil, err
	}
	p := apiPath(filePath)
	if p == "" {
		return dbfs.rootInfo(), nil
	}
	if info, ok := dbfs.cache.Get(p); ok {
		return info, nil
	}
	_, info, err := dbfs.metadata(ctx, p, filePath)
	return info, err
}

// Stat returns the FileInfo of filePath from the cache or the API.
func (dbfs *fileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return dbfs.stat(context.Background(), filePath)
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
	result, err := dbfs.client.ListFolderContext(ctx, arg)
	if err != nil {
		if tag := lookupTag(err); tag == files.LookupErrorNotFound || tag == files.LookupErrorNotFolder {
			// A file is reported as not_folder or not_found by ListFolder
			if info, statErr := dbfs.stat(ctx, dirPath); statErr == nil && !info.IsDir {
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
		if err := dbfs.check(ctx); err != nil {
			return err
		}
		result, err = dbfs.client.ListFolderContinueContext(ctx, files.NewListFolderContinueArg(result.Cursor))
		if err != nil {
			return dbfs.wrapErr(dirPath, err)
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// OpenReader streams the download of the file.
// A folder is an fs.ErrIsDirectory.
func (dbfs *fileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	return dbfs.download(context.Background(), filePath)
}

// download returns the content of the file at filePath.
func (dbfs *fileSystem) download(ctx context.Context, filePath string) (io.ReadCloser, error) {
	p := apiPath(filePath)
	if p == "" {
		return nil, fs.NewErrIsDirectory(dbfs.RootDir())
	}
	_, body, err := dbfs.client.DownloadContext(ctx, files.NewDownloadArg(p))
	if err != nil {
		return nil, dbfs.wrapErr(filePath, err)
	}
	return body, nil
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// WriteAll uploads data as the file, overwriting an existing one.
// Data larger than 150 MiB is uploaded with an upload session.
// A folder at filePath is an fs.ErrIsDirectory.
func (dbfs *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := apiPath(filePath)
	if p == "" {
		return fs.NewErrIsDirectory(dbfs.RootDir())
	}
	meta, err := dbfs.upload(ctx, p, data)
	return dbfs.uploaded(p, filePath, meta, err)
}

// OpenWriter returns a writer that uploads the file, see uploadWriter.
// The file is created or replaced when the writer is closed,
// a folder at filePath is an fs.ErrIsDirectory then.
// The writer implements fs.WriteAborter.
func (dbfs *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := apiPath(filePath)
	if p == "" {
		return nil, fs.NewErrIsDirectory(dbfs.RootDir())
	}
	return &uploadWriter{
		dbfs:     dbfs,
		filePath: filePath,
		session:  uploadSession{dbfs: dbfs, path: p},
	}, nil
}

// OpenReadWriter downloads the file (or starts empty for a missing one)
// into a memory buffer that is uploaded on Close.
// The buffer implements fs.WriteAborter.
func (dbfs *fileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if err := dbfs.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	ctx := context.Background()
	var data []byte
	body, err := dbfs.download(ctx, filePath)
	switch {
	case err == nil:
		data, err = fs.ReadAll(ctx, body)
		err = errors.Join(err, body.Close())
		if err != nil {
			return nil, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return fsimpl.NewWriteOnCloseFileBuffer(data, func(data []byte) error {
		return dbfs.WriteAll(ctx, filePath, data, perm)
	}), nil
}

// MakeDir creates a folder. It returns an error wrapping
// os.ErrExist if the path already exists.
//
// Unlike other file systems Dropbox creates missing parent folders.
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
	result, err := dbfs.client.CreateFolderV2Context(context.Background(), files.NewCreateFolderArg(p))
	if err != nil {
		if conflict := conflictTag(err); conflict == files.WriteConflictErrorFile || conflict == files.WriteConflictErrorFolder {
			return fs.NewErrAlreadyExists(dbfs.file(dirPath))
		}
		return dbfs.wrapErr(dirPath, err)
	}
	dbfs.cache.Put(p, dbfs.metadataToFileInfo(result.Metadata))
	return nil
}

// CopyFile copies a file server-side.
//
// An existing file at destFile is replaced, an existing folder
// is an fs.ErrIsDirectory. Copying a file onto itself, also with
// a destination that differs only in case, is a no-op.
func (dbfs *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	src, dest := apiPath(srcFile), apiPath(destFile)
	if strings.EqualFold(src, dest) {
		return nil
	}
	info, err := dbfs.stat(ctx, srcFile)
	if err != nil {
		return err
	}
	if info.IsDir {
		return fs.NewErrIsDirectory(info.File)
	}
	if dest == "" {
		return fs.NewErrIsDirectory(dbfs.RootDir())
	}
	return dbfs.relocate(ctx, false, src, dest)
}

// Move moves or renames a file or folder server-side.
//
// An existing file at destPath is replaced (an fs.ErrIsNotDirectory
// if filePath is a folder), an existing folder is an error wrapping
// os.ErrExist and is neither deleted nor merged.
// Moving a folder into itself is an error wrapping os.ErrInvalid.
// Renames that only change the case of a name are supported.
//
// When filePath and destPath are the same after path cleaning, Move
// returns nil without calling the Dropbox API, matching the no-op
// behavior required by the [fs.MoveFileSystem] contract.
func (dbfs *fileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if err := dbfs.check(ctx); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return fs.ErrEmptyPath
	}
	src, dest := apiPath(filePath), apiPath(destPath)
	switch {
	case src == dest:
		return nil
	case dest == "":
		return fs.NewErrAlreadyExists(dbfs.RootDir())
	case isInside(src, dest):
		// Only a folder can contain the destination
		_, info, err := dbfs.metadata(ctx, src, filePath)
		if err != nil {
			return err
		}
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(info.File)
		}
		return fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, info.File, dbfs.file(dest))
	case strings.EqualFold(src, dest):
		return dbfs.moveCase(ctx, src, dest)
	}
	return dbfs.relocate(ctx, true, src, dest)
}

// relocateV2 moves or copies src to dest with one API call.
func (dbfs *fileSystem) relocateV2(ctx context.Context, move bool, src, dest string) (*files.RelocationResult, error) {
	if err := dbfs.check(ctx); err != nil {
		return nil, err
	}
	arg := files.NewRelocationArg(src, dest)
	if move {
		return dbfs.client.MoveV2Context(ctx, arg)
	}
	return dbfs.client.CopyV2Context(ctx, arg)
}

// relocate moves or copies the API path src to dest, which differ
// not only in case, with the overwrite semantics of Move and CopyFile.
//
// Dropbox refuses to relocate onto an existing path. An existing file
// at dest is therefore deleted before a second attempt, if src is not
// a folder on the server, and only the version of the file that was
// checked (DeleteArg.ParentRev), so a folder or a newer file that took
// its place in the meantime is never deleted.
// The deleted file stays restorable in the version history of Dropbox.
func (dbfs *fileSystem) relocate(ctx context.Context, move bool, src, dest string) error {
	defer func() {
		if move {
			dbfs.cache.Delete(src)
		}
		dbfs.cache.Delete(dest)
	}()
	result, err := dbfs.relocateV2(ctx, move, src, dest)
	if conflict := conflictTag(err); conflict == files.WriteConflictErrorFile || conflict == files.WriteConflictErrorFolder {
		destMeta, destInfo, statErr := dbfs.metadata(ctx, dest, dest)
		if statErr != nil {
			return statErr
		}
		switch {
		case destInfo.IsDir && move:
			return fs.NewErrAlreadyExists(destInfo.File)
		case destInfo.IsDir:
			return fs.NewErrIsDirectory(destInfo.File)
		}
		// A folder never replaces a file. The source is checked on the
		// server before the file is deleted, because CopyFile checked it
		// in the cache and it may have become a folder since then.
		_, srcInfo, statErr := dbfs.metadata(ctx, src, src)
		if statErr != nil {
			return statErr
		}
		switch {
		case srcInfo.IsDir && move:
			return fs.NewErrIsNotDirectory(destInfo.File)
		case srcInfo.IsDir:
			return fs.NewErrIsDirectory(srcInfo.File)
		}
		if err := dbfs.deleteFile(ctx, dest, destMeta); err != nil {
			return err
		}
		result, err = dbfs.relocateV2(ctx, move, src, dest)
	}
	if err != nil {
		return dbfs.wrapRelocationErr(src, dest, err)
	}
	if _, isFolder := result.Metadata.(*files.FolderMetadata); isFolder && !move {
		// The source was a file when it was checked, but is a folder now:
		// don't leave a copy of the folder behind
		_, delErr := dbfs.client.DeleteV2Context(ctx, files.NewDeleteArg(dest))
		return errors.Join(fs.NewErrIsDirectory(dbfs.file(src)), delErr)
	}
	return nil
}

// deleteFile deletes the file of the API path p with the metadata meta,
// but only the version of meta.
func (dbfs *fileSystem) deleteFile(ctx context.Context, p string, meta files.IsMetadata) error {
	arg := files.NewDeleteArg(p)
	if m, ok := meta.(*files.FileMetadata); ok {
		arg.ParentRev = m.Rev
	}
	dbfs.cache.Delete(p)
	_, err := dbfs.client.DeleteV2Context(ctx, arg)
	return dbfs.wrapErr(p, err)
}

// moveCase renames the API path src to dest, which differs only in case.
// Dropbox may refuse that as a move onto itself, then src is moved to
// a temporary name in the same folder first. The conflict handling of
// relocate must never run for such a move: it would delete src.
func (dbfs *fileSystem) moveCase(ctx context.Context, src, dest string) error {
	defer dbfs.cache.Delete(src)
	_, err := dbfs.relocateV2(ctx, true, src, dest)
	if !isConflictError(err) {
		return dbfs.wrapRelocationErr(src, dest, err)
	}
	tmp := path.Join(path.Dir(src), ".gofs-rename-"+rand.Text())
	_, err = dbfs.relocateV2(ctx, true, src, tmp)
	if err != nil {
		return dbfs.wrapRelocationErr(src, dest, err)
	}
	_, err = dbfs.relocateV2(ctx, true, tmp, dest)
	if err != nil {
		// Restore the original name
		_, restoreErr := dbfs.relocateV2(context.WithoutCancel(ctx), true, tmp, src)
		return errors.Join(dbfs.wrapRelocationErr(src, dest, err), restoreErr)
	}
	return nil
}

// Remove deletes a file or an empty folder.
// It returns an error wrapping os.ErrNotExist for a missing path
// and an fs.ErrDirectoryNotEmpty for a folder with content.
//
// The type of the path is always checked on the server, never in the
// cache, because the DeleteV2 API call deletes folders recursively.
// A file is only deleted in the version that was checked. A folder
// could still get content between the check and the deletion,
// which is then deleted with it.
func (dbfs *fileSystem) Remove(filePath string) error {
	if err := dbfs.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := apiPath(filePath)
	if p == "" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, dbfs)
	}
	ctx := context.Background()
	meta, info, err := dbfs.metadata(ctx, p, filePath)
	if err != nil {
		return err
	}
	if info.IsDir {
		arg := files.NewListFolderArg(p)
		arg.Limit = 1
		result, err := dbfs.client.ListFolderContext(ctx, arg)
		if err != nil {
			return dbfs.wrapErr(filePath, err)
		}
		if len(result.Entries) > 0 || result.HasMore {
			return fs.NewErrDirectoryNotEmpty(info.File)
		}
	}
	return dbfs.deleteFile(ctx, p, meta)
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
	p := apiPath(filePath)
	if p == "" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, dbfs)
	}
	dbfs.cache.Delete(p)
	_, err := dbfs.client.DeleteV2Context(ctx, files.NewDeleteArg(p))
	if isNotExistError(err) {
		return nil
	}
	return dbfs.wrapErr(filePath, err)
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
