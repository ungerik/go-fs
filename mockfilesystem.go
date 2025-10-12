package fs

import (
	"context"
	iofs "io/fs"
)

var (
	// Ensure that MockFileSystem implements the FileSystem interface
	_ FileSystem = &MockFileSystem{}

	// Ensure that MockFileSystemFullyFeatured implements the FullyFeaturedFileSystem interface
	_ FullyFeaturedFileSystem = &MockFullyFeaturedFileSystem{}
)

// MockFileSystem is a FileSystem implementation
// with function pointers for every method.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding FileSystem method.
//
// If a function pointer is nil, then the corresponding method will panic,
// except for the Name and String methods, which will return "MockFileSystem"
// if the function pointer is nil.
type MockFileSystem struct {
	MockReadableWritable func() (readable, writable bool)
	MockRootDir          func() File
	MockID               func() (string, error)
	MockPrefix           func() string
	MockName             func() string
	MockString           func() string
	MockURL              func(cleanPath string) string
	MockCleanPathFromURI func(uri string) string
	MockJoinCleanFile    func(uriParts ...string) File
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
	MockListDirInfo      func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error
	MockMakeDir          func(dirPath string, perm []Permissions) error
	MockOpenReader       func(filePath string) (ReadCloser, error)
	MockOpenWriter       func(filePath string, perm []Permissions) (WriteCloser, error)
	MockOpenReadWriter   func(filePath string, perm []Permissions) (ReadWriteSeekCloser, error)
	MockRemove           func(filePath string) error
	MockClose            func() error
}

// MockFullyFeaturedFileSystem is a FullyFeaturedFileSystem implementation
// with function pointers for every method.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding FullyFeaturedFileSystem method.
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
	MockWatch                func(filePath string, onEvent func(File, Event)) (cancel func() error, err error)
	MockTouch                func(filePath string, perm []Permissions) error
	MockMakeAllDirs          func(dirPath string, perm []Permissions) error
	MockReadAll              func(ctx context.Context, filePath string) ([]byte, error)
	MockWriteAll             func(ctx context.Context, filePath string, data []byte, perm []Permissions) error
	MockAppend               func(ctx context.Context, filePath string, data []byte, perm []Permissions) error
	MockOpenAppendWriter     func(filePath string, perm []Permissions) (WriteCloser, error)
	MockTruncate             func(filePath string, size int64) error
	MockExists               func(filePath string) bool
	MockUser                 func(filePath string) (string, error)
	MockSetUser              func(filePath string, user string) error
	MockGroup                func(filePath string) (string, error)
	MockSetGroup             func(filePath string, group string) error
	MockSetPermissions       func(filePath string, perm Permissions) error
	MockListDirMax           func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error)
	MockListDirInfoRecursive func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error
}

// ReadableWritable implements FileSystem
func (m *MockFileSystem) ReadableWritable() (readable, writable bool) {
	if m.MockReadableWritable == nil {
		panic("MockFileSystem.MockReadableWritable is nil")
	}
	return m.MockReadableWritable()
}

// RootDir implements FileSystem
func (m *MockFileSystem) RootDir() File {
	if m.MockRootDir == nil {
		panic("MockFileSystem.MockRootDir is nil")
	}
	return m.MockRootDir()
}

// ID implements FileSystem
func (m *MockFileSystem) ID() (string, error) {
	if m.MockID == nil {
		panic("MockFileSystem.MockID is nil")
	}
	return m.MockID()
}

// Prefix implements FileSystem
func (m *MockFileSystem) Prefix() string {
	if m.MockPrefix == nil {
		panic("MockFileSystem.MockPrefix is nil")
	}
	return m.MockPrefix()
}

// Name implements FileSystem
func (m *MockFileSystem) Name() string {
	if m.MockName != nil {
		return m.MockName()
	}
	return "MockFileSystem"
}

// String implements FileSystem
func (m *MockFileSystem) String() string {
	if m.MockString != nil {
		return m.MockString()
	}
	return "MockFileSystem"
}

// URL implements FileSystem
func (m *MockFileSystem) URL(cleanPath string) string {
	if m.MockURL == nil {
		panic("MockFileSystem.MockURL is nil")
	}
	return m.MockURL(cleanPath)
}

// CleanPathFromURI implements FileSystem
func (m *MockFileSystem) CleanPathFromURI(uri string) string {
	if m.MockCleanPathFromURI == nil {
		panic("MockFileSystem.MockCleanPathFromURI is nil")
	}
	return m.MockCleanPathFromURI(uri)
}

