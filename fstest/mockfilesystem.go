// Package fstest provides testing helpers for github.com/ungerik/go-fs,
// analogous to the standard library's testing/fstest package.
//
// MockFileSystem and MockFullyFeaturedFileSystem implement the go-fs
// FileSystem interfaces with per-method function pointers, so tests can
// control individual behaviors without a real backing file system.
package fstest

import (
	"context"
	iofs "io/fs"

	fs "github.com/ungerik/go-fs"
)

var (
	// Ensure that MockFileSystem implements the fs.FileSystem interface
	_ fs.FileSystem = &MockFileSystem{}

	// Ensure that MockFullyFeaturedFileSystem implements the fs.FullyFeaturedFileSystem interface
	_ fs.FullyFeaturedFileSystem = &MockFullyFeaturedFileSystem{}
)

// MockFileSystem is a fs.FileSystem implementation
// with function pointers for every method.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding fs.FileSystem method.
//
// If a function pointer is nil, then the corresponding method will panic,
// except for the Name and String methods, which will return "MockFileSystem"
// if the function pointer is nil.
type MockFileSystem struct {
	// MockPrefix is the prefix string returned by the Prefix() method.
	// If empty, defaults to "mock://".
	MockPrefix string

	MockReadableWritable func() (readable, writable bool)
	MockRootDir          func() fs.File
	MockID               func() (string, error)
	MockName             func() string
	MockString           func() string
	MockURL              func(cleanPath string) string
	MockCleanPathFromURI func(uri string) string
	MockJoinCleanFile    func(uriParts ...string) fs.File
	MockJoinCleanPath    func(uriParts ...string) string
	MockSplitPath        func(filePath string) []string
	MockSeparator        func() string
	MockIsAbsPath        func(filePath string) bool
	MockAbsPath          func(filePath string) string
	MockMatchAnyPattern  func(name string, patterns []string) (bool, error)
	MockSplitDirAndName  func(filePath string) (dir, name string)
	MockStat             func(filePath string) (iofs.FileInfo, error)
	MockIsHidden         func(filePath string) bool
	MockIsSymbolicLink   func(filePath string) bool
	MockListDirInfo      func(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error
	MockMakeDir          func(dirPath string, perm []fs.Permissions) error
	MockOpenReader       func(filePath string) (fs.ReadCloser, error)
	MockOpenWriter       func(filePath string, perm []fs.Permissions) (fs.WriteCloser, error)
	MockOpenReadWriter   func(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error)
	MockRemove           func(filePath string) error
	MockClose            func() error
}

// MockFullyFeaturedFileSystem is a fs.FullyFeaturedFileSystem implementation
// with function pointers for every method.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding fs.FullyFeaturedFileSystem method.
//
// If a function pointer is nil, then the corresponding method will panic,
// except for the Name and String methods, which will return "MockFileSystem"
// if the function pointer is nil.
type MockFullyFeaturedFileSystem struct {
	MockFileSystem

	MockCopyFile             func(ctx context.Context, srcFile string, destFile string, buf *[]byte) error
	MockMove                 func(filePath string, destinationPath string) error
	MockRename               func(filePath string, newName string) (newPath string, err error)
	MockVolumeName           func(filePath string) string
	MockWatch                func(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error)
	MockTouch                func(filePath string, perm []fs.Permissions) error
	MockMakeAllDirs          func(dirPath string, perm []fs.Permissions) error
	MockReadAll              func(ctx context.Context, filePath string) ([]byte, error)
	MockWriteAll             func(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error
	MockAppend               func(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error
	MockOpenAppendWriter     func(filePath string, perm []fs.Permissions) (fs.WriteCloser, error)
	MockTruncate             func(filePath string, size int64) error
	MockExists               func(filePath string) bool
	MockUser                 func(filePath string) (string, error)
	MockSetUser              func(filePath string, user string) error
	MockGroup                func(filePath string) (string, error)
	MockSetGroup             func(filePath string, group string) error
	MockSetPermissions       func(filePath string, perm fs.Permissions) error
	MockListDirMax           func(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error)
	MockListDirInfoRecursive func(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error
}

// ReadableWritable implements fs.FileSystem
func (m *MockFileSystem) ReadableWritable() (readable, writable bool) {
	if m.MockReadableWritable == nil {
		panic("MockFileSystem.MockReadableWritable is nil")
	}
	return m.MockReadableWritable()
}

// RootDir implements fs.FileSystem
func (m *MockFileSystem) RootDir() fs.File {
	if m.MockRootDir == nil {
		panic("MockFileSystem.MockRootDir is nil")
	}
	return m.MockRootDir()
}

// ID implements fs.FileSystem
func (m *MockFileSystem) ID() (string, error) {
	if m.MockID == nil {
		panic("MockFileSystem.MockID is nil")
	}
	return m.MockID()
}

// Prefix implements fs.FileSystem.
// Returns the value of MockPrefix if not empty, otherwise returns "mock://".
func (m *MockFileSystem) Prefix() string {
	if m.MockPrefix == "" {
		return "mock://"
	}
	return m.MockPrefix
}

// Name implements fs.FileSystem.
// Calls MockName if not nil, otherwise returns "MockFileSystem".
func (m *MockFileSystem) Name() string {
	if m.MockName != nil {
		return m.MockName()
	}
	return "MockFileSystem"
}

// String implements fs.FileSystem.
// Calls MockString if not nil, otherwise returns "MockFileSystem".
func (m *MockFileSystem) String() string {
	if m.MockString != nil {
		return m.MockString()
	}
	return "MockFileSystem"
}

// URL implements fs.FileSystem
func (m *MockFileSystem) URL(cleanPath string) string {
	if m.MockURL == nil {
		panic("MockFileSystem.MockURL is nil")
	}
	return m.MockURL(cleanPath)
}

// CleanPathFromURI implements fs.FileSystem
func (m *MockFileSystem) CleanPathFromURI(uri string) string {
	if m.MockCleanPathFromURI == nil {
		panic("MockFileSystem.MockCleanPathFromURI is nil")
	}
	return m.MockCleanPathFromURI(uri)
}

// JoinCleanFile implements fs.FileSystem
func (m *MockFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	if m.MockJoinCleanFile == nil {
		panic("MockFileSystem.MockJoinCleanFile is nil")
	}
	return m.MockJoinCleanFile(uriParts...)
}

// JoinCleanPath implements fs.FileSystem
func (m *MockFileSystem) JoinCleanPath(uriParts ...string) string {
	if m.MockJoinCleanPath == nil {
		panic("MockFileSystem.MockJoinCleanPath is nil")
	}
	return m.MockJoinCleanPath(uriParts...)
}

// SplitPath implements fs.FileSystem
func (m *MockFileSystem) SplitPath(filePath string) []string {
	if m.MockSplitPath == nil {
		panic("MockFileSystem.MockSplitPath is nil")
	}
	return m.MockSplitPath(filePath)
}

// Separator implements fs.FileSystem
func (m *MockFileSystem) Separator() string {
	if m.MockSeparator == nil {
		panic("MockFileSystem.MockSeparator is nil")
	}
	return m.MockSeparator()
}

// IsAbsPath implements fs.FileSystem
func (m *MockFileSystem) IsAbsPath(filePath string) bool {
	if m.MockIsAbsPath == nil {
		panic("MockFileSystem.MockIsAbsPath is nil")
	}
	return m.MockIsAbsPath(filePath)
}

// AbsPath implements fs.FileSystem
func (m *MockFileSystem) AbsPath(filePath string) string {
	if m.MockAbsPath == nil {
		panic("MockFileSystem.MockAbsPath is nil")
	}
	return m.MockAbsPath(filePath)
}

// MatchAnyPattern implements fs.FileSystem
func (m *MockFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	if m.MockMatchAnyPattern == nil {
		panic("MockFileSystem.MockMatchAnyPattern is nil")
	}
	return m.MockMatchAnyPattern(name, patterns)
}

// SplitDirAndName implements fs.FileSystem
func (m *MockFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	if m.MockSplitDirAndName == nil {
		panic("MockFileSystem.MockSplitDirAndName is nil")
	}
	return m.MockSplitDirAndName(filePath)
}

// Stat implements fs.FileSystem
func (m *MockFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	if m.MockStat == nil {
		panic("MockFileSystem.MockStat is nil")
	}
	return m.MockStat(filePath)
}

// IsHidden implements fs.FileSystem
func (m *MockFileSystem) IsHidden(filePath string) bool {
	if m.MockIsHidden == nil {
		panic("MockFileSystem.MockIsHidden is nil")
	}
	return m.MockIsHidden(filePath)
}

// IsSymbolicLink implements fs.FileSystem
func (m *MockFileSystem) IsSymbolicLink(filePath string) bool {
	if m.MockIsSymbolicLink == nil {
		panic("MockFileSystem.MockIsSymbolicLink is nil")
	}
	return m.MockIsSymbolicLink(filePath)
}

// ListDirInfo implements fs.FileSystem
func (m *MockFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	if m.MockListDirInfo == nil {
		panic("MockFileSystem.MockListDirInfo is nil")
	}
	return m.MockListDirInfo(ctx, dirPath, callback, patterns)
}

// MakeDir implements fs.FileSystem
func (m *MockFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	if m.MockMakeDir == nil {
		panic("MockFileSystem.MockMakeDir is nil")
	}
	return m.MockMakeDir(dirPath, perm)
}

// OpenReader implements fs.FileSystem
func (m *MockFileSystem) OpenReader(filePath string) (fs.ReadCloser, error) {
	if m.MockOpenReader == nil {
		panic("MockFileSystem.MockOpenReader is nil")
	}
	return m.MockOpenReader(filePath)
}

// OpenWriter implements fs.FileSystem
func (m *MockFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if m.MockOpenWriter == nil {
		panic("MockFileSystem.MockOpenWriter is nil")
	}
	return m.MockOpenWriter(filePath, perm)
}

// OpenReadWriter implements fs.FileSystem
func (m *MockFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if m.MockOpenReadWriter == nil {
		panic("MockFileSystem.MockOpenReadWriter is nil")
	}
	return m.MockOpenReadWriter(filePath, perm)
}

// Remove implements fs.FileSystem
func (m *MockFileSystem) Remove(filePath string) error {
	if m.MockRemove == nil {
		panic("MockFileSystem.MockRemove is nil")
	}
	return m.MockRemove(filePath)
}

// Close implements fs.FileSystem
func (m *MockFileSystem) Close() error {
	if m.MockClose == nil {
		panic("MockFileSystem.MockClose is nil")
	}
	return m.MockClose()
}

// CopyFile implements fs.CopyFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if m.MockCopyFile == nil {
		panic("MockFullyFeaturedFileSystem.MockCopyFile is nil")
	}
	return m.MockCopyFile(ctx, srcFile, destFile, buf)
}

// Move implements fs.MoveFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Move(filePath string, destinationPath string) error {
	if m.MockMove == nil {
		panic("MockFullyFeaturedFileSystem.MockMove is nil")
	}
	return m.MockMove(filePath, destinationPath)
}

// Rename implements fs.RenameFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Rename(filePath string, newName string) (newPath string, err error) {
	if m.MockRename == nil {
		panic("MockFullyFeaturedFileSystem.MockRename is nil")
	}
	return m.MockRename(filePath, newName)
}

