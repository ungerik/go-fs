package fs

import (
	"context"
	"errors"
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

func (s *SubFileSystem) ReadableWritable() (readable, writable bool) {
	return s.parent.ReadableWritable()
}

func (s *SubFileSystem) RootDir() File {
	return File(s.URIPrefix + "/")
}

func (s *SubFileSystem) ID() string {
	return s.id
}

func (s *SubFileSystem) Name() string {
	return "sub file system of " + s.parent.Name()
}

func (s *SubFileSystem) String() string {
	return s.Name() + " rooted at " + string(s.Dir())
}

///////////////////////////////////////////////////////////////////////////////
// FileSystem

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
	return File(s.JoinCleanURI(s.subPath(parentFile.Path())))
}

// subErr translates the file of typed parent errors to this file system.
func (s *SubFileSystem) subErr(err error) error {
	var (
		notExist ErrDoesNotExist
		exists   ErrAlreadyExists
		isDir    ErrIsDirectory
		isNotDir ErrIsNotDirectory
	)
	switch {
	case errors.As(err, &notExist):
		if f, ok := notExist.file.(File); ok {
			return NewErrDoesNotExist(s.subFile(f))
		}
	case errors.As(err, &exists):
		return NewErrAlreadyExists(s.subFile(exists.file))
	case errors.As(err, &isDir):
		if f, ok := isDir.file.(File); ok {
			return NewErrIsDirectory(s.subFile(f))
		}
	case errors.As(err, &isNotDir):
		if f, ok := isNotDir.file.(File); ok {
			return NewErrIsNotDirectory(s.subFile(f))
		}
	}
	return err
}

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

func (s *SubFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMakeDir(s.parent, s.parentPath(dirPath), perm))
}

func (s *SubFileSystem) Remove(filePath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if s.parentPath(filePath) == s.dir {
		return fmt.Errorf("can't remove root directory of %s", s)
	}
	return s.subErr(fsRemove(s.parent, s.parentPath(filePath)))
}

///////////////////////////////////////////////////////////////////////////////
// Optional interfaces, forwarded via the fs package dispatch
// so the parent's native implementation or the emulation is used

func (s *SubFileSystem) Exists(filePath string) (bool, error) {
	if err := s.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	return fsExists(s.parent, s.parentPath(filePath))
}

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

func (s *SubFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsWriteAll(ctx, s.parent, s.parentPath(filePath), data, perm))
}

func (s *SubFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsAppend(ctx, s.parent, s.parentPath(filePath), data, perm))
}

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

func (s *SubFileSystem) Truncate(filePath string, size int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsTruncate(context.Background(), s.parent, s.parentPath(filePath), size))
}

func (s *SubFileSystem) Touch(filePath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsTouch(s.parent, s.parentPath(filePath), perm))
}

func (s *SubFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMakeAllDirs(s.parent, s.parentPath(dirPath), perm))
}

func (s *SubFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if s.parentPath(filePath) == s.dir {
		return fmt.Errorf("can't remove root directory of %s", s)
	}
	return s.subErr(fsRemoveAll(ctx, s.parent, s.parentPath(filePath)))
}

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

func (s *SubFileSystem) Move(filePath string, destPath string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	return s.subErr(fsMove(context.Background(), s.parent, s.parentPath(filePath), s.parentPath(destPath)))
}

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

func (s *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if p, ok := s.parent.(PermissionsFileSystem); ok {
		return s.subErr(p.SetPermissions(s.parentPath(filePath), perm))
	}
	return NewErrUnsupported(s, "SetPermissions")
}

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
	if strings.HasPrefix(target, s.dir) {
		return s.subPath(target), nil
	}
	return target, nil
}

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
