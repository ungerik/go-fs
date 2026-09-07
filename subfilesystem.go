package fs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/ungerik/go-fs/fsimpl"
)

// SubFileSystemPrefix is the URI prefix of SubFileSystem, followed by the id.
const SubFileSystemPrefix = "sub://"

var (
	_ FileSystem                 = new(SubFileSystem)
	_ WriteFileSystem            = new(SubFileSystem)
	_ ExistsFileSystem           = new(SubFileSystem)
	_ ReadAllFileSystem          = new(SubFileSystem)
	_ WriteAllFileSystem         = new(SubFileSystem)
	_ AppendFileSystem           = new(SubFileSystem)
	_ AppendWriterFileSystem     = new(SubFileSystem)
	_ ReadWriterFileSystem       = new(SubFileSystem)
	_ TruncateFileSystem         = new(SubFileSystem)
	_ TouchFileSystem            = new(SubFileSystem)
	_ MakeAllDirsFileSystem      = new(SubFileSystem)
	_ RemoveAllFileSystem        = new(SubFileSystem)
	_ CopyFileSystem             = new(SubFileSystem)
	_ MoveFileSystem             = new(SubFileSystem)
	_ RenameFileSystem           = new(SubFileSystem)
	_ ListDirMaxFileSystem       = new(SubFileSystem)
	_ ListDirRecursiveFileSystem = new(SubFileSystem)
	_ PermissionsFileSystem      = new(SubFileSystem)
	_ SymbolicLinkFileSystem     = new(SubFileSystem)
	_ WatchFileSystem            = new(SubFileSystem)
)

// SubFileSystem is a view of a directory of another file system
// as a file system of its own with the root at that directory.
// It forwards every operation to the parent file system with
// translated paths, so the parent's native implementations of the
// optional interfaces are used where they exist and the generic
// emulations otherwise. Paths can't escape the directory.
//
// The parent must stay registered while the view is used,
// because emulated operations go through the File API.
type SubFileSystem struct {
	fsimpl.PathHelper

	parent FileSystem
	dir    string // clean parent path of the root directory
	id     string
	closed atomic.Bool
}

// NewSubFileSystem returns a file system with the URI prefix "sub://" + id
// whose root is the directory dirPath of the parent file system.
// A random id is used if id is empty. dirPath is a parent path or URI
// and must be an existing directory. The file system is not registered.
func NewSubFileSystem(parent FileSystem, dirPath string, id string) (*SubFileSystem, error) {
	if parent == nil {
		return nil, fmt.Errorf("nil parent file system")
	}
	dir := parent.CleanPath(dirPath)
	info, err := fsStat(parent, dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir {
		return nil, NewErrIsNotDirectory(info.File)
	}
	if id == "" {
		id = fsimpl.RandomString()
	}
	return &SubFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: SubFileSystemPrefix + id, Rooted: true},
		parent:     parent,
		dir:        dir,
		id:         id,
	}, nil
}

// NewSubFileSystemAndRegister returns a registered file system
// rooted at dirPath of the parent, see NewSubFileSystem.
func NewSubFileSystemAndRegister(parent FileSystem, dirPath string, id string) (*SubFileSystem, error) {
	subFS, err := NewSubFileSystem(parent, dirPath, id)
	if err != nil {
		return nil, err
	}
	Register(subFS)
	return subFS, nil
}

// Parent returns the parent file system.
func (s *SubFileSystem) Parent() FileSystem {
	return s.parent
}

// Dir returns the directory of the parent file system
// that is the root of this file system.
func (s *SubFileSystem) Dir() File {
	return fsFile(s.parent, s.dir)
}

// parentPath translates a path of this file system to the parent.
func (s *SubFileSystem) parentPath(filePath string) string {
	return s.parent.CleanPath(s.dir, filePath)
}

// belowDir reports whether the parent path is the root directory
// or below it. A raw prefix check is not enough: for the root
// directory "/root" the sibling "/root-other" also has the prefix
// but is outside, translating it would yield a misleading path.
func (s *SubFileSystem) belowDir(parentPath string) bool {
	if parentPath == s.dir {
		return true
	}
	sep := s.parent.Separator()
	return strings.HasPrefix(parentPath, strings.TrimSuffix(s.dir, sep)+sep)
}

// subPath translates a parent path below the root directory
// to a path of this file system.
func (s *SubFileSystem) subPath(parentPath string) string {
	rel := strings.TrimPrefix(parentPath, s.dir)
	sep := s.parent.Separator()
	if sep != "/" {
		rel = strings.ReplaceAll(rel, sep, "/")
	}
	return s.CleanPath(rel)
}

// subInfo translates a FileInfo of the parent to this file system.
func (s *SubFileSystem) subInfo(info *FileInfo) *FileInfo {
	subInfo := *info
	subInfo.File = s.subFile(info.File)
	return &subInfo
}

