package dropboxfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"path"
	"path/filepath"
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
	_ fs.FileSystem = new(DropboxFileSystem)
)

// DropboxFileSystem implements fs.FileSystem for a Dropbox app.
type DropboxFileSystem struct {
	id            string
	prefix        string
	client        *dropbox.Client
	fileInfoCache *fs.FileInfoCache
}

// New returns a new DropboxFileSystem for accessToken
func New(accessToken string, cacheTimeout time.Duration) *DropboxFileSystem {
	dbfs := &DropboxFileSystem{
		prefix:        Prefix + fsimpl.RandomString(),
		client:        dropbox.New(dropbox.NewConfig(accessToken)),
		fileInfoCache: fs.NewFileInfoCache(cacheTimeout),
	}
	fs.Register(dbfs)
	return dbfs
}

func (dbfs *DropboxFileSystem) wrapErrNotExist(filePath string, err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "path/not_found/") {
		return fs.NewErrDoesNotExist(dbfs.File(filePath))
	}
	return err
}

func (dbfs *DropboxFileSystem) Close() error {
	fs.Unregister(dbfs)
	return nil
}

func (dbfs *DropboxFileSystem) IsReadOnly() bool {
	return false
}

func (dbfs *DropboxFileSystem) IsWriteOnly() bool {
	return false
}

func (dbfs *DropboxFileSystem) Root() fs.File {
	return fs.File(dbfs.prefix + Separator)
}

func (dbfs *DropboxFileSystem) ID() (string, error) {
	if dbfs.id == "" {
		account, err := dbfs.client.Users.GetCurrentAccount()
		if err != nil {
			return "", err
		}
		dbfs.id = account.AccountID
	}
	return dbfs.id, nil
}

func (dbfs *DropboxFileSystem) Prefix() string {
	return dbfs.prefix
}

func (dbfs *DropboxFileSystem) Name() string {
	return "Dropbox file system"
}

// String implements the fmt.Stringer interface.
func (dbfs *DropboxFileSystem) String() string {
	return dbfs.Name() + " with prefix " + dbfs.Prefix()
}

func (dbfs *DropboxFileSystem) File(filePath string) fs.File {
	return dbfs.JoinCleanFile(filePath)
}

func (dbfs *DropboxFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(dbfs.prefix + dbfs.JoinCleanPath(uriParts...))
}

func (dbfs *DropboxFileSystem) URL(cleanPath string) string {
	return dbfs.prefix + cleanPath
}

func (dbfs *DropboxFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, dbfs.prefix, Separator)
}

func (dbfs *DropboxFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, dbfs.prefix, Separator)
}

func (dbfs *DropboxFileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*DropboxFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (dbfs *DropboxFileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, 0, Separator)
}

func (*DropboxFileSystem) VolumeName(filePath string) string {
	return ""
}

