package dropboxfs

import (
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"path"
	"strings"
	"time"

	"github.com/tj/go-dropbox"

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
	client        *dropbox.Client
	fileInfoCache *fs.FileInfoCache
}

// NewAndRegister returns a new fs.FileSystem for a Dropbox with
// the passed accessToken and registers it.
func NewAndRegister(accessToken string, cacheTimeout time.Duration) fs.FileSystem {
	dbfs := &fileSystem{
		prefix:        Prefix + fsimpl.RandomString(),
		client:        dropbox.New(dropbox.NewConfig(accessToken)),
		fileInfoCache: fs.NewFileInfoCache(cacheTimeout),
	}
	fs.Register(dbfs)
	return dbfs
}

func (dbfs *fileSystem) wrapErrNotExist(filePath string, err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "path/not_found/") {
		return fs.NewErrDoesNotExist(dbfs.File(filePath))
	}
	return err
}

func (dbfs *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (dbfs *fileSystem) RootDir() fs.File {
	return fs.File(dbfs.prefix + Separator)
}

func (dbfs *fileSystem) ID() (string, error) {
	if dbfs.id == "" {
		account, err := dbfs.client.Users.GetCurrentAccount()
		if err != nil {
			return "", err
		}
		dbfs.id = account.AccountID
	}
	return dbfs.id, nil
}

func (dbfs *fileSystem) Prefix() string {
	return dbfs.prefix
}

func (dbfs *fileSystem) Name() string {
	return "Dropbox file system"
}

// String implements the fmt.Stringer interface.
func (dbfs *fileSystem) String() string {
	return dbfs.Name() + " with prefix " + dbfs.Prefix()
}

func (dbfs *fileSystem) File(filePath string) fs.File {
	return dbfs.JoinCleanFile(filePath)
}

func (dbfs *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(dbfs.prefix + dbfs.JoinCleanPath(uriParts...))
}

func (dbfs *fileSystem) URL(cleanPath string) string {
	return dbfs.prefix + cleanPath
}

func (dbfs *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, dbfs.prefix, Separator)
}

func (dbfs *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, dbfs.prefix, Separator)
}

func (dbfs *fileSystem) Separator() string {
	return Separator
}