// JoinCleanFile implements FileSystem
func (m *MockFileSystem) JoinCleanFile(uriParts ...string) File {
	if m.MockJoinCleanFile == nil {
		panic("MockFileSystem.MockJoinCleanFile is nil")
	}
	return m.MockJoinCleanFile(uriParts...)
}

// JoinCleanPath implements FileSystem
func (m *MockFileSystem) JoinCleanPath(uriParts ...string) string {
	if m.MockJoinCleanPath == nil {
		panic("MockFileSystem.MockJoinCleanPath is nil")
	}
	return m.MockJoinCleanPath(uriParts...)
}

// SplitPath implements FileSystem
func (m *MockFileSystem) SplitPath(filePath string) []string {
	if m.MockSplitPath == nil {
		panic("MockFileSystem.MockSplitPath is nil")
	}
	return m.MockSplitPath(filePath)
}

// Separator implements FileSystem
func (m *MockFileSystem) Separator() string {
	if m.MockSeparator == nil {
		panic("MockFileSystem.MockSeparator is nil")
	}
	return m.MockSeparator()
}

// IsAbsPath implements FileSystem
func (m *MockFileSystem) IsAbsPath(filePath string) bool {
	if m.MockIsAbsPath == nil {
		panic("MockFileSystem.MockIsAbsPath is nil")
	}
	return m.MockIsAbsPath(filePath)
}

// AbsPath implements FileSystem
func (m *MockFileSystem) AbsPath(filePath string) string {
	if m.MockAbsPath == nil {
		panic("MockFileSystem.MockAbsPath is nil")
	}
	return m.MockAbsPath(filePath)
}

// MatchAnyPattern implements FileSystem
func (m *MockFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	if m.MockMatchAnyPattern == nil {
		panic("MockFileSystem.MockMatchAnyPattern is nil")
	}
	return m.MockMatchAnyPattern(name, patterns)
}

// SplitDirAndName implements FileSystem
func (m *MockFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	if m.MockSplitDirAndName == nil {
		panic("MockFileSystem.MockSplitDirAndName is nil")
	}
	return m.MockSplitDirAndName(filePath)
}

// Stat implements FileSystem
func (m *MockFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	if m.MockStat == nil {
		panic("MockFileSystem.MockStat is nil")
	}
	return m.MockStat(filePath)
}

// IsHidden implements FileSystem
func (m *MockFileSystem) IsHidden(filePath string) bool {
	if m.MockIsHidden == nil {
		panic("MockFileSystem.MockIsHidden is nil")
	}
	return m.MockIsHidden(filePath)
}

// IsSymbolicLink implements FileSystem
func (m *MockFileSystem) IsSymbolicLink(filePath string) bool {
	if m.MockIsSymbolicLink == nil {
		panic("MockFileSystem.MockIsSymbolicLink is nil")
	}
	return m.MockIsSymbolicLink(filePath)
}

// ListDirInfo implements FileSystem
func (m *MockFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	if m.MockListDirInfo == nil {
		panic("MockFileSystem.MockListDirInfo is nil")
	}
	return m.MockListDirInfo(ctx, dirPath, callback, patterns)
}

// MakeDir implements FileSystem
func (m *MockFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	if m.MockMakeDir == nil {
		panic("MockFileSystem.MockMakeDir is nil")
	}
	return m.MockMakeDir(dirPath, perm)
}

// OpenReader implements FileSystem
func (m *MockFileSystem) OpenReader(filePath string) (ReadCloser, error) {
	if m.MockOpenReader == nil {
		panic("MockFileSystem.MockOpenReader is nil")
	}
	return m.MockOpenReader(filePath)
}

// OpenWriter implements FileSystem
func (m *MockFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if m.MockOpenWriter == nil {
		panic("MockFileSystem.MockOpenWriter is nil")
	}
	return m.MockOpenWriter(filePath, perm)
}

// OpenReadWriter implements FileSystem
func (m *MockFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	if m.MockOpenReadWriter == nil {
		panic("MockFileSystem.MockOpenReadWriter is nil")
	}
	return m.MockOpenReadWriter(filePath, perm)
}

// Remove implements FileSystem
func (m *MockFileSystem) Remove(filePath string) error {
	if m.MockRemove == nil {
		panic("MockFileSystem.MockRemove is nil")
	}
	return m.MockRemove(filePath)
}

// Close implements FileSystem
func (m *MockFileSystem) Close() error {
	if m.MockClose == nil {
		panic("MockFileSystem.MockClose is nil")
	}
	return m.MockClose()
}

// CopyFile implements CopyFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if m.MockCopyFile == nil {
		panic("MockFileSystemFullyFeatured.MockCopyFile is nil")
	}
	return m.MockCopyFile(ctx, srcFile, destFile, buf)
}

