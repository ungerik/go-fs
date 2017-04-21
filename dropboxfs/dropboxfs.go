package dropboxfs

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"

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

func wrapErrNotExist(filePath string, err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "path/not_found/") {
		return fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return err
}

// DropboxFileSystem implements fs.FileSystem for a Dropbox app.
type DropboxFileSystem struct {
	prefix string
	client *dropbox.Client
}

// New returns a new DropboxFileSystem for accessToken
func New(accessToken string) *DropboxFileSystem {
	return &DropboxFileSystem{
		prefix: "",
		client: dropbox.New(dropbox.NewConfig(accessToken)),
	}
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

func (dbfs *DropboxFileSystem) FileName(filePath string) string {
	return path.Base(filePath)
}

func (dbfs *DropboxFileSystem) Ext(filePath string) string {
	return path.Ext(filePath)
}

func (dbfs *DropboxFileSystem) Dir(filePath string) string {
	return path.Dir(filePath)
}

func (dbfs *DropboxFileSystem) GetMetadata(filePath string) (*dropbox.GetMetadataOutput, error) {
	return dbfs.client.Files.GetMetadata(
		&dropbox.GetMetadataInput{
			Path: dbfs.CleanPath(filePath),
		},
	)
}

// Stat returns FileInfo
func (dbfs *DropboxFileSystem) Stat(filePath string) (info fs.FileInfo) {
	meta, err := dbfs.GetMetadata(dbfs.CleanPath(filePath))
	if err != nil {
		return info
	}
	info.Exists = true
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

func (dbfs *DropboxFileSystem) ListDir(dirPath string, callback func(fs.File) error, patterns []string) (err error) {
	info := dbfs.Stat(dirPath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(fs.File(dirPath))
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(fs.File(dirPath))
	}

	dirPath = dbfs.CleanPath(dirPath)

	var cursor string
	for {
		var out *dropbox.ListFolderOutput

		if cursor == "" {
			out, err = dbfs.client.Files.ListFolder(&dropbox.ListFolderInput{Path: dirPath})
		} else {
			out, err = dbfs.client.Files.ListFolderContinue(&dropbox.ListFolderContinueInput{Cursor: cursor})
		}
		if err != nil {
			return wrapErrNotExist(dirPath, err)
		}
		cursor = out.Cursor

		for _, entry := range out.Entries {
			match, err := fs.MatchAnyPattern(entry.Name, patterns)
			if match {
				err = callback(dbfs.File(dirPath, entry.Name))
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

func (dbfs *DropboxFileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(dirPath, max, patterns, func(dirPath string, callback func(fs.File) error, patterns []string) error {
		return dbfs.ListDir(dirPath, callback, patterns)
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
	filePath = dbfs.CleanPath(filePath)
	if dbfs.Stat(filePath).Exists {
		return errors.New("Touch can't change time on Dropbox")
	}
	return dbfs.WriteAll(filePath, nil, perm)
}

func (dbfs *DropboxFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	_, err := dbfs.client.Files.CreateFolder(&dropbox.CreateFolderInput{Path: dbfs.CleanPath(dirPath)})
	return wrapErrNotExist(dirPath, err)
}

func (dbfs *DropboxFileSystem) ReadAll(filePath string) ([]byte, error) {
	out, err := dbfs.client.Files.Download(&dropbox.DownloadInput{Path: dbfs.CleanPath(filePath)})
	if err != nil {
		return nil, wrapErrNotExist(filePath, err)
	}
	defer out.Body.Close()
	return ioutil.ReadAll(out.Body)
}

func (dbfs *DropboxFileSystem) WriteAll(filePath string, data []byte, perm []fs.Permissions) error {
	_, err := dbfs.client.Files.Upload(
		&dropbox.UploadInput{
			Path:   dbfs.CleanPath(filePath),
			Mode:   dropbox.WriteModeOverwrite,
			Mute:   true,
			Reader: bytes.NewBuffer(data),
		},
	)
	return wrapErrNotExist(filePath, err)
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
		return nil, fs.NewErrIsNotDirectory(fs.File(dbfs.Dir(filePath)))
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
		return fs.NewErrDoesNotExist(fs.File(filePath))
	}
	if info.IsDir {
		return fs.NewErrIsDirectory(fs.File(filePath))
	}
	if info.Size <= size {
		return nil
	}
	data, err := dbfs.ReadAll(filePath)
	// File size or existence could have changed in the meantime
	// because this is a slow network FS, the
	if err != nil {
		return wrapErrNotExist(filePath, err)
	}
	if int64(len(data)) <= size {
		return nil
	}
	return dbfs.WriteAll(filePath, data[:size], []fs.Permissions{info.Permissions})
}

func (dbfs *DropboxFileSystem) CopyFile(srcFile string, destFile string, buf *[]byte) error {
	_, err := dbfs.client.Files.Copy(&dropbox.CopyInput{
		FromPath: dbfs.CleanPath(srcFile),
		ToPath:   dbfs.CleanPath(destFile),
	})
	return wrapErrNotExist(srcFile, err)
}

func (dbfs *DropboxFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	_, err := dbfs.client.Files.Move(&dropbox.MoveInput{
		FromPath: dbfs.CleanPath(filePath),
		ToPath:   dbfs.CleanPath(newPath),
	})
	return wrapErrNotExist(filePath, err)
}

func (dbfs *DropboxFileSystem) Move(filePath string, destPath string) error {
	// if !dbfs.Stat(filePath).Exists {
	// 	return NewErrDoesNotExist(File(filePath))
	// }
	// if dbfs.Stat(destPath).IsDir {
	// 	destPath = filepath.Join(destPath, dbfs.FileName(filePath))
	// }
	_, err := dbfs.client.Files.Move(&dropbox.MoveInput{
		FromPath: dbfs.CleanPath(filePath),
		ToPath:   dbfs.CleanPath(destPath),
	})
	return wrapErrNotExist(filePath, err)
}

func (dbfs *DropboxFileSystem) Remove(filePath string) error {
	_, err := dbfs.client.Files.Delete(&dropbox.DeleteInput{Path: dbfs.CleanPath(filePath)})
	return wrapErrNotExist(filePath, err)
}
