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
	FileSystem    = &HTTPFileSystem{prefix: Prefix}
	FileSystemTLS = &HTTPFileSystem{prefix: PrefixTLS}
)

type HTTPFileSystem struct {
	fs.ReadOnlyBase

	prefix string
}

func (*HTTPFileSystem) RootDir() fs.File {
	return fs.InvalidFile
}

func (f *HTTPFileSystem) ID() (string, error) {
	return strings.TrimSuffix(f.prefix, "://"), nil
}

func (f *HTTPFileSystem) Prefix() string {
	return f.prefix
}

func (f *HTTPFileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.prefix, "://"))
}

func (f *HTTPFileSystem) String() string {
	return f.Name() + " read-only file system"
}

func (f *HTTPFileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *HTTPFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

func (f *HTTPFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, f.prefix, Separator)
}

func (f *HTTPFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), f.Separator())
}

func (f *HTTPFileSystem) Separator() string { return Separator }

func (f *HTTPFileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, f.prefix)
}

func (f *HTTPFileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return f.prefix + strings.TrimPrefix(filePath, Separator)
}

func (f *HTTPFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (f *HTTPFileSystem) info(filePath string) fs.FileInfo {
	// First try fast HEAD request
	request, err := http.NewRequest("HEAD", f.URL(filePath), nil)
	if err != nil {
		return fs.FileInfo{}
	}
	response, err := http.DefaultClient.Do(request)
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
	response, err = http.DefaultClient.Get(f.URL(filePath))
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

func (f *HTTPFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	info := f.info(filePath)
	if !info.Exists {
		return nil, fs.NewErrDoesNotExist(fs.File(filePath))
	}
	return info.StdFileInfo(), nil
}

func (f *HTTPFileSystem) Exists(filePath string) bool {
	return f.info(filePath).Exists
}

func (f *HTTPFileSystem) IsHidden(filePath string) bool       { return false }
func (f *HTTPFileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *HTTPFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	return fs.NewErrUnsupported(f, "ListDirInfo")
}

func (f *HTTPFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	return fs.NewErrUnsupported(f, "ListDirInfoRecursive")
}

func (f *HTTPFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error) {
	return nil, fs.NewErrUnsupported(f, "ListDirMax")
}

func (f *HTTPFileSystem) ReadAll(ctx context.Context, filePath string) (data []byte, err error) {
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

func (f *HTTPFileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	info, err := f.Stat(filePath)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Get(f.URL(filePath))
	if err != nil {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("HTTPFileSystem.OpenReader: %d: %s", response.StatusCode, response.Status)
	}
	defer response.Body.Close()
	return fsimpl.NewReadonlyFileBufferReadAll(response.Body, info)
}
