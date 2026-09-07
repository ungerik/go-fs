// Package fstest provides testing helpers for github.com/ungerik/go-fs,
// analogous to the standard library's testing/fstest package.
//
// RunConformance runs the conformance suite that every FileSystem
// implementation must pass.
//
// MockFileSystem and MockFullyFeaturedFileSystem implement the go-fs
// FileSystem interfaces with per-method function pointers, so tests can
// control individual behaviors without a real backing file system.
package fstest

import (
	"context"
	"io"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ fs.FileSystem      = &MockFileSystem{}
	_ fs.WriteFileSystem = &MockFileSystem{}

	_ fs.ExistsFileSystem           = &MockFullyFeaturedFileSystem{}
	_ fs.ReadAllFileSystem          = &MockFullyFeaturedFileSystem{}
	_ fs.WriteAllFileSystem         = &MockFullyFeaturedFileSystem{}
	_ fs.AppendFileSystem           = &MockFullyFeaturedFileSystem{}
	_ fs.AppendWriterFileSystem     = &MockFullyFeaturedFileSystem{}
	_ fs.ReadWriterFileSystem       = &MockFullyFeaturedFileSystem{}
	_ fs.TruncateFileSystem         = &MockFullyFeaturedFileSystem{}
	_ fs.TouchFileSystem            = &MockFullyFeaturedFileSystem{}
	_ fs.MakeAllDirsFileSystem      = &MockFullyFeaturedFileSystem{}
	_ fs.RemoveAllFileSystem        = &MockFullyFeaturedFileSystem{}
	_ fs.CopyFileSystem             = &MockFullyFeaturedFileSystem{}
	_ fs.MoveFileSystem             = &MockFullyFeaturedFileSystem{}
	_ fs.RenameFileSystem           = &MockFullyFeaturedFileSystem{}
	_ fs.ListDirMaxFileSystem       = &MockFullyFeaturedFileSystem{}
	_ fs.ListDirRecursiveFileSystem = &MockFullyFeaturedFileSystem{}
	_ fs.WatchFileSystem            = &MockFullyFeaturedFileSystem{}
	_ fs.HiddenFileSystem           = &MockFullyFeaturedFileSystem{}
	_ fs.PermissionsFileSystem      = &MockFullyFeaturedFileSystem{}
	_ fs.UserFileSystem             = &MockFullyFeaturedFileSystem{}
	_ fs.GroupFileSystem            = &MockFullyFeaturedFileSystem{}
	_ fs.SymbolicLinkFileSystem     = &MockFullyFeaturedFileSystem{}
	_ fs.XAttrFileSystem            = &MockFullyFeaturedFileSystem{}
	_ fs.VolumeNameFileSystem       = &MockFullyFeaturedFileSystem{}
	_ fs.AbsPathFileSystem          = &MockFullyFeaturedFileSystem{}
)

// MockFileSystem is a fs.FileSystem and fs.WriteFileSystem implementation
// with function pointers for every method.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding method.
//
// If a function pointer is nil, then the corresponding method will panic,
// except for:
//   - Prefix returns MockPrefix or "mock://" if that is empty
//   - Name and String return "MockFileSystem"
//   - Separator returns "/"
//   - ID returns Prefix()
//   - CleanPath is implemented by a fsimpl.PathHelper for the prefix
//   - RootDir returns the root of the prefix
//   - ReadableWritable returns true, true
//   - Close returns nil
type MockFileSystem struct {
	// MockPrefix is the prefix string returned by the Prefix() method.
	// If empty, defaults to "mock://".
	MockPrefix string

	MockID               func() string
	MockName             func() string
	MockString           func() string
	MockSeparator        func() string
	MockReadableWritable func() (readable, writable bool)
	MockRootDir          func() fs.File
	MockCleanPath        func(uriParts ...string) string
	MockStat             func(filePath string) (*fs.FileInfo, error)
	MockListDir          func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error
	MockOpenReader       func(filePath string) (io.ReadCloser, error)
	MockClose            func() error

	MockOpenWriter func(filePath string, perm fs.Permissions) (io.WriteCloser, error)
	MockMakeDir    func(dirPath string, perm fs.Permissions) error
	MockRemove     func(filePath string) error
}

