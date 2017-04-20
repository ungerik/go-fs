package dropboxfs

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path"
	"strings"
	"time"

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

func (dbfs *DropboxFileSystem) Exists(filePath string) bool {
	_, err := dbfs.GetMetadata(filePath)
	return err == nil
}

func (dbfs *DropboxFileSystem) IsDir(filePath string) bool {
	info, err := dbfs.GetMetadata(filePath)
	return err == nil && info.Tag == "folder"
}

func (dbfs *DropboxFileSystem) Size(filePath string) int64 {
	info, err := dbfs.GetMetadata(filePath)
	if err != nil {
		return 0
	}
	return int64(info.Size)
}

func (dbfs *DropboxFileSystem) ModTime(filePath string) time.Time {
	info, err := dbfs.GetMetadata(filePath)
	if err != nil {
		return time.Time{}
	}
	return info.ServerModified
}

func (dbfs *DropboxFileSystem) ListDir(dirPath string, callback func(fs.File) error, patterns []string) (err error) {
	if !dbfs.IsDir(dirPath) {
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
			return err
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

func (dbfs *DropboxFileSystem) ListDirMax(dirPath string, n int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(dbfs, dirPath, n, patterns)
}

func (dbfs *DropboxFileSystem) Permissions(filePath string) fs.Permissions {
	info, err := dbfs.GetMetadata(filePath)
	if err != nil {
		return fs.NoPermissions
	}
	perm := DefaultPermissions
	if info.Tag == "folder" {
		perm += fs.AllExecute
	}
	return perm
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
	if dbfs.Exists(filePath) {
		return errors.New("Touch can't change time on Dropbox")
	} else {
		return dbfs.WriteAll(filePath, nil, perm)
	}
}

func (dbfs *DropboxFileSystem) MakeDir(filePath string, perm []fs.Permissions) error {
	_, err := dbfs.client.Files.CreateFolder(&dropbox.CreateFolderInput{Path: dbfs.CleanPath(filePath)})
	return err
}

func (dbfs *DropboxFileSystem) ReadAll(filePath string) ([]byte, error) {
	out, err := dbfs.client.Files.Download(&dropbox.DownloadInput{Path: dbfs.CleanPath(filePath)})
	if err != nil {
		if strings.HasPrefix(err.Error(), "path/not_found/") {
			return nil, fs.NewErrFileDoesNotExist(fs.File(filePath))
		}
		return nil, err
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
	return err
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
	if !dbfs.IsDir(dbfs.Dir(filePath)) {
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
	return nil, fs.ErrFileWatchNotAvailable
}

func (dbfs *DropboxFileSystem) Truncate(filePath string, size int64) error {
	panic("to do")
}

func (dbfs *DropboxFileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains a path separators: " + newName)
	}
	newPath := path.Join(path.Dir(filePath), newName)
	panic("to do")
	// if err != nil {
	// 	return err
	// }
	filePath = newPath
	return nil
}

func (dbfs *DropboxFileSystem) Move(filePath string, destPath string) error {
	if dbfs.IsDir(destPath) {
		destPath = path.Join(destPath, dbfs.FileName(filePath))
	}
	panic("to do")
	// if err != nil {
	// 	return err
	// }
	filePath = destPath
	return nil
}

func (dbfs *DropboxFileSystem) Remove(filePath string) error {
	panic("to do")
}