// VolumeName implements fs.VolumeNameFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) VolumeName(filePath string) string {
	if m.MockVolumeName == nil {
		panic("MockFullyFeaturedFileSystem.MockVolumeName is nil")
	}
	return m.MockVolumeName(filePath)
}

// Watch implements fs.WatchFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	if m.MockWatch == nil {
		panic("MockFullyFeaturedFileSystem.MockWatch is nil")
	}
	return m.MockWatch(filePath, onEvent)
}

// Touch implements fs.TouchFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if m.MockTouch == nil {
		panic("MockFullyFeaturedFileSystem.MockTouch is nil")
	}
	return m.MockTouch(filePath, perm)
}

// MakeAllDirs implements fs.MakeAllDirsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) MakeAllDirs(dirPath string, perm []fs.Permissions) error {
	if m.MockMakeAllDirs == nil {
		panic("MockFullyFeaturedFileSystem.MockMakeAllDirs is nil")
	}
	return m.MockMakeAllDirs(dirPath, perm)
}

// ReadAll implements fs.ReadAllFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if m.MockReadAll == nil {
		panic("MockFullyFeaturedFileSystem.MockReadAll is nil")
	}
	return m.MockReadAll(ctx, filePath)
}

// WriteAll implements fs.WriteAllFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if m.MockWriteAll == nil {
		panic("MockFullyFeaturedFileSystem.MockWriteAll is nil")
	}
	return m.MockWriteAll(ctx, filePath, data, perm)
}