// MockFullyFeaturedFileSystem is a MockFileSystem that additionally
// implements every optional file system interface
// with function pointers for every method.
//
// If a function pointer is nil, then the corresponding method will panic.
type MockFullyFeaturedFileSystem struct {
	MockFileSystem

	MockExists           func(filePath string) (bool, error)
	MockReadAll          func(ctx context.Context, filePath string) ([]byte, error)
	MockWriteAll         func(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error
	MockAppend           func(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error
	MockOpenAppendWriter func(filePath string, perm fs.Permissions) (io.WriteCloser, error)
	MockOpenReadWriter   func(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error)
	MockTruncate         func(filePath string, size int64) error
	MockTouch            func(filePath string, perm fs.Permissions) error
	MockMakeAllDirs      func(dirPath string, perm fs.Permissions) error
	MockRemoveAll        func(ctx context.Context, filePath string) error
	MockCopyFile         func(ctx context.Context, srcFile string, destFile string) error
	MockMove             func(filePath string, destPath string) error
	MockRename           func(filePath string, newName string) (newPath string, err error)
	MockListDirMax       func(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error)
	MockListDirRecursive func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error
	MockWatch            func(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error)
	MockIsHidden         func(filePath string) bool
	MockSetPermissions   func(filePath string, perm fs.Permissions) error
	MockUser             func(filePath string) (string, error)
	MockSetUser          func(filePath string, user string) error
	MockGroup            func(filePath string) (string, error)
	MockSetGroup         func(filePath string, group string) error

	MockIsSymbolicLink     func(filePath string) bool
	MockCreateSymbolicLink func(targetPath, linkPath string) error
	MockReadSymbolicLink   func(linkPath string) (targetPath string, err error)

	MockListXAttr   func(filePath string, followSymlinks bool) ([]string, error)
	MockGetXAttr    func(filePath string, name string, followSymlinks bool) ([]byte, error)
	MockSetXAttr    func(filePath string, name string, data []byte, flags int, followSymlinks bool) error
	MockRemoveXAttr func(filePath string, name string, followSymlinks bool) error

	MockVolumeName func(filePath string) string
	MockIsAbsPath  func(filePath string) bool
	MockAbsPath    func(filePath string) string
	MockRelPath    func(basePath, targPath string) (string, error)
}

func (m *MockFileSystem) pathHelper() fsimpl.PathHelper {
	return fsimpl.PathHelper{URIPrefix: m.Prefix(), PathSep: m.Separator(), Rooted: true}
}

// ID implements fs.FileSystem
func (m *MockFileSystem) ID() string {
	if m.MockID == nil {
		return m.Prefix()
	}
	return m.MockID()
}

// Prefix implements fs.FileSystem
func (m *MockFileSystem) Prefix() string {
	if m.MockPrefix == "" {
		return "mock://"
	}
	return m.MockPrefix
}

// Name implements fs.FileSystem
func (m *MockFileSystem) Name() string {
	if m.MockName == nil {
		return "MockFileSystem"
	}
	return m.MockName()
}

// String implements fs.FileSystem
func (m *MockFileSystem) String() string {
	if m.MockString == nil {
		return "MockFileSystem"
	}
	return m.MockString()
}

// Separator implements fs.FileSystem
func (m *MockFileSystem) Separator() string {
	if m.MockSeparator == nil {
		return "/"
	}
	return m.MockSeparator()
}

// ReadableWritable implements fs.FileSystem
func (m *MockFileSystem) ReadableWritable() (readable, writable bool) {
	if m.MockReadableWritable == nil {
		return true, true
	}
	return m.MockReadableWritable()
}

// RootDir implements fs.FileSystem
func (m *MockFileSystem) RootDir() fs.File {
	if m.MockRootDir == nil {
		return fs.File(m.pathHelper().JoinCleanURI(m.Separator()))
	}
	return m.MockRootDir()
}

// CleanPath implements fs.FileSystem
func (m *MockFileSystem) CleanPath(uriParts ...string) string {
	if m.MockCleanPath == nil {
		return m.pathHelper().CleanPath(uriParts...)
	}
	return m.MockCleanPath(uriParts...)
}

// Stat implements fs.FileSystem
func (m *MockFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	if m.MockStat == nil {
		panic("MockFileSystem.MockStat is nil")
	}
	return m.MockStat(filePath)
}

// ListDir implements fs.FileSystem
func (m *MockFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if m.MockListDir == nil {
		panic("MockFileSystem.MockListDir is nil")
	}
	return m.MockListDir(ctx, dirPath, patterns, callback)
}

// OpenReader implements fs.FileSystem
func (m *MockFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if m.MockOpenReader == nil {
		panic("MockFileSystem.MockOpenReader is nil")
	}
	return m.MockOpenReader(filePath)
}

// Close implements fs.FileSystem
func (m *MockFileSystem) Close() error {
	if m.MockClose == nil {
		return nil
	}
	return m.MockClose()
}

// OpenWriter implements fs.WriteFileSystem
func (m *MockFileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if m.MockOpenWriter == nil {
		panic("MockFileSystem.MockOpenWriter is nil")
	}
	return m.MockOpenWriter(filePath, perm)
}

// MakeDir implements fs.WriteFileSystem
func (m *MockFileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if m.MockMakeDir == nil {
		panic("MockFileSystem.MockMakeDir is nil")
	}
	return m.MockMakeDir(dirPath, perm)
}

// Remove implements fs.WriteFileSystem
func (m *MockFileSystem) Remove(filePath string) error {
	if m.MockRemove == nil {
		panic("MockFileSystem.MockRemove is nil")
	}
	return m.MockRemove(filePath)
}

// Exists implements fs.ExistsFileSystem
func (m *MockFullyFeaturedFileSystem) Exists(filePath string) (bool, error) {
	if m.MockExists == nil {
		panic("MockFullyFeaturedFileSystem.MockExists is nil")
	}
	return m.MockExists(filePath)
}

// ReadAll implements fs.ReadAllFileSystem
func (m *MockFullyFeaturedFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if m.MockReadAll == nil {
		panic("MockFullyFeaturedFileSystem.MockReadAll is nil")
	}
	return m.MockReadAll(ctx, filePath)
}

// WriteAll implements fs.WriteAllFileSystem
func (m *MockFullyFeaturedFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if m.MockWriteAll == nil {
		panic("MockFullyFeaturedFileSystem.MockWriteAll is nil")
	}
	return m.MockWriteAll(ctx, filePath, data, perm)
}

// Append implements fs.AppendFileSystem
func (m *MockFullyFeaturedFileSystem) Append(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if m.MockAppend == nil {
		panic("MockFullyFeaturedFileSystem.MockAppend is nil")
	}
	return m.MockAppend(ctx, filePath, data, perm)
}

// OpenAppendWriter implements fs.AppendWriterFileSystem
func (m *MockFullyFeaturedFileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if m.MockOpenAppendWriter == nil {
		panic("MockFullyFeaturedFileSystem.MockOpenAppendWriter is nil")
	}
	return m.MockOpenAppendWriter(filePath, perm)
}

// OpenReadWriter implements fs.ReadWriterFileSystem
func (m *MockFullyFeaturedFileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if m.MockOpenReadWriter == nil {
		panic("MockFullyFeaturedFileSystem.MockOpenReadWriter is nil")
	}
	return m.MockOpenReadWriter(filePath, perm)
}

// Truncate implements fs.TruncateFileSystem
func (m *MockFullyFeaturedFileSystem) Truncate(filePath string, size int64) error {
	if m.MockTruncate == nil {
		panic("MockFullyFeaturedFileSystem.MockTruncate is nil")
	}
	return m.MockTruncate(filePath, size)
}

// Touch implements fs.TouchFileSystem
func (m *MockFullyFeaturedFileSystem) Touch(filePath string, perm fs.Permissions) error {
	if m.MockTouch == nil {
		panic("MockFullyFeaturedFileSystem.MockTouch is nil")
	}
	return m.MockTouch(filePath, perm)
}

// MakeAllDirs implements fs.MakeAllDirsFileSystem
func (m *MockFullyFeaturedFileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	if m.MockMakeAllDirs == nil {
		panic("MockFullyFeaturedFileSystem.MockMakeAllDirs is nil")
	}
	return m.MockMakeAllDirs(dirPath, perm)
}

// RemoveAll implements fs.RemoveAllFileSystem
func (m *MockFullyFeaturedFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if m.MockRemoveAll == nil {
		panic("MockFullyFeaturedFileSystem.MockRemoveAll is nil")
	}
	return m.MockRemoveAll(ctx, filePath)
}

// CopyFile implements fs.CopyFileSystem
func (m *MockFullyFeaturedFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if m.MockCopyFile == nil {
		panic("MockFullyFeaturedFileSystem.MockCopyFile is nil")
	}
	return m.MockCopyFile(ctx, srcFile, destFile)
}

// Move implements fs.MoveFileSystem
func (m *MockFullyFeaturedFileSystem) Move(filePath string, destPath string) error {
	if m.MockMove == nil {
		panic("MockFullyFeaturedFileSystem.MockMove is nil")
	}
	return m.MockMove(filePath, destPath)
}

// Rename implements fs.RenameFileSystem
func (m *MockFullyFeaturedFileSystem) Rename(filePath string, newName string) (newPath string, err error) {
	if m.MockRename == nil {
		panic("MockFullyFeaturedFileSystem.MockRename is nil")
	}
	return m.MockRename(filePath, newName)
}

// ListDirMax implements fs.ListDirMaxFileSystem
func (m *MockFullyFeaturedFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error) {
	if m.MockListDirMax == nil {
		panic("MockFullyFeaturedFileSystem.MockListDirMax is nil")
	}
	return m.MockListDirMax(ctx, dirPath, max, patterns)
}

// ListDirRecursive implements fs.ListDirRecursiveFileSystem
func (m *MockFullyFeaturedFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if m.MockListDirRecursive == nil {
		panic("MockFullyFeaturedFileSystem.MockListDirRecursive is nil")
	}
	return m.MockListDirRecursive(ctx, dirPath, patterns, callback)
}

// Watch implements fs.WatchFileSystem
func (m *MockFullyFeaturedFileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	if m.MockWatch == nil {
		panic("MockFullyFeaturedFileSystem.MockWatch is nil")
	}
	return m.MockWatch(filePath, onEvent)
}

// IsHidden implements fs.HiddenFileSystem
func (m *MockFullyFeaturedFileSystem) IsHidden(filePath string) bool {
	if m.MockIsHidden == nil {
		panic("MockFullyFeaturedFileSystem.MockIsHidden is nil")
	}
	return m.MockIsHidden(filePath)
}

// SetPermissions implements fs.PermissionsFileSystem
func (m *MockFullyFeaturedFileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	if m.MockSetPermissions == nil {
		panic("MockFullyFeaturedFileSystem.MockSetPermissions is nil")
	}
	return m.MockSetPermissions(filePath, perm)
}

// User implements fs.UserFileSystem
func (m *MockFullyFeaturedFileSystem) User(filePath string) (string, error) {
	if m.MockUser == nil {
		panic("MockFullyFeaturedFileSystem.MockUser is nil")
	}
	return m.MockUser(filePath)
}

// SetUser implements fs.UserFileSystem
func (m *MockFullyFeaturedFileSystem) SetUser(filePath string, user string) error {
	if m.MockSetUser == nil {
		panic("MockFullyFeaturedFileSystem.MockSetUser is nil")
	}
	return m.MockSetUser(filePath, user)
}

// Group implements fs.GroupFileSystem
func (m *MockFullyFeaturedFileSystem) Group(filePath string) (string, error) {
	if m.MockGroup == nil {
		panic("MockFullyFeaturedFileSystem.MockGroup is nil")
	}
	return m.MockGroup(filePath)
}

// SetGroup implements fs.GroupFileSystem
func (m *MockFullyFeaturedFileSystem) SetGroup(filePath string, group string) error {
	if m.MockSetGroup == nil {
		panic("MockFullyFeaturedFileSystem.MockSetGroup is nil")
	}
	return m.MockSetGroup(filePath, group)
}

// IsSymbolicLink implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) IsSymbolicLink(filePath string) bool {
	if m.MockIsSymbolicLink == nil {
		panic("MockFullyFeaturedFileSystem.MockIsSymbolicLink is nil")
	}
	return m.MockIsSymbolicLink(filePath)
}

// CreateSymbolicLink implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if m.MockCreateSymbolicLink == nil {
		panic("MockFullyFeaturedFileSystem.MockCreateSymbolicLink is nil")
	}
	return m.MockCreateSymbolicLink(targetPath, linkPath)
}

