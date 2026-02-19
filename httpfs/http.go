// Package httpfs implements a read only file system
// for HTTP URLs.
// Import it to register FileSystem and FileSystemTLS:
//
//	import _ "github.com/ungerik/go-fs/httpfs"
package httpfs

import (
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

func init() {
	fs.Register(FileSystem)
	fs.Register(FileSystemTLS)
}

const (
	Prefix    = "http://"
	PrefixTLS = "https://"
	Separator = "/"
)

var (
	FileSystem    = &fileSystem{prefix: Prefix}
	FileSystemTLS = &fileSystem{prefix: PrefixTLS}
)

type fileSystem struct {
	fs.ReadOnlyBase

	prefix string
}

func (*fileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

func (f *fileSystem) ID() (string, error) {
	return strings.TrimSuffix(f.prefix, "://"), nil
}

func (f *fileSystem) Prefix() string {
	return f.prefix
}

func (f *fileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.prefix, "://"))
}

func (f *fileSystem) String() string {
	return f.Name() + " read-only file system"
}

func (f *fileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *fileSystem) CleanPathFromURI(uri string) string {
	return path.Clean(strings.TrimPrefix(uri, f.prefix))
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

func (f *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, f.prefix, Separator)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), f.Separator())
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, f.prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return f.prefix + strings.TrimPrefix(filePath, Separator)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (f *fileSystem) info(filePath string) fs.FileInfo {
	// First try fast HEAD request
	request, err := http.NewRequest("HEAD", f.URL(filePath), nil)
	if err != nil {
		return fs.FileInfo{}
	}
	response, err := http.DefaultClient.Do(request) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return fs.FileInfo{}
	}
	if response.ContentLength >= 0 {
		modified, err := http.ParseTime(response.Header.Get("Last-Modified"))
		if err != nil {
			modified, err = http.ParseTime(response.Header.Get("Date"))
			if err != nil {
				modified = time.Time{}
			}
		}
		return fs.FileInfo{
			Exists:   true,
			Name:     path.Base(request.URL.Path),
			Size:     response.ContentLength,
			Modified: modified,
		}
	}

	// If HEAD request did not return a ContentLength do a full GET request
	response, err = http.DefaultClient.Get(f.URL(filePath)) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return fs.FileInfo{}
	}
	modified, err := http.ParseTime(response.Header.Get("Last-Modified"))
	if err != nil {
		modified, err = http.ParseTime(response.Header.Get("Date"))
		if err != nil {
			modified = time.Time{}
		}
	}
	size := response.ContentLength

	if size < 0 {
		// Read full body if still no ContentLength available
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return fs.FileInfo{}
		}
		size = int64(len(body))
	}

	return fs.FileInfo{
		Exists:   true,
		Name:     path.Base(request.URL.Path),
		Size:     size,
		Modified: modified,
	}
}

func (f *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info := f.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

func (f *fileSystem) Exists(filePath string) bool {
	return f.info(filePath).Exists
}

func (f *fileSystem) IsHidden(filePath string) bool       { return false }
func (f *fileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	return fs.NewErrUnsupported(f, "ListDirInfo")
}

func (f *fileSystem) ReadAll(ctx context.Context, filePath string) (data []byte, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// TODO use HTTP GET with context
	reader, err := f.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.ReadAll: %w", err)
	}

	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.ReadAll: %w", err)
	}

	return data, nil
}

func (f *fileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Get(f.URL(filePath)) //#nosec G704 -- HTTP filesystem intentionally fetches user-provided URLs
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %d: %s", response.StatusCode, response.Status)
	}
	defer response.Body.Close()
	return fsimpl.NewReadonlyFileBufferReadAll(response.Body, info)
}

func (f *fileSystem) Close() error {
	return nil
}