func (s *SubFileSystem) checkClosed() error {
	if s.closed.Load() {
		return ErrFileSystemClosed
	}
	return nil
}

// ReadableWritable returns the readable and writable flags
// of the parent file system.
func (s *SubFileSystem) ReadableWritable() (readable, writable bool) {
	return s.parent.ReadableWritable()
}

// RootDir returns the root directory of the view,
// which is the directory of the parent it was created for.
func (s *SubFileSystem) RootDir() File {
	return File(s.URIPrefix + "/")
}

// ID returns the id of the file system, which is part of its URI prefix.
func (s *SubFileSystem) ID() string {
	return s.id
}

// Name returns a descriptive name of the view including the parent's name.
func (s *SubFileSystem) Name() string {
	return "sub file system of " + s.parent.Name()
}

// String returns a descriptive string of the view
// including its prefix and the parent directory.
func (s *SubFileSystem) String() string {
	return s.Name() + " rooted at " + string(s.Dir())
}

///////////////////////////////////////////////////////////////////////////////
// FileSystem

// Stat returns the FileInfo of the file with the path translated
// to the parent file system.
func (s *SubFileSystem) Stat(filePath string) (*FileInfo, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	info, err := fsStat(s.parent, s.parentPath(filePath))
	if err != nil {
		return nil, s.subErr(err)
	}
	return s.subInfo(info), nil
}

// subFile translates a File of the parent below the root directory.
func (s *SubFileSystem) subFile(parentFile File) File {
	return File(s.JoinCleanURI(s.subPath(s.parent.CleanPath(string(parentFile)))))
}

// subErr translates the file of typed parent errors to this file system.
func (s *SubFileSystem) subErr(err error) error {
	return translateErrFile(err, s.subFile)
}

// ListDir calls the callback for every file in the directory that
// matches any of the patterns, or for all files if no patterns are passed.
// The FileInfo passed to the callback has a File of this file system.
func (s *SubFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	err := fsListDir(ctx, s.parent, s.parentPath(dirPath), patterns, func(info *FileInfo) error {
		return callback(s.subInfo(info))
	})
	return s.subErr(err)
}

// OpenReader opens the file of the parent file system for reading.
func (s *SubFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	r, err := fsOpenReader(s.parent, s.parentPath(filePath))
	return r, s.subErr(err)
}

// Close unregisters the file system if it was registered
// without closing the parent file system.
func (s *SubFileSystem) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	Unregister(s)
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// WriteFileSystem

// OpenWriter opens the file of the parent file system for writing,
// creating it if it does not exist and truncating it if it does.
func (s *SubFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	w, err := fsOpenWriter(s.parent, s.parentPath(filePath), perm)
	return w, s.subErr(err)
}

// MakeDir creates a directory in the parent file system.
func (s *SubFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMakeDir(s.parent, s.parentPath(dirPath), perm))
}

// Remove removes a file or empty directory from the parent file system.
// The root directory of the view can't be removed.
func (s *SubFileSystem) Remove(filePath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	parentPath := s.parentPath(filePath)
	if parentPath == s.dir {
		return fmt.Errorf("can't remove root directory of %s", s)
	}
	return s.subErr(fsRemove(s.parent, parentPath))
}

///////////////////////////////////////////////////////////////////////////////
// Optional interfaces, forwarded via the fs package dispatch
// so the parent's native implementation or the emulation is used

// Exists reports if the file exists in the parent file system.
func (s *SubFileSystem) Exists(filePath string) (bool, error) {
	if err := s.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	return fsExists(s.parent, s.parentPath(filePath))
}

// ReadAll reads the complete file from the parent file system.
func (s *SubFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	data, err := fsReadAll(ctx, s.parent, s.parentPath(filePath))
	return data, s.subErr(err)
}

// WriteAll writes data to the file of the parent file system,
// creating it if it does not exist and replacing its content if it does.
func (s *SubFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsWriteAll(ctx, s.parent, s.parentPath(filePath), data, perm))
}

// Append appends data to the file of the parent file system,
// creating it if it does not exist.
func (s *SubFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsAppend(ctx, s.parent, s.parentPath(filePath), data, perm))
}

// OpenAppendWriter opens the file of the parent file system for appending,
// creating it if it does not exist.
func (s *SubFileSystem) OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	w, err := fsOpenAppendWriter(s.parent, s.parentPath(filePath), perm)
	return w, s.subErr(err)
}

// OpenReadWriter opens the file of the parent file system
// for reading and writing at any offset.
func (s *SubFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	rw, err := fsOpenReadWriter(s.parent, s.parentPath(filePath), perm)
	return rw, s.subErr(err)
}