// Append implements fs.AppendFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if m.MockAppend == nil {
		panic("MockFullyFeaturedFileSystem.MockAppend is nil")
	}
	return m.MockAppend(ctx, filePath, data, perm)
}

// OpenAppendWriter implements fs.AppendWriterFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if m.MockOpenAppendWriter == nil {
		panic("MockFullyFeaturedFileSystem.MockOpenAppendWriter is nil")
	}
	return m.MockOpenAppendWriter(filePath, perm)
}

// Truncate implements fs.TruncateFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Truncate(filePath string, size int64) error {
	if m.MockTruncate == nil {
		panic("MockFullyFeaturedFileSystem.MockTruncate is nil")
	}
	return m.MockTruncate(filePath, size)
}

// Exists implements fs.ExistsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Exists(filePath string) bool {
	if m.MockExists == nil {
		panic("MockFullyFeaturedFileSystem.MockExists is nil")
	}
	return m.MockExists(filePath)
}

// User implements fs.UserFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) User(filePath string) (string, error) {
	if m.MockUser == nil {
		panic("MockFullyFeaturedFileSystem.MockUser is nil")
	}
	return m.MockUser(filePath)
}

// SetUser implements fs.UserFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetUser(filePath string, user string) error {
	if m.MockSetUser == nil {
		panic("MockFullyFeaturedFileSystem.MockSetUser is nil")
	}
	return m.MockSetUser(filePath, user)
}

