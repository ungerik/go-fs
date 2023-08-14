// Package httpfs implements a read only file system
// for HTTP URLs.
// Import it to register FileSystem and FileSystemTLS:
//
//	import _ "github.com/ungerik/go-fs/httpfs"
package httpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"os"
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
	FileSystem    fs.FileSystem = &HTTPFileSystem{prefix: Prefix}
	FileSystemTLS fs.FileSystem = &HTTPFileSystem{prefix: PrefixTLS}
)

type HTTPFileSystem struct {
	fs.ReadOnlyBase

	prefix string
}

func (*HTTPFileSystem) Root() fs.File {
	return fs.InvalidFile
}

// ID returns a unique identifyer for the FileSystem
func (f *HTTPFileSystem) ID() (string, error) {
	return strings.TrimSuffix(f.prefix, "://"), nil
}

func (f *HTTPFileSystem) Prefix() string {
	return f.prefix
}

// Name returns the name of the FileSystem implementation
func (f *HTTPFileSystem) Name() string {
	return strings.ToUpper(strings.TrimSuffix(f.prefix, "://"))
}

// String returns a descriptive string for the FileSystem implementation
func (f *HTTPFileSystem) String() string {
	return f.Name() + " read-only file system"
}

// URL returns a full URL wich is Prefix() + cleanPath
func (f *HTTPFileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

// JoinCleanFile joins the file system prefix with uriParts
// into a File with clean path and prefix
func (f *HTTPFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(f.prefix + f.JoinCleanPath(uriParts...))
}

// JoinCleanPath joins the uriParts into a cleaned path
// of the file system style without the file system prefix
func (f *HTTPFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, f.prefix, Separator)
}

// SplitPath returns all Separator() delimited components of filePath
// without the file system prefix.
func (f *HTTPFileSystem) SplitPath(filePath string) []string {
	return strings.Split(strings.TrimPrefix(filePath, f.prefix), Separator)
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

// DirAndName returns the parent directory of filePath and the name with that directory of the last filePath element.
// If filePath is the root of the file systeme, then an empty string will be returned for name.
func (f *HTTPFileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, 0, Separator)
}

// VolumeName returns the name of the volume at the beginning of the filePath,
// or an empty string if the filePath has no volume.
// A volume is for example "C:" on Windows
func (f *HTTPFileSystem) VolumeName(filePath string) string { return "" }

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
			Exists:  true,
			Name:    path.Base(request.URL.Path),
			Size:    response.ContentLength,
			ModTime: modified,
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
		Exists:  true,
		Name:    path.Base(request.URL.Path),
		Size:    size,
		ModTime: modified,
	}
}

func (f *HTTPFileSystem) Stat(filePath string) (os.FileInfo, error) {
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

func (f *HTTPFileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	return nil, fmt.Errorf("HTTPFileSystem.Watch: %w", errors.ErrUnsupported)
}

// ListDirInfo calls the passed callback function for every file and directory in dirPath.
// If any patterns are passed, then only files or directores with a name that matches
// at least one of the patterns are returned.
func (f *HTTPFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) error {
	return fmt.Errorf("HTTPFileSystem.ListDirInfo: %w", errors.ErrUnsupported)
}

// ListDirInfoRecursive calls the passed callback function for every file (not directory) in dirPath
// recursing into all sub-directories.
// If any patterns are passed, then only files (not directories) with a name that matches
// at least one of the patterns are returned.
func (f *HTTPFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) error {
	return fmt.Errorf("HTTPFileSystem.ListDirInfoRecursive: %w", errors.ErrUnsupported)
}

// ListDirMax returns at most max files and directories in dirPath.
// A max value of -1 returns all files.
// If any patterns are passed, then only files or directories with a name that matches
// at least one of the patterns are returned.
func (f *HTTPFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error) {
	return nil, fmt.Errorf("HTTPFileSystem.ListDirMax: %w", errors.ErrUnsupported)
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