// ReadSymbolicLink implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if m.MockReadSymbolicLink == nil {
		panic("MockFullyFeaturedFileSystem.MockReadSymbolicLink is nil")
	}
	return m.MockReadSymbolicLink(linkPath)
}

// ListXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) ListXAttr(filePath string, followSymlinks bool) ([]string, error) {
	if m.MockListXAttr == nil {
		panic("MockFullyFeaturedFileSystem.MockListXAttr is nil")
	}
	return m.MockListXAttr(filePath, followSymlinks)
}

// GetXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) GetXAttr(filePath string, name string, followSymlinks bool) ([]byte, error) {
	if m.MockGetXAttr == nil {
		panic("MockFullyFeaturedFileSystem.MockGetXAttr is nil")
	}
	return m.MockGetXAttr(filePath, name, followSymlinks)
}

// SetXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) SetXAttr(filePath string, name string, data []byte, flags int, followSymlinks bool) error {
	if m.MockSetXAttr == nil {
		panic("MockFullyFeaturedFileSystem.MockSetXAttr is nil")
	}
	return m.MockSetXAttr(filePath, name, data, flags, followSymlinks)
}

// RemoveXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) RemoveXAttr(filePath string, name string, followSymlinks bool) error {
	if m.MockRemoveXAttr == nil {
		panic("MockFullyFeaturedFileSystem.MockRemoveXAttr is nil")
	}
	return m.MockRemoveXAttr(filePath, name, followSymlinks)
}

// VolumeName implements fs.VolumeNameFileSystem
func (m *MockFullyFeaturedFileSystem) VolumeName(filePath string) string {
	if m.MockVolumeName == nil {
		panic("MockFullyFeaturedFileSystem.MockVolumeName is nil")
	}
	return m.MockVolumeName(filePath)
}

// IsAbsPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) IsAbsPath(filePath string) bool {
	if m.MockIsAbsPath == nil {
		panic("MockFullyFeaturedFileSystem.MockIsAbsPath is nil")
	}
	return m.MockIsAbsPath(filePath)
}

// AbsPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) AbsPath(filePath string) string {
	if m.MockAbsPath == nil {
		panic("MockFullyFeaturedFileSystem.MockAbsPath is nil")
	}
	return m.MockAbsPath(filePath)
}

// RelPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) RelPath(basePath, targPath string) (string, error) {
	if m.MockRelPath == nil {
		panic("MockFullyFeaturedFileSystem.MockRelPath is nil")
	}
	return m.MockRelPath(basePath, targPath)
}