func (*fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (dbfs *fileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (dbfs *fileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

func metadataToFileInfo(meta *dropbox.Metadata) *fs.FileInfo {
	var info fs.FileInfo
	info.Name = meta.Name
	info.Exists = true
	info.IsRegular = true
	info.IsDir = meta.Tag == "folder"
	info.IsHidden = len(meta.Name) > 0 && meta.Name[0] == '.'
	info.Size = int64(meta.Size)
	info.Modified = meta.ServerModified
	if info.IsDir {
		info.Permissions = DefaultDirPermissions
	} else {
		info.Permissions = DefaultPermissions
	}
	// info.ContentHash = meta.ContentHash
	return &info
}

// info returns FileInfo
func (dbfs *fileSystem) info(filePath string) *fs.FileInfo {

	// The root folder is unsupported by the API
	if filePath == "/" {

		return &fs.FileInfo{
			Name:        "",
			Exists:      true,
			IsRegular:   true,
			IsDir:       true,
			Permissions: DefaultDirPermissions,
		}
	}

	if cachedInfo, ok := dbfs.fileInfoCache.Get(filePath); ok {
		return cachedInfo
	}

	meta, err := dbfs.client.Files.GetMetadata(
		&dropbox.GetMetadataInput{
			Path: filePath,
		},
	)
	if err != nil {
		dbfs.fileInfoCache.Delete(filePath)
		// fmt.Println(meta, err)
		return new(fs.FileInfo)
	}
	info := metadataToFileInfo(&meta.Metadata)
	if dbfs.fileInfoCache != nil {
		dbfs.fileInfoCache.Put(filePath, info)
	}
	return info
}

func (dbfs *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info := dbfs.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

func (dbfs *fileSystem) Exists(filePath string) bool {
	return dbfs.info(filePath).Exists
}

func (dbfs *fileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (dbfs *fileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (dbfs *fileSystem) listDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	info := dbfs.info(dirPath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(dbfs.File(dirPath))
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(dbfs.File(dirPath))
	}

	var cursor string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var out *dropbox.ListFolderOutput

		if cursor == "" {
			out, err = dbfs.client.Files.ListFolder(
				&dropbox.ListFolderInput{
					Path:      dirPath,
					Recursive: recursive,
				},
			)
		} else {
			out, err = dbfs.client.Files.ListFolderContinue(
				&dropbox.ListFolderContinueInput{
					Cursor: cursor,
				},
			)
		}
		if err != nil {
			return dbfs.wrapErrNotExist(dirPath, err)
		}
		cursor = out.Cursor

		// fmt.Println("out.Entries", len(out.Entries))

		for _, entry := range out.Entries {
			// fmt.Println(entry)
			match, err := fsimpl.MatchAnyPattern(entry.Name, patterns)
			if match {
				info := metadataToFileInfo(entry)
				if dbfs.fileInfoCache != nil {
					dbfs.fileInfoCache.Put(entry.PathDisplay, info)
				}
				// file := dbfs.File(entry.PathDisplay)
				err = callback(info)
			}
			if err != nil {
				return err
			}
		}

		if !out.HasMore {
			break
		}
	}
	return nil
}

func (dbfs *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, true)
}

func (dbfs *fileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, true)
}

func (dbfs *fileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if dbfs.info(filePath).Exists {
		return errors.New("Touch can't change time on Dropbox")
	}
	return dbfs.WriteAll(context.Background(), filePath, nil, perm)
}

func (dbfs *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	_, err := dbfs.client.Files.CreateFolder(&dropbox.CreateFolderInput{Path: dirPath})
	return dbfs.wrapErrNotExist(dirPath, err)
}

func (dbfs *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out, err := dbfs.client.Files.Download(&dropbox.DownloadInput{Path: filePath})
	if err != nil {
		return nil, dbfs.wrapErrNotExist(filePath, err)
	}
	defer out.Body.Close()

	return fs.ReadAllContext(ctx, out.Body)
}

func (dbfs *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err := dbfs.client.Files.Upload(
		&dropbox.UploadInput{
			Path:   filePath,
			Mode:   dropbox.WriteModeOverwrite,
			Mute:   true,
			Reader: bytes.NewBuffer(data),
		},
	)
	return dbfs.wrapErrNotExist(filePath, err)
}

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

func (dbfs *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if !dbfs.info(path.Dir(filePath)).IsDir {
		return nil, fs.NewErrIsNotDirectory(dbfs.File(path.Dir(filePath)))
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return dbfs.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

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

func (dbfs *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err := dbfs.client.Files.Copy(&dropbox.CopyInput{
		FromPath: srcFile,
		ToPath:   destFile,
	})
	return dbfs.wrapErrNotExist(srcFile, err)
}

func (dbfs *fileSystem) Move(filePath string, destPath string) error {
	// if !dbfs.Stat(filePath).Exists {
	// 	return NewErrDoesNotExist(File(filePath))
	// }
	// if dbfs.Stat(destPath).IsDir {
	// 	destPath = filepath.Join(destPath, dbfs.FileName(filePath))
	// }
	_, err := dbfs.client.Files.Move(&dropbox.MoveInput{
		FromPath: filePath,
		ToPath:   destPath,
	})
	return dbfs.wrapErrNotExist(filePath, err)
}

func (dbfs *fileSystem) Remove(filePath string) error {
	_, err := dbfs.client.Files.Delete(&dropbox.DeleteInput{Path: filePath})
	return dbfs.wrapErrNotExist(filePath, err)
}

func (dbfs *fileSystem) Close() error {
	if dbfs.id == "" {
		return nil // already closed
	}
	fs.Unregister(dbfs)
	dbfs.id = ""
	return nil
}