// Group implements fs.GroupFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Group(filePath string) (string, error) {
	if m.MockGroup == nil {
		panic("MockFullyFeaturedFileSystem.MockGroup is nil")
	}
	return m.MockGroup(filePath)
}

// SetGroup implements fs.GroupFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetGroup(filePath string, group string) error {
	if m.MockSetGroup == nil {
		panic("MockFullyFeaturedFileSystem.MockSetGroup is nil")
	}
	return m.MockSetGroup(filePath, group)
}

// SetPermissions implements fs.PermissionsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	if m.MockSetPermissions == nil {
		panic("MockFullyFeaturedFileSystem.MockSetPermissions is nil")
	}
	return m.MockSetPermissions(filePath, perm)
}

// ListDirMax implements fs.ListDirMaxFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]fs.File, error) {
	if m.MockListDirMax == nil {
		panic("MockFullyFeaturedFileSystem.MockListDirMax is nil")
	}
	return m.MockListDirMax(ctx, dirPath, max, patterns)
}

// ListDirInfoRecursive implements fs.ListDirRecursiveFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	if m.MockListDirInfoRecursive == nil {
		panic("MockFullyFeaturedFileSystem.MockListDirInfoRecursive is nil")
	}
	return m.MockListDirInfoRecursive(ctx, dirPath, callback, patterns)
}
