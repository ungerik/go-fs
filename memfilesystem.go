package fs

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ FileSystem = new(MemFileSystem)

	// memFileNode implements io/fs.FileInfo
	_ iofs.FileInfo = new(memFileInfo)
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

func (n *memFileNode) Mode() iofs.FileMode {
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
	volume   string
	prefix   string
	readOnly bool
	root     memFileNode
	mtx      sync.RWMutex
}

func NewMemFileSystem(separator string, initialFiles ...MemFile) (*MemFileSystem, error) {
	// Validate arguments
	if separator != `/` && separator != `\` {
		return nil, fmt.Errorf("invalid separator %q", separator)
	}
	for i, f := range initialFiles {
		if f.FileName == "" {
			return nil, fmt.Errorf("empty initialFiles[%d].FileName", i)
		}
	}

	// Create MemFileSystem
	now := time.Now()
	memFS := &MemFileSystem{
		sep: separator,
		root: memFileNode{
			MemFile:  MemFile{FileName: separator},
			Modified: now,
			Dir:      make(map[string]*memFileNode, len(initialFiles)),
		},
	}
	memFS.id = fmt.Sprintf("%x", unsafe.Pointer(memFS))
	memFS.updatePrefix()

	// Add initial files
	for _, f := range initialFiles {
		_, err := memFS.AddMemFile(f, now)
		if err != nil {
			return nil, err
		}
	}

	// Register with global file system registry
	Register(memFS)
	return memFS, nil
}

// NewSingleMemFileSystem creates and registers a new MemFileSystem
// containing a single MemFile that is returned as a File
// that can be used to access the file without knowing the file system.
// Closing the file system will make the File invalid.
func NewSingleMemFileSystem(file MemFile) (*MemFileSystem, File, error) {
	fs, err := NewMemFileSystem("/", file)
	if err != nil {
		return nil, "", err
	}
	return fs, fs.JoinCleanFile("/", file.FileName), nil
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

func (fs *MemFileSystem) SetReadOnly(readOnly bool) {
	fs.mtx.Lock()
	fs.readOnly = readOnly
	fs.mtx.Unlock()
}

func (fs *MemFileSystem) WithID(id string) *MemFileSystem {
	if id == "" {
		panic("empty id")
	}
	if id == fs.id {
		return fs
	}
	Unregister(fs)
	fs.id = id
	fs.updatePrefix()
	Register(fs)
	return fs
}

func (fs *MemFileSystem) WithVolume(volume string) *MemFileSystem {
	if volume == fs.volume {
		return fs
	}
	Unregister(fs)
	fs.volume = volume
	fs.updatePrefix()
	Register(fs)
	return fs
}

// AddMemFile adds a MemFile to the file system with the given modified time.
// The MemFile.FileName can be a path with the path separator of the MemFileSystem,
// in which case all directories of the path are created.
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
		// TODO makeAllDirs
		err := fs.makeDir(fs.JoinCleanPath(pathParts[0:i+1]...), nil)
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
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	return fs.makeDir(dirPath, perm)
}

func (fs *MemFileSystem) makeDir(dirPath string, _ []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
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
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	return fs.makeAllDirs(dirPath, perm)
}

func (fs *MemFileSystem) makeAllDirs(dirPath string, _ []Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
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

	panic("TODO")
}

func (fs *MemFileSystem) ReadableWritable() (readable, writable bool) {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	return true, !fs.readOnly
}

func (fs *MemFileSystem) RootDir() File {
	return File(fs.prefix + fs.sep)
}

func (fs *MemFileSystem) ID() (string, error) {
	return fs.id, nil
}

func (fs *MemFileSystem) updatePrefix() {
	if fs.volume != "" {
		fs.prefix = "mem://" + fs.id + "/" + fs.volume
	} else {
		fs.prefix = "mem://" + fs.id
	}
}

func (fs *MemFileSystem) Prefix() string {
	return fs.prefix
}

func (*MemFileSystem) Name() string {
	return "memory file system"
}

func (fs *MemFileSystem) String() string {
	return fmt.Sprintf("MemFileSystem(%s)", fs.prefix)
}

func (fs *MemFileSystem) JoinCleanFile(uri ...string) File {
	return File(fs.prefix + fs.JoinCleanPath(uri...))
}

func (fs *MemFileSystem) IsAbsPath(filePath string) bool {
	if strings.HasPrefix(filePath, fs.sep) {
		return true
	}
	if fs.volume != "" && strings.HasPrefix(filePath, fs.volume) {
		return true
	}
	return false
}

func (fs *MemFileSystem) AbsPath(filePath string) string {
	return fs.JoinCleanPath(filePath)
}

func (fs *MemFileSystem) URL(cleanPath string) string {
	return fs.prefix + cleanPath
}

func (fs *MemFileSystem) CleanPathFromURI(uri string) string {
	return strings.TrimPrefix(uri, fs.prefix)
}

func (fs *MemFileSystem) JoinCleanPath(uriParts ...string) string {
	if len(uriParts) > 0 {
		uriParts[0] = strings.TrimPrefix(uriParts[0], fs.prefix)
	}
	cleanPath := strings.Join(uriParts, fs.sep)
	unescPath, err := url.PathUnescape(cleanPath)
	if err == nil {
		cleanPath = unescPath
	}
	cleanPath = path.Clean(cleanPath) // TODO use sep
	return cleanPath
}

func (fs *MemFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, fs.prefix, fs.sep)
}

func (fs *MemFileSystem) Separator() string {
	return fs.sep
}

func (*MemFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return false, nil
}

func (fs *MemFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, "/")
}

func (fs *MemFileSystem) VolumeName(filePath string) string {
	if len(filePath) < len(fs.volume) {
		return ""
	}
	return filePath[:len(fs.volume)]
}

func (fs *MemFileSystem) Volume() string {
	return fs.volume
}

func (fs *MemFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
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

func (*MemFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
	panic("TODO")
}

// func (*MemFileSystem) SetPermissions(filePath string, perm Permissions) error {
// 	return nil
// }

// func (*MemFileSystem) User(filePath string) (string, error) {
// 	return "", nil
// }

// func (*MemFileSystem) SetUser(filePath string, user string) error {
// 	return nil
// }

// func (*MemFileSystem) Group(filePath string) (string, error) {
// 	return "", nil
// }

// func (*MemFileSystem) SetGroup(filePath string, group string) error {
// 	return nil
// }

func (fs *MemFileSystem) Touch(filePath string, perm []Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(filePath)
	if node != nil {
		node.Modified = time.Now()
		return nil
	}

	parentDir, name := fs.SplitDirAndName(filePath)
	if parent == nil {
		return NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name},
		time.Now(),
		JoinPermissions(perm, memFileSystemDefaultPermissions),
	)
	return nil
}

func (fs *MemFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	return node.FileData, nil
}

func (fs *MemFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(filePath)
	if node != nil {
		node.FileData = data
		node.Modified = time.Now()
		return nil
	}

	parentDir, name := fs.SplitDirAndName(filePath)
	if parent == nil {
		return NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name, FileData: data},
		time.Now(),
		JoinPermissions(perm, memFileSystemDefaultPermissions),
	)
	return nil
}

func (fs *MemFileSystem) Append(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(filePath)
	if node != nil {
		node.FileData = append(node.FileData, data...)
		node.Modified = time.Now()
		return nil
	}

	parentDir, name := fs.SplitDirAndName(filePath)
	if parent == nil {
		return NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name, FileData: data},
		time.Now(),
		JoinPermissions(perm, memFileSystemDefaultPermissions),
	)
	return nil
}

func (fs *MemFileSystem) OpenReader(filePath string) (iofs.File, error) {
	return nil, nil
}

func (fs *MemFileSystem) OpenWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	return nil, nil
}

func (fs *MemFileSystem) OpenAppendWriter(filePath string, perm []Permissions) (WriteCloser, error) {
	return nil, nil
}

func (fs *MemFileSystem) OpenReadWriter(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
	return nil, nil
}

func (fs *MemFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	return nil, nil
}

func (fs *MemFileSystem) Truncate(filePath string, newSize int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	currentSize := int64(len(node.FileData))
	if currentSize == newSize {
		return nil
	}
	if currentSize > newSize {
		node.FileData = node.FileData[:newSize]
	} else {
		node.FileData = append(node.FileData, make([]byte, newSize-currentSize)...)
	}
	node.Modified = time.Now()
	return nil
}

func (fs *MemFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	return nil
}

func (fs *MemFileSystem) Rename(filePath string, newName string) (string, error) {
	return "", nil
}

func (fs *MemFileSystem) Move(filePath string, destPath string) error {
	return nil
}

func (fs *MemFileSystem) Remove(filePath string) error {
	return nil
}

func (fs *MemFileSystem) Close() error {
	fs.mtx.Lock()
	if fs.root.Dir == nil {
		fs.mtx.Unlock()
		return nil // already closed
	}
	fs.root.Dir = nil
	fs.mtx.Unlock() // Unlock before Unregister to avoid deadlock
	Unregister(fs)
	return nil
}

func (fs *MemFileSystem) Clear() {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	clear(fs.root.Dir)
	fs.root.Modified = time.Now()
}
