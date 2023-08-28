package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ fullyFeaturedFileSystem = new(MemFileSystem)

	// memFileNode implements io/fs.FileInfo
	_ fs.FileInfo = new(memFileInfo)
)

var memFileSystemDefaultPermissions = UserAndGroupReadWrite

// memFileNode implements io/fs.FileInfo
type memFileNode struct {
	MemFile
	Modified    time.Time
	Permissions Permissions
	Dir         map[string]*memFileNode
}

func (n *memFileNode) IsDir() bool {
	return n != nil && n.Dir != nil
}

func (n *memFileNode) Mode() fs.FileMode {
	return n.Permissions.FileMode(n.Dir != nil)
}

func (n *memFileNode) ModTime() time.Time {
	return n.Modified
}

func (n *memFileNode) Sys() any { return nil }

// MemFileSystem is a fully featured thread-safe
// file system living in random access memory.
//
// Usefull as mock file system for tests
// or caching of slow file systems.
type MemFileSystem struct {
	id       string
	sep      string
	readOnly bool
	volume   string
	root     memFileNode
	mtx      sync.RWMutex
}

func NewMemFileSystem(separator string, rootFiles ...MemFile) (*MemFileSystem, error) {
	if separator != `/` && separator != `\` {
		return nil, fmt.Errorf("invalid separator %#v", separator)
	}
	for i, f := range rootFiles {
		if f.FileName == "" {
			return nil, fmt.Errorf("empty rootFiles[%d].FileName", i)
		}
	}
	now := time.Now()
	fs := &MemFileSystem{
		sep: separator,
		root: memFileNode{
			MemFile:  MemFile{FileName: separator},
			Modified: now,
			Dir:      make(map[string]*memFileNode, len(rootFiles)),
		},
	}
	fs.id = fmt.Sprintf("%x", unsafe.Pointer(fs))
	for _, rootFile := range rootFiles {
		_, err := fs.AddMemFile(rootFile, now)
		if err != nil {
			return nil, err
		}
	}
	Register(fs)
	return fs, nil
}

func (fs *MemFileSystem) Close() error {
	Unregister(fs)
	fs.Clear()
	return nil
}

func (fs *MemFileSystem) Clear() {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	clear(fs.root.Dir)
	fs.root.Modified = time.Now()
}

func newMemDirNode(name string, modified time.Time, perm ...Permissions) *memFileNode {
	if name == "" {
		panic("empty dir name")
	}
	return &memFileNode{
		MemFile:     MemFile{FileName: name},
		Modified:    modified,
		Permissions: JoinPermissions(perm, memFileSystemDefaultPermissions),
		Dir:         make(map[string]*memFileNode),
	}
}

func newMemFileNode(f MemFile, modified time.Time, perm ...Permissions) *memFileNode {
	if f.FileName == "" {
		panic("empty filename")
	}
	return &memFileNode{
		MemFile:     f,
		Modified:    modified,
		Permissions: JoinPermissions(perm, memFileSystemDefaultPermissions),
		Dir:         nil,
	}
}

func (fs *MemFileSystem) AddMemFile(f MemFile, modified time.Time) (File, error) {
	if f.FileName == "" {
		return "", errors.New("empty filename")
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return "", ErrReadOnlyFileSystem
	}

	pathParts := fs.SplitPath(f.FileName)
	parentDir := &fs.root
	// Make all dirs first
	for i := 0; i < len(pathParts)-2; i++ {
		err := fs.MakeDir(fs.JoinCleanPath(pathParts[0:i+1]...), nil)
		if err != nil {
			return "", err
		}
		panic(" todo set parentDir ")
	}
	parentDir.Dir[pathParts[len(pathParts)-1]] = newMemFileNode(f, modified)
	return fs.JoinCleanFile(pathParts...), nil
}

func (fs *MemFileSystem) pathNodeOrNil(filePath string) (node, parent *memFileNode) {
	if filePath == "" {
		return nil, nil
	}
	node = &fs.root
	for _, name := range fs.SplitPath(filePath) {
		subNode, ok := node.Dir[name]
		if !ok {
			return nil, parent
		}
		parent = node
		node = subNode
	}
	return node, parent
}

func (fs *MemFileSystem) MakeDir(dirPath string, perm []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(dirPath)
	if node != nil {
		return NewErrAlreadyExists(fs.RootDir().Join(dirPath))
	}
	parentDir, name := fs.SplitDirAndName(dirPath)
	if parent == nil {
		return NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	if !parent.IsDir() {
		return NewErrIsNotDirectory(fs.RootDir().Join(parentDir))
	}
	parent.Dir[name] = newMemDirNode(name, time.Now())
	return nil
}

func (fs *MemFileSystem) MakeAllDirs(dirPath string, perm []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	// pathParts := fs.SplitPath(f.FileName)
	// parentDir := &fs.root
	// // Make all dirs first
	// for i := 0; i < len(pathParts)-2; i++ {
	// 	err := fs.MakeDir(fs.JoinCleanPath(pathParts[0:i+1]...), nil)
	// 	if err != nil {
	// 		return "", err
	// 	}
	// 	panic(" todo set parentDir ")
	// }
	// parentDir.Dir[pathParts[len(pathParts)-1]] = fs.newMemFileInfo(f, modified)

	panic("todo")

}

func (fs *MemFileSystem) IsReadOnly() bool {
	return fs.readOnly
}

func (fs *MemFileSystem) SetReadOnly(readOnly bool) {
	fs.mtx.Lock()
	fs.readOnly = readOnly
	fs.mtx.Unlock()
}

func (*MemFileSystem) IsWriteOnly() bool {
	return false
}

func (fs *MemFileSystem) RootDir() File {
	return File(fs.Prefix() + fs.sep)
}

func (fs *MemFileSystem) ID() (string, error) {
	return "MemFileSystem_" + fs.id, nil
}

func (fs *MemFileSystem) Prefix() string {
	if fs.volume != "" {
		return "mem://" + fs.id + "/" + fs.volume
	}
	return "mem://" + fs.id
}

func (*MemFileSystem) Name() string {
	return "memory file system"
}

func (fs *MemFileSystem) String() string {
	return "MemFileSystem_" + fs.id
}

func (fs *MemFileSystem) JoinCleanFile(uri ...string) File {
	return File(fs.Prefix() + fs.JoinCleanPath(uri...))
}

func (*MemFileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (fs *MemFileSystem) AbsPath(filePath string) string {
	return fs.JoinCleanPath(filePath)
}

func (fs *MemFileSystem) URL(cleanPath string) string {
	return fs.Prefix() + cleanPath
}

func (fs *MemFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], fs.Prefix())
	}
	cleanPath := path.Join(uriParts...)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath)
	return cleanPath
}

func (fs *MemFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, fs.Prefix(), fs.sep)
}

func (fs *MemFileSystem) Separator() string {
	return fs.sep
}

func (*MemFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return false, nil
}

func (fs *MemFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, fs.sep)
}

func (fs *MemFileSystem) VolumeName(filePath string) string {
	if filePath == "" {
		return ""
	}

	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	return fs.volume
}

func (fs *MemFileSystem) SetVolume(volume string) {
	fs.mtx.Lock()
	fs.volume = volume
	fs.mtx.Unlock()
}

func (fs *MemFileSystem) Stat(filePath string) (fs.FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	return node, nil
}

func (fs *MemFileSystem) Exists(filePath string) bool {
	if filePath == "" {
		return false
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, _ := fs.pathNodeOrNil(filePath)
	return node != nil
}

func (*MemFileSystem) IsHidden(filePath string) bool {
	return false
}

func (*MemFileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (*MemFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return nil
}

func (*MemFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return nil
}

func (*MemFileSystem) ListDirMax(ctx context.Context, dirPath string, n int, patterns []string) (files []File, err error) {
	return nil, nil
}

func (*MemFileSystem) SetPermissions(filePath string, perm Permissions) error {
	return nil
}

func (*MemFileSystem) User(filePath string) string {
	return ""
}

func (*MemFileSystem) SetUser(filePath string, user string) error {
	return nil
}

func (*MemFileSystem) Group(filePath string) string {
	return ""
}

func (*MemFileSystem) SetGroup(filePath string, group string) error {
	return nil
}

func (*MemFileSystem) Touch(filePath string, perm []Permissions) error {
	return nil
}

func (*MemFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	return nil, nil
}

func (*MemFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return nil
}

func (*MemFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	return nil
}

func (*MemFileSystem) OpenReader(filePath string) (fs.File, error) {
	return nil, nil
}

func (*MemFileSystem) OpenWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, nil
}

func (*MemFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (io.WriteCloser, error) {
	return nil, nil
}

func (*MemFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	return nil, nil
}

func (*MemFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	return nil, nil
}

func (*MemFileSystem) Truncate(filePath string, size int64) error {
	return nil
}

func (*MemFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return nil
}

func (*MemFileSystem) Rename(filePath string, newName string) (string, error) {
	return "", nil
}

func (*MemFileSystem) Move(filePath string, destPath string) error {
	return nil
}

func (*MemFileSystem) Remove(filePath string) error {
	return nil
}
