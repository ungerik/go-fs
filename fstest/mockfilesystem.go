package fstest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ fs.FileSystem      = &MockFileSystem{}
	_ fs.WriteFileSystem = &MockFileSystem{}

	_ fs.PrefixAliasFileSystem      = &MockFullyFeaturedFileSystem{}
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
	_ fs.ValidNameFileSystem        = &MockFullyFeaturedFileSystem{}
)

// MockFileSystem is a fs.FileSystem and fs.WriteFileSystem implementation
// with function pointers for every method, and nothing else, so the
// package uses its emulations for every optional interface.
//
// All function pointers are prefixed with "Mock" and use the same calling interface
// as their corresponding method.
//
// A nil function pointer selects the default behavior of the method:
//   - Prefix returns MockPrefix or "mock://" if that is empty
//   - Name and String return "MockFileSystem"
//   - Separator returns "/"
//   - ID returns Prefix()
//   - CleanPath is implemented by a fsimpl.PathHelper for the prefix and separator
//   - RootDir returns the root of the prefix
//   - ReadableWritable returns true, true
//   - Close unregisters the file system and returns nil
//   - Stat, ListDir, OpenReader, OpenWriter, MakeDir and Remove return
//     an error wrapping errors.ErrUnsupported, because there is nothing
//     they could be emulated with
//
// Close always unregisters the file system, as required by fs.FileSystem,
// before it returns the result of MockClose.
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
// implements every optional file system interface of package fs
// with function pointers for every method.
//
// A nil function pointer falls back to an emulation built from the
// other methods, like the package emulates the optional interfaces for
// file systems that don't implement them, so a test only sets the
// methods it wants to control:
//   - PrefixAliases returns no aliases
//   - Exists, ReadAll, WriteAll, Append, OpenAppendWriter, OpenReadWriter,
//     Truncate, MakeAllDirs, RemoveAll, CopyFile, Move and ListDirRecursive
//     are emulated with the methods of MockFileSystem and with each other
//   - Touch creates a missing file and returns an error wrapping
//     errors.ErrUnsupported for an existing one
//   - IsHidden reports names beginning with a dot
//   - ValidName accepts every name
//   - Lstat returns Stat
//   - VolumeName returns "", IsAbsPath returns true, AbsPath returns
//     the path and RelPath computes the relative path of the elements
//   - Watch, SetPermissions, User, SetUser, Group, SetGroup,
//     CreateSymbolicLink, ReadSymbolicLink and the extended attribute
//     methods return an error wrapping errors.ErrUnsupported,
//     because the package has no emulation for them either
//
// The emulated Move follows the overwrite semantics of fs.MoveFileSystem.
//
// fs.URIFileSystem is deliberately not implemented: a file system that
// implements it maps its URIs to paths itself and the package stops
// decoding percent-encoded characters, which would give the mock another path model
// than every other file system. Embed the mock in a type with the three
// methods of fs.URIFileSystem to test that path model.
type MockFullyFeaturedFileSystem struct {
	MockFileSystem

	MockPrefixAliases    func() []string
	MockExists           func(filePath string) (bool, error)
	MockReadAll          func(ctx context.Context, filePath string) ([]byte, error)
	MockWriteAll         func(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error
	MockAppend           func(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error
	MockOpenAppendWriter func(filePath string, perm fs.Permissions) (io.WriteCloser, error)
	MockOpenReadWriter   func(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error)
	MockTruncate         func(ctx context.Context, filePath string, size int64) error
	MockTouch            func(filePath string, perm fs.Permissions) error
	MockMakeAllDirs      func(dirPath string, perm fs.Permissions) error
	MockRemoveAll        func(ctx context.Context, filePath string) error
	MockCopyFile         func(ctx context.Context, srcFile string, destFile string) error
	MockMove             func(ctx context.Context, filePath string, destPath string) error
	MockListDirRecursive func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error
	MockWatch            func(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error)
	MockIsHidden         func(filePath string) bool
	MockSetPermissions   func(filePath string, perm fs.Permissions) error
	MockUser             func(filePath string) (string, error)
	MockSetUser          func(filePath string, user string) error
	MockGroup            func(filePath string) (string, error)
	MockSetGroup         func(filePath string, group string) error

	MockLstat              func(filePath string) (*fs.FileInfo, error)
	MockCreateSymbolicLink func(targetPath, linkPath string) error
	MockReadSymbolicLink   func(linkPath string) (targetPath string, err error)

	MockListXAttr   func(filePath string, followSymlinks bool) ([]string, error)
	MockGetXAttr    func(filePath string, name string, followSymlinks bool) ([]byte, error)
	MockSetXAttr    func(filePath string, name string, data []byte, flags fs.XAttrFlags, followSymlinks bool) error
	MockRemoveXAttr func(filePath string, name string, followSymlinks bool) error

	MockVolumeName func(filePath string) string
	MockIsAbsPath  func(filePath string) bool
	MockAbsPath    func(filePath string) string
	MockRelPath    func(basePath, targPath string) (string, error)

	MockValidName func(name string) error
}

func (m *MockFileSystem) pathHelper() fsimpl.PathHelper {
	return fsimpl.PathHelper{URIPrefix: m.Prefix(), PathSep: m.Separator(), Rooted: true}
}

// file returns the File of a clean path of the mock.
func (m *MockFileSystem) file(filePath string) fs.File {
	return fs.File(m.pathHelper().URI(filePath))
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
		return nil, fs.NewErrUnsupported(m, "Stat")
	}
	return m.MockStat(filePath)
}

// ListDir implements fs.FileSystem
func (m *MockFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if m.MockListDir == nil {
		return fs.NewErrUnsupported(m, "ListDir")
	}
	return m.MockListDir(ctx, dirPath, patterns, callback)
}

// OpenReader implements fs.FileSystem
func (m *MockFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if m.MockOpenReader == nil {
		return nil, fs.NewErrUnsupported(m, "OpenReader")
	}
	return m.MockOpenReader(filePath)
}

// Close implements fs.FileSystem by unregistering the file system
// and returning the result of MockClose, or nil without MockClose.
func (m *MockFileSystem) Close() error {
	fs.Unregister(m)
	return m.close()
}

func (m *MockFileSystem) close() error {
	if m.MockClose == nil {
		return nil
	}
	return m.MockClose()
}

// OpenWriter implements fs.WriteFileSystem
func (m *MockFileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if m.MockOpenWriter == nil {
		return nil, fs.NewErrUnsupported(m, "OpenWriter")
	}
	return m.MockOpenWriter(filePath, perm)
}

// MakeDir implements fs.WriteFileSystem
func (m *MockFileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	if m.MockMakeDir == nil {
		return fs.NewErrUnsupported(m, "MakeDir")
	}
	return m.MockMakeDir(dirPath, perm)
}

// Remove implements fs.WriteFileSystem
func (m *MockFileSystem) Remove(filePath string) error {
	if m.MockRemove == nil {
		return fs.NewErrUnsupported(m, "Remove")
	}
	return m.MockRemove(filePath)
}

// Close implements fs.FileSystem by unregistering the file system
// and returning the result of MockClose, or nil without MockClose.
func (m *MockFullyFeaturedFileSystem) Close() error {
	fs.Unregister(m)
	return m.close()
}

// PrefixAliases implements fs.PrefixAliasFileSystem
func (m *MockFullyFeaturedFileSystem) PrefixAliases() []string {
	if m.MockPrefixAliases == nil {
		return nil
	}
	return m.MockPrefixAliases()
}

// Exists implements fs.ExistsFileSystem
func (m *MockFullyFeaturedFileSystem) Exists(filePath string) (bool, error) {
	if m.MockExists != nil {
		return m.MockExists(filePath)
	}
	_, err := m.Stat(filePath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, err
}

// ReadAll implements fs.ReadAllFileSystem
func (m *MockFullyFeaturedFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if m.MockReadAll != nil {
		return m.MockReadAll(ctx, filePath)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := m.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return fs.ReadAll(ctx, r)
}

// readAllOrNew returns the content of filePath, or nil if it does not exist.
func (m *MockFullyFeaturedFileSystem) readAllOrNew(ctx context.Context, filePath string) ([]byte, error) {
	data, err := m.ReadAll(ctx, filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// WriteAll implements fs.WriteAllFileSystem
func (m *MockFullyFeaturedFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if m.MockWriteAll != nil {
		return m.MockWriteAll(ctx, filePath, data, perm)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w, err := m.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	err = fs.WriteAll(ctx, w, data)
	if aborter, ok := w.(fs.WriteAborter); ok && err != nil {
		err = errors.Join(err, aborter.Abort())
	}
	return errors.Join(err, w.Close())
}

// Append implements fs.AppendFileSystem
func (m *MockFullyFeaturedFileSystem) Append(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if m.MockAppend != nil {
		return m.MockAppend(ctx, filePath, data, perm)
	}
	current, err := m.readAllOrNew(ctx, filePath)
	if err != nil {
		return err
	}
	return m.WriteAll(ctx, filePath, append(current, data...), perm)
}

// OpenAppendWriter implements fs.AppendWriterFileSystem
func (m *MockFullyFeaturedFileSystem) OpenAppendWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if m.MockOpenAppendWriter != nil {
		return m.MockOpenAppendWriter(filePath, perm)
	}
	current, err := m.readAllOrNew(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	buf := fsimpl.NewWriteOnCloseFileBuffer(current, func(data []byte) error {
		return m.WriteAll(context.Background(), filePath, data, perm)
	})
	// Writes must append after the current content
	_, err = buf.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// OpenReadWriter implements fs.ReadWriterFileSystem
func (m *MockFullyFeaturedFileSystem) OpenReadWriter(filePath string, perm fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	if m.MockOpenReadWriter != nil {
		return m.MockOpenReadWriter(filePath, perm)
	}
	// Read when opening, so that errors are returned by OpenReadWriter
	current, err := m.readAllOrNew(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	return fsimpl.NewReadWriteAllSeekCloser(
		func() ([]byte, error) {
			return current, nil
		},
		func(data []byte) error {
			return m.WriteAll(context.Background(), filePath, data, perm)
		},
		nil,
	), nil
}

// Truncate implements fs.TruncateFileSystem
func (m *MockFullyFeaturedFileSystem) Truncate(ctx context.Context, filePath string, size int64) error {
	if m.MockTruncate != nil {
		return m.MockTruncate(ctx, filePath, size)
	}
	if size < 0 {
		return fmt.Errorf("negative file size: %d", size)
	}
	data, err := m.ReadAll(ctx, filePath)
	if err != nil {
		return err
	}
	if int64(len(data)) > size {
		data = data[:size]
	} else {
		data = append(data, make([]byte, size-int64(len(data)))...)
	}
	return m.WriteAll(ctx, filePath, data, 0)
}

// Touch implements fs.TouchFileSystem
func (m *MockFullyFeaturedFileSystem) Touch(filePath string, perm fs.Permissions) error {
	if m.MockTouch != nil {
		return m.MockTouch(filePath, perm)
	}
	exists, err := m.Exists(filePath)
	if err != nil {
		return err
	}
	if exists {
		return fs.NewErrUnsupported(m, "Touch of an existing file")
	}
	w, err := m.OpenWriter(filePath, perm)
	if err != nil {
		return err
	}
	return w.Close()
}

// MakeAllDirs implements fs.MakeAllDirsFileSystem
func (m *MockFullyFeaturedFileSystem) MakeAllDirs(dirPath string, perm fs.Permissions) error {
	if m.MockMakeAllDirs != nil {
		return m.MockMakeAllDirs(dirPath, perm)
	}
	if info, err := m.Stat(dirPath); err == nil {
		if !info.IsDir {
			return fs.NewErrIsNotDirectory(m.file(dirPath))
		}
		return nil
	}
	parent, name := m.pathHelper().SplitDirAndName(dirPath)
	if name != "" && parent != dirPath {
		if err := m.MakeAllDirs(parent, perm); err != nil {
			return err
		}
	}
	return m.MakeDir(dirPath, perm)
}

// RemoveAll implements fs.RemoveAllFileSystem
func (m *MockFullyFeaturedFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if m.MockRemoveAll != nil {
		return m.MockRemoveAll(ctx, filePath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := m.Stat(filePath)
	if err != nil {
		return fs.RemoveErrDoesNotExist(err)
	}
	if info.IsDir {
		err = m.ListDir(ctx, filePath, nil, func(entry *fs.FileInfo) error {
			return m.RemoveAll(ctx, m.CleanPath(filePath, entry.Name))
		})
		if err != nil {
			return err
		}
	}
	return fs.RemoveErrDoesNotExist(m.Remove(filePath))
}

// CopyFile implements fs.CopyFileSystem
func (m *MockFullyFeaturedFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if m.MockCopyFile != nil {
		return m.MockCopyFile(ctx, srcFile, destFile)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if srcFile == destFile {
		return nil
	}
	if info, err := m.Stat(destFile); err == nil && info.IsDir {
		return fs.NewErrIsDirectory(m.file(destFile))
	}
	data, err := m.ReadAll(ctx, srcFile)
	if err != nil {
		return err
	}
	return m.WriteAll(ctx, destFile, data, 0)
}

// Move implements fs.MoveFileSystem
func (m *MockFullyFeaturedFileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if m.MockMove != nil {
		return m.MockMove(ctx, filePath, destPath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if filePath == destPath {
		return nil
	}
	info, err := m.Stat(filePath)
	if err != nil {
		return err
	}
	if destInfo, err := m.Stat(destPath); err == nil {
		switch {
		case destInfo.IsDir:
			return fs.NewErrAlreadyExists(m.file(destPath))
		case info.IsDir:
			return fs.NewErrIsNotDirectory(m.file(destPath))
		}
	}
	if !info.IsDir {
		data, err := m.ReadAll(ctx, filePath)
		if err != nil {
			return err
		}
		if err = m.WriteAll(ctx, destPath, data, info.Permissions); err != nil {
			return err
		}
		return m.Remove(filePath)
	}
	if strings.HasPrefix(destPath, filePath+m.Separator()) {
		return fmt.Errorf("%w: can't move %s into itself", os.ErrInvalid, m.file(filePath))
	}
	if err = m.MakeDir(destPath, info.Permissions); err != nil {
		return err
	}
	err = m.ListDir(ctx, filePath, nil, func(entry *fs.FileInfo) error {
		return m.Move(ctx, m.CleanPath(filePath, entry.Name), m.CleanPath(destPath, entry.Name))
	})
	if err != nil {
		return err
	}
	return m.Remove(filePath)
}

// ListDirRecursive implements fs.ListDirRecursiveFileSystem
func (m *MockFullyFeaturedFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	if m.MockListDirRecursive != nil {
		return m.MockListDirRecursive(ctx, dirPath, patterns, callback)
	}
	return m.ListDir(ctx, dirPath, nil, func(info *fs.FileInfo) error {
		if info.IsDir && !info.IsSymlink {
			return m.ListDirRecursive(ctx, m.CleanPath(dirPath, info.Name), patterns, callback)
		}
		match, err := fsimpl.MatchAnyPattern(info.Name, patterns)
		if !match || err != nil {
			return err
		}
		// A symbolic link to a directory is listed, not followed
		info.IsDir = false
		return callback(info)
	})
}

// Watch implements fs.WatchFileSystem
func (m *MockFullyFeaturedFileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	if m.MockWatch == nil {
		return nil, fs.NewErrUnsupported(m, "Watch")
	}
	return m.MockWatch(filePath, onEvent)
}

// IsHidden implements fs.HiddenFileSystem
func (m *MockFullyFeaturedFileSystem) IsHidden(filePath string) bool {
	if m.MockIsHidden == nil {
		_, name := m.pathHelper().SplitDirAndName(filePath)
		return strings.HasPrefix(name, ".")
	}
	return m.MockIsHidden(filePath)
}

// ValidName implements fs.ValidNameFileSystem
func (m *MockFullyFeaturedFileSystem) ValidName(name string) error {
	if m.MockValidName == nil {
		return nil
	}
	return m.MockValidName(name)
}

// SetPermissions implements fs.PermissionsFileSystem
func (m *MockFullyFeaturedFileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	if m.MockSetPermissions == nil {
		return fs.NewErrUnsupported(m, "SetPermissions")
	}
	return m.MockSetPermissions(filePath, perm)
}

// User implements fs.UserFileSystem
func (m *MockFullyFeaturedFileSystem) User(filePath string) (string, error) {
	if m.MockUser == nil {
		return "", fs.NewErrUnsupported(m, "User")
	}
	return m.MockUser(filePath)
}

// SetUser implements fs.UserFileSystem
func (m *MockFullyFeaturedFileSystem) SetUser(filePath string, user string) error {
	if m.MockSetUser == nil {
		return fs.NewErrUnsupported(m, "SetUser")
	}
	return m.MockSetUser(filePath, user)
}

// Group implements fs.GroupFileSystem
func (m *MockFullyFeaturedFileSystem) Group(filePath string) (string, error) {
	if m.MockGroup == nil {
		return "", fs.NewErrUnsupported(m, "Group")
	}
	return m.MockGroup(filePath)
}

// SetGroup implements fs.GroupFileSystem
func (m *MockFullyFeaturedFileSystem) SetGroup(filePath string, group string) error {
	if m.MockSetGroup == nil {
		return fs.NewErrUnsupported(m, "SetGroup")
	}
	return m.MockSetGroup(filePath, group)
}

// Lstat implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) Lstat(filePath string) (*fs.FileInfo, error) {
	if m.MockLstat == nil {
		return m.Stat(filePath)
	}
	return m.MockLstat(filePath)
}

// CreateSymbolicLink implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if m.MockCreateSymbolicLink == nil {
		return fs.NewErrUnsupported(m, "CreateSymbolicLink")
	}
	return m.MockCreateSymbolicLink(targetPath, linkPath)
}

// ReadSymbolicLink implements fs.SymbolicLinkFileSystem
func (m *MockFullyFeaturedFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if m.MockReadSymbolicLink == nil {
		return "", fs.NewErrUnsupported(m, "ReadSymbolicLink")
	}
	return m.MockReadSymbolicLink(linkPath)
}

// ListXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) ListXAttr(filePath string, followSymlinks bool) ([]string, error) {
	if m.MockListXAttr == nil {
		return nil, fs.NewErrUnsupported(m, "ListXAttr")
	}
	return m.MockListXAttr(filePath, followSymlinks)
}

// GetXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) GetXAttr(filePath string, name string, followSymlinks bool) ([]byte, error) {
	if m.MockGetXAttr == nil {
		return nil, fs.NewErrUnsupported(m, "GetXAttr")
	}
	return m.MockGetXAttr(filePath, name, followSymlinks)
}

// SetXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) SetXAttr(filePath string, name string, data []byte, flags fs.XAttrFlags, followSymlinks bool) error {
	if m.MockSetXAttr == nil {
		return fs.NewErrUnsupported(m, "SetXAttr")
	}
	return m.MockSetXAttr(filePath, name, data, flags, followSymlinks)
}

// RemoveXAttr implements fs.XAttrFileSystem
func (m *MockFullyFeaturedFileSystem) RemoveXAttr(filePath string, name string, followSymlinks bool) error {
	if m.MockRemoveXAttr == nil {
		return fs.NewErrUnsupported(m, "RemoveXAttr")
	}
	return m.MockRemoveXAttr(filePath, name, followSymlinks)
}

// VolumeName implements fs.VolumeNameFileSystem
func (m *MockFullyFeaturedFileSystem) VolumeName(filePath string) string {
	if m.MockVolumeName == nil {
		return ""
	}
	return m.MockVolumeName(filePath)
}

// IsAbsPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) IsAbsPath(filePath string) bool {
	if m.MockIsAbsPath == nil {
		return true
	}
	return m.MockIsAbsPath(filePath)
}

// AbsPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) AbsPath(filePath string) string {
	if m.MockAbsPath == nil {
		return filePath
	}
	return m.MockAbsPath(filePath)
}

// RelPath implements fs.AbsPathFileSystem
func (m *MockFullyFeaturedFileSystem) RelPath(basePath, targPath string) (string, error) {
	if m.MockRelPath != nil {
		return m.MockRelPath(basePath, targPath)
	}
	base := m.pathHelper().SplitPath(basePath)
	targ := m.pathHelper().SplitPath(targPath)
	common := 0
	for common < len(base) && common < len(targ) && base[common] == targ[common] {
		common++
	}
	parts := append(slices.Repeat([]string{".."}, len(base)-common), targ[common:]...)
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, m.Separator()), nil
}