// Truncate changes the size of the file in the parent file system.
func (s *SubFileSystem) Truncate(filePath string, size int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsTruncate(context.Background(), s.parent, s.parentPath(filePath), size))
}

// Touch creates the file in the parent file system if it does not exist,
// else it updates its modification time.
func (s *SubFileSystem) Touch(filePath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsTouch(s.parent, s.parentPath(filePath), perm))
}

// MakeAllDirs creates a directory and all missing parent directories
// within the view, never above its root.
func (s *SubFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMakeAllDirs(s.parent, s.parentPath(dirPath), perm))
}

// RemoveAll removes the file or directory with all its content
// from the parent file system. The root directory of the view
// can't be removed.
func (s *SubFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	parentPath := s.parentPath(filePath)
	if parentPath == s.dir {
		return fmt.Errorf("can't remove root directory of %s", s)
	}
	return s.subErr(fsRemoveAll(ctx, s.parent, parentPath))
}

// CopyFile copies a file within the view using the parent file system.
func (s *SubFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if srcFile == "" || destFile == "" {
		return ErrEmptyPath
	}
	src, dest := fsFile(s.parent, s.parentPath(srcFile)), fsFile(s.parent, s.parentPath(destFile))
	if src == dest {
		return nil
	}
	return s.subErr(CopyFile(ctx, src, dest))
}

// Move moves and/or renames a file within the view
// using the parent file system.
func (s *SubFileSystem) Move(filePath string, destPath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMove(context.Background(), s.parent, s.parentPath(filePath), s.parentPath(destPath)))
}

// Rename renames a file within its directory in the parent file system
// and returns the new path within the view.
func (s *SubFileSystem) Rename(filePath string, newName string) (newPath string, err error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	if filePath == "" {
		return "", ErrEmptyPath
	}
	newParentPath, err := fsRename(s.parent, s.parentPath(filePath), newName)
	if err != nil {
		return "", s.subErr(err)
	}
	return s.subPath(newParentPath), nil
}

// ListDirMax returns at most max files of the directory that match any
// of the patterns, or all of them if max is negative.
func (s *SubFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if dirPath == "" {
		return nil, ErrEmptyPath
	}
	parentFiles, err := fsListDirMax(ctx, s.parent, s.parentPath(dirPath), max, patterns)
	if err != nil {
		return nil, s.subErr(err)
	}
	files := make([]File, len(parentFiles))
	for i, f := range parentFiles {
		files[i] = s.subFile(f)
	}
	return files, nil
}

// ListDirRecursive calls the callback for every file below the directory
// that matches any of the patterns.
func (s *SubFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	err := fsListDirRecursive(ctx, s.parent, s.parentPath(dirPath), patterns, func(info *FileInfo) error {
		return callback(s.subInfo(info))
	})
	return s.subErr(err)
}

// SetPermissions sets the permissions of the file in the parent file system.
func (s *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if p, ok := s.parent.(PermissionsFileSystem); ok {
		return s.subErr(p.SetPermissions(s.parentPath(filePath), perm))
	}
	return NewErrUnsupported(s, "SetPermissions")
}

// IsSymbolicLink reports if the file is a symbolic link
// in the parent file system.
func (s *SubFileSystem) IsSymbolicLink(filePath string) bool {
	if s.closed.Load() {
		return false
	}
	if p, ok := s.parent.(SymbolicLinkFileSystem); ok {
		return p.IsSymbolicLink(s.parentPath(filePath))
	}
	return false
}

// CreateSymbolicLink creates a link at linkPath to targetPath.
// The target is stored as a path of the parent file system.
func (s *SubFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if p, ok := s.parent.(SymbolicLinkFileSystem); ok {
		return s.subErr(p.CreateSymbolicLink(s.parentPath(targetPath), s.parentPath(linkPath)))
	}
	return NewErrUnsupported(s, "CreateSymbolicLink")
}

// ReadSymbolicLink returns the target of the link translated
// to this file system if it points below the root directory.
func (s *SubFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	p, ok := s.parent.(SymbolicLinkFileSystem)
	if !ok {
		return "", NewErrUnsupported(s, "ReadSymbolicLink")
	}
	target, err := p.ReadSymbolicLink(s.parentPath(linkPath))
	if err != nil {
		return "", s.subErr(err)
	}
	if s.belowDir(target) {
		return s.subPath(target), nil
	}
	return target, nil
}

// Watch registers the callback for file system events of the file
// at the parent file system and returns a cancel function.
func (s *SubFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	p, ok := s.parent.(WatchFileSystem)
	if !ok {
		return nil, NewErrUnsupported(s, "Watch")
	}
	cancel, err = p.Watch(s.parentPath(filePath), func(file File, event Event) {
		onEvent(s.subFile(file), event)
	})
	return cancel, s.subErr(err)
}
