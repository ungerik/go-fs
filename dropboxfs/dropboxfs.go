package dropboxfs

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/satori/go.uuid"
	"github.com/tj/go-dropbox"

	"github.com/ungerik/go-fs"
)

// Prefix of DropboxFileSystem URLs
const Prefix = "dropbox://"

var (
	// DefaultPermissions used for Dropbox files
	DefaultPermissions = fs.UserAndGroupReadWrite
	// DefaultDirPermissions used for Dropbox directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllExecute
)

// DropboxFileSystem implements fs.FileSystem for a Dropbox app.
type DropboxFileSystem struct {
	prefix        string
	client        *dropbox.Client
	fileInfoCache *fs.FileInfoCache
}

// New returns a new DropboxFileSystem for accessToken
func New(accessToken string, cacheTimeout time.Duration) *DropboxFileSystem {
	dbfs := &DropboxFileSystem{
		prefix:        Prefix + uuid.NewV4().String(),
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

func (dbfs *DropboxFileSystem) Destroy() error {
	fs.Unregister(dbfs)
	return nil
}

func (dbfs *DropboxFileSystem) IsReadOnly() bool {
	return false
}

func (dbfs *DropboxFileSystem) Prefix() string {
	return dbfs.prefix
}

func (dbfs *DropboxFileSystem) Name() string {
	return "Dropbox file system"
}

func (dbfs *DropboxFileSystem) String() string {
	return dbfs.Name() + " with prefix " + dbfs.Prefix()
}

func (dbfs *DropboxFileSystem) File(uriParts ...string) fs.File {
	return fs.File(dbfs.prefix + dbfs.CleanPath(uriParts...))
}

func (dbfs *DropboxFileSystem) URL(cleanPath string) string {
	return dbfs.prefix + cleanPath
}

func (dbfs *DropboxFileSystem) CleanPath(uriParts ...string) string {
	return path.Clean(strings.TrimPrefix(path.Join(uriParts...), dbfs.prefix))
}

func (dbfs *DropboxFileSystem) SplitPath(filePath string) []string {
	filePath = strings.TrimPrefix(filePath, dbfs.prefix)
	filePath = strings.TrimPrefix(filePath, "/")
	return strings.Split(filePath, "/")
}

func (dbfs *DropboxFileSystem) Seperator() string {
	return "/"
}

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern works like path.Match or filepath.Match
func (*DropboxFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fs.MatchAnyPatternImpl(name, patterns)
}

func (dbfs *DropboxFileSystem) FileName(filePath string) string {
	return path.Base(filePath)
}

func (dbfs *DropboxFileSystem) Ext(filePath string) string {
	return path.Ext(filePath)
}

func (dbfs *DropboxFileSystem) Dir(filePath string) string {
	return path.Dir(filePath)
}

func metadataToFileInfo(meta *dropbox.Metadata) (info fs.FileInfo) {
	info.Exists = true
	info.IsRegular = true
	info.IsDir = meta.Tag == "folder"
	info.Size = int64(meta.Size)
	info.ModTime = meta.ServerModified
	if info.IsDir {
		info.Permissions = DefaultDirPermissions
	} else {
		info.Permissions = DefaultPermissions
	}
	return info
}

// Stat returns FileInfo
func (dbfs *DropboxFileSystem) Stat(filePath string) (info fs.FileInfo) {
	// The root folder is unsupported by the API
	if filePath == "/" {
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

func (dbfs *DropboxFileSystem) listDir(dirPath string, callback func(fs.File) error, patterns []string, recursive bool) (err error) {
	info := dbfs.Stat(dirPath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(dbfs.File(dirPath))
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(dbfs.File(dirPath))
	}

	var cursor string
	for {
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
			match, err := fs.MatchAnyPatternImpl(entry.Name, patterns)
			if match {
				if dbfs.fileInfoCache != nil {
					info := metadataToFileInfo(entry)
					dbfs.fileInfoCache.Put(entry.PathDisplay, &info)
				}
				err = callback(dbfs.File(entry.PathDisplay))
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

func (dbfs *DropboxFileSystem) ListDir(dirPath string, callback func(fs.File) error, patterns []string) (err error) {
	return dbfs.listDir(dirPath, callback, patterns, true)
}

func (dbfs *DropboxFileSystem) ListDirRecursive(dirPath string, callback func(fs.File) error, patterns []string) (err error) {
	return dbfs.listDir(dirPath, callback, patterns, true)
}

func (dbfs *DropboxFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	listDirFunc := fs.ListDirFunc(func(callback func(fs.File) error) error {
		return dbfs.ListDir(dirPath, callback, patterns)
	})
	return listDirFunc.ListDirMaxImpl(max)
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

func (dbfs *DropboxFileSystem) OpenReader(filePath string) (fs.ReadSeekCloser, error) {
	data, err := dbfs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	return fs.NewReadonlyFileBuffer(data), nil
}

func (dbfs *DropboxFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteSeekCloser, error) {
	if !dbfs.Stat(dbfs.Dir(filePath)).IsDir {
		return nil, fs.NewErrIsNotDirectory(dbfs.File(dbfs.Dir(filePath)))
	}
	var fileBuffer *fs.FileBuffer
	fileBuffer = fs.NewFileBufferWithClose(nil, func() error {
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
	var fileBuffer *fs.FileBuffer
	fileBuffer = fs.NewFileBufferWithClose(data, func() error {
		return dbfs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

func (dbfs *DropboxFileSystem) Watch(filePath string) (<-chan fs.WatchEvent, error) {
	return nil, fs.ErrFileWatchNotSupported
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

func (dbfs *DropboxFileSystem) CopyFile(srcFile string, destFile string, buf *[]byte) error {
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