func (dbfs *DropboxFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (dbfs *DropboxFileSystem) AbsPath(filePath string) string {
	if !path.IsAbs(filePath) {
		filePath = Separator + filePath
	}
	return path.Clean(filePath)
}

func metadataToFileInfo(meta *dropbox.Metadata) (info fs.FileInfo) {
	info.Name = meta.Name
	info.Exists = true
	info.IsRegular = true
	info.IsDir = meta.Tag == "folder"
	info.IsHidden = len(meta.Name) > 0 && meta.Name[0] == '.'
	info.Size = int64(meta.Size)
	info.ModTime = meta.ServerModified
	if info.IsDir {
		info.Permissions = DefaultDirPermissions
	} else {
		info.Permissions = DefaultPermissions
	}
	info.ContentHash = meta.ContentHash
	return info
}

// Stat returns FileInfo
func (dbfs *DropboxFileSystem) Stat(filePath string) (info fs.FileInfo) {
	// The root folder is unsupported by the API
	if filePath == "/" {
		// info.Name = ""
		info.Exists = true
		info.IsRegular = true
		info.IsDir = true
		info.Permissions = DefaultDirPermissions
		return info
	}

	if cachedInfo, ok := dbfs.fileInfoCache.Get(filePath); ok {
		return *cachedInfo
	}

	meta, err := dbfs.client.Files.GetMetadata(
		&dropbox.GetMetadataInput{
			Path: filePath,
		},
	)
	if err != nil {
		dbfs.fileInfoCache.Delete(filePath)
		// fmt.Println(meta, err)
		return fs.FileInfo{}
	}
	info = metadataToFileInfo(&meta.Metadata)
	if dbfs.fileInfoCache != nil {
		dbfs.fileInfoCache.Put(filePath, &info)
	}
	return info
}

func (dbfs *DropboxFileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (dbfs *DropboxFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (dbfs *DropboxFileSystem) listDirInfo(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	info := dbfs.Stat(dirPath)
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
					dbfs.fileInfoCache.Put(entry.PathDisplay, &info)
				}
				file := dbfs.File(entry.PathDisplay)
				err = callback(file, info)
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

func (dbfs *DropboxFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, true)
}

func (dbfs *DropboxFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	return dbfs.listDirInfo(ctx, dirPath, callback, patterns, true)
}

func (dbfs *DropboxFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(ctx, max, func(ctx context.Context, callback func(fs.File) error) error {
		return dbfs.ListDirInfo(ctx, dirPath, fs.FileCallback(callback).FileInfoCallback, patterns)
	})
}

func (dbfs *DropboxFileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return errors.New("SetPermissions not possible on Dropbox")
}

func (dbfs *DropboxFileSystem) User(filePath string) string {
	return ""
}

func (dbfs *DropboxFileSystem) SetUser(filePath string, user string) error {
	return errors.New("SetUser not possible on Dropbox")
}

func (dbfs *DropboxFileSystem) Group(filePath string) string {
	return ""
}

func (dbfs *DropboxFileSystem) SetGroup(filePath string, group string) error {
	return errors.New("SetGroup not possible on Dropbox")
}

func (dbfs *DropboxFileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if dbfs.Stat(filePath).Exists {
		return errors.New("Touch can't change time on Dropbox")
	}
	return dbfs.WriteAll(filePath, nil, perm)
}

func (dbfs *DropboxFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	_, err := dbfs.client.Files.CreateFolder(&dropbox.CreateFolderInput{Path: dirPath})
	return dbfs.wrapErrNotExist(dirPath, err)
}

func (dbfs *DropboxFileSystem) ReadAll(filePath string) ([]byte, error) {
	out, err := dbfs.client.Files.Download(&dropbox.DownloadInput{Path: filePath})
	if err != nil {
		return nil, dbfs.wrapErrNotExist(filePath, err)
	}
	defer out.Body.Close()

	return ioutil.ReadAll(out.Body)
}

func (dbfs *DropboxFileSystem) WriteAll(filePath string, data []byte, perm []fs.Permissions) error {
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

func (dbfs *DropboxFileSystem) Append(filePath string, data []byte, perm []fs.Permissions) error {
	writer, err := dbfs.OpenAppendWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		return io.ErrShortWrite
	}
	return err
}

func (dbfs *DropboxFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	data, err := dbfs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	return fsimpl.NewReadonlyFileBuffer(data), nil
}

func (dbfs *DropboxFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	if !dbfs.Stat(path.Dir(filePath)).IsDir {
		return nil, fs.NewErrIsNotDirectory(dbfs.File(path.Dir(filePath)))
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return dbfs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

func (dbfs *DropboxFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	writer, err := dbfs.OpenReadWriter(filePath, perm)
	if err != nil {
		return nil, err
	}
	writer.Seek(0, io.SeekEnd)
	return writer, nil
}

func (dbfs *DropboxFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	data, err := dbfs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(data, func() error {
		return dbfs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

func (dbfs *DropboxFileSystem) Watch(filePath string) (<-chan fs.WatchEvent, error) {
	return nil, fmt.Errorf("DropboxFileSystem.Watch: %w", fs.ErrNotSupported)
}

func (dbfs *DropboxFileSystem) Truncate(filePath string, size int64) error {
	info := dbfs.Stat(filePath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(dbfs.File(filePath))
	}
	if info.IsDir {
		return fs.NewErrIsDirectory(dbfs.File(filePath))
	}
	if info.Size <= size {
		return nil
	}
	data, err := dbfs.ReadAll(filePath)
	// File size or existence could have changed in the meantime
	// because this is a slow network FS, the
	if err != nil {
		return dbfs.wrapErrNotExist(filePath, err)
	}
	if int64(len(data)) <= size {
		return nil
	}
	return dbfs.WriteAll(filePath, data[:size], []fs.Permissions{info.Permissions})
}

func (dbfs *DropboxFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err := dbfs.client.Files.Copy(&dropbox.CopyInput{
		FromPath: srcFile,
		ToPath:   destFile,
	})
	return dbfs.wrapErrNotExist(srcFile, err)
}

func (dbfs *DropboxFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	_, err := dbfs.client.Files.Move(&dropbox.MoveInput{
		FromPath: filePath,
		ToPath:   newPath,
	})
	return dbfs.wrapErrNotExist(filePath, err)
}

func (dbfs *DropboxFileSystem) Move(filePath string, destPath string) error {
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

func (dbfs *DropboxFileSystem) Remove(filePath string) error {
	_, err := dbfs.client.Files.Delete(&dropbox.DeleteInput{Path: filePath})
	return dbfs.wrapErrNotExist(filePath, err)
}