// Move implements MoveFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Move(filePath string, destinationPath string) error {
	if m.MockMove == nil {
		panic("MockFileSystemFullyFeatured.MockMove is nil")
	}
	return m.MockMove(filePath, destinationPath)
}

// Rename implements RenameFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Rename(filePath string, newName string) (newPath string, err error) {
	if m.MockRename == nil {
		panic("MockFileSystemFullyFeatured.MockRename is nil")
	}
	return m.MockRename(filePath, newName)
}

// VolumeName implements VolumeNameFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) VolumeName(filePath string) string {
	if m.MockVolumeName == nil {
		panic("MockFileSystemFullyFeatured.MockVolumeName is nil")
	}
	return m.MockVolumeName(filePath)
}

// Watch implements WatchFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if m.MockWatch == nil {
		panic("MockFileSystemFullyFeatured.MockWatch is nil")
	}
	return m.MockWatch(filePath, onEvent)
}

// Touch implements TouchFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Touch(filePath string, perm []Permissions) error {
	if m.MockTouch == nil {
		panic("MockFileSystemFullyFeatured.MockTouch is nil")
	}
	return m.MockTouch(filePath, perm)
}

// MakeAllDirs implements MakeAllDirsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) MakeAllDirs(dirPath string, perm []Permissions) error {
	if m.MockMakeAllDirs == nil {
		panic("MockFileSystemFullyFeatured.MockMakeAllDirs is nil")
	}
	return m.MockMakeAllDirs(dirPath, perm)
}

// ReadAll implements ReadAllFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if m.MockReadAll == nil {
		panic("MockFileSystemFullyFeatured.MockReadAll is nil")
	}
	return m.MockReadAll(ctx, filePath)
}

// WriteAll implements WriteAllFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	if m.MockWriteAll == nil {
		panic("MockFileSystemFullyFeatured.MockWriteAll is nil")
	}
	return m.MockWriteAll(ctx, filePath, data, perm)
}

// Append implements AppendFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	if m.MockAppend == nil {
		panic("MockFileSystemFullyFeatured.MockAppend is nil")
	}
	return m.MockAppend(ctx, filePath, data, perm)
}

// OpenAppendWriter implements AppendWriterFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	if m.MockOpenAppendWriter == nil {
		panic("MockFileSystemFullyFeatured.MockOpenAppendWriter is nil")
	}
	return m.MockOpenAppendWriter(filePath, perm)
}

// Truncate implements TruncateFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Truncate(filePath string, size int64) error {
	if m.MockTruncate == nil {
		panic("MockFileSystemFullyFeatured.MockTruncate is nil")
	}
	return m.MockTruncate(filePath, size)
}

// Exists implements ExistsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Exists(filePath string) bool {
	if m.MockExists == nil {
		panic("MockFileSystemFullyFeatured.MockExists is nil")
	}
	return m.MockExists(filePath)
}

// User implements UserFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) User(filePath string) (string, error) {
	if m.MockUser == nil {
		panic("MockFileSystemFullyFeatured.MockUser is nil")
	}
	return m.MockUser(filePath)
}

// SetUser implements UserFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetUser(filePath string, user string) error {
	if m.MockSetUser == nil {
		panic("MockFileSystemFullyFeatured.MockSetUser is nil")
	}
	return m.MockSetUser(filePath, user)
}

// Group implements GroupFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) Group(filePath string) (string, error) {
	if m.MockGroup == nil {
		panic("MockFileSystemFullyFeatured.MockGroup is nil")
	}
	return m.MockGroup(filePath)
}

// SetGroup implements GroupFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetGroup(filePath string, group string) error {
	if m.MockSetGroup == nil {
		panic("MockFileSystemFullyFeatured.MockSetGroup is nil")
	}
	return m.MockSetGroup(filePath, group)
}

// SetPermissions implements PermissionsFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if m.MockSetPermissions == nil {
		panic("MockFileSystemFullyFeatured.MockSetPermissions is nil")
	}
	return m.MockSetPermissions(filePath, perm)
}

// ListDirMax implements ListDirMaxFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
	if m.MockListDirMax == nil {
		panic("MockFileSystemFullyFeatured.MockListDirMax is nil")
	}
	return m.MockListDirMax(ctx, dirPath, max, patterns)
}

// ListDirInfoRecursive implements ListDirRecursiveFileSystem (optional)
func (m *MockFullyFeaturedFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	if m.MockListDirInfoRecursive == nil {
		panic("MockFileSystemFullyFeatured.MockListDirInfoRecursive is nil")
	}
	return m.MockListDirInfoRecursive(ctx, dirPath, callback, patterns)
}
