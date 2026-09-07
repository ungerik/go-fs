package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/ungerik/go-fs/fsimpl"
)

var (
	_ FileSystem                 = new(MemFileSystem)
	_ WriteFileSystem            = new(MemFileSystem)
	_ ExistsFileSystem           = new(MemFileSystem)
	_ TouchFileSystem            = new(MemFileSystem)
	_ MakeAllDirsFileSystem      = new(MemFileSystem)
	_ RemoveAllFileSystem        = new(MemFileSystem)
	_ ReadAllFileSystem          = new(MemFileSystem)
	_ WriteAllFileSystem         = new(MemFileSystem)
	_ AppendFileSystem           = new(MemFileSystem)
	_ AppendWriterFileSystem     = new(MemFileSystem)
	_ ReadWriterFileSystem       = new(MemFileSystem)
	_ TruncateFileSystem         = new(MemFileSystem)
	_ CopyFileSystem             = new(MemFileSystem)
	_ MoveFileSystem             = new(MemFileSystem)
	_ RenameFileSystem           = new(MemFileSystem)
	_ VolumeNameFileSystem       = new(MemFileSystem)
	_ WatchFileSystem            = new(MemFileSystem)
	_ PermissionsFileSystem      = new(MemFileSystem)
	_ UserFileSystem             = new(MemFileSystem)
	_ GroupFileSystem            = new(MemFileSystem)
	_ ListDirMaxFileSystem       = new(MemFileSystem)
	_ ListDirRecursiveFileSystem = new(MemFileSystem)
	_ XAttrFileSystem            = new(MemFileSystem)
	_ SymbolicLinkFileSystem     = new(MemFileSystem)

	// memFileNode implements io/fs.FileInfo
	_ iofs.FileInfo = new(memFileNode)
)

var memFileSystemDefaultPermissions = UserAndGroupReadWrite

// memFileNode implements io/fs.FileInfo.
//
// A node is one of three kinds:
//   - regular file: Dir == nil && SymlinkTarget == ""
//   - directory:    Dir != nil
//   - symbolic link: SymlinkTarget != "" (Dir is always nil, FileData unused)
type memFileNode struct {
	MemFile
	Modified      time.Time
	Permissions   Permissions
	User          string
	Group         string
	XAttrs        map[string][]byte
	SymlinkTarget string
	Dir           map[string]*memFileNode
}

func (n *memFileNode) IsDir() bool {
	return n != nil && n.Dir != nil
}

func (n *memFileNode) IsSymlink() bool {
	return n != nil && n.SymlinkTarget != ""
}

func (n *memFileNode) Mode() iofs.FileMode {
	m := n.Permissions.FileMode(n.Dir != nil)
	if n.IsSymlink() {
		m |= iofs.ModeSymlink
	}
	return m
}

func (n *memFileNode) ModTime() time.Time {
	return n.Modified
}

func (n *memFileNode) Sys() any { return nil }

// MemFileSystem is a fully featured thread-safe file system implementation
// that stores all files and directories in random access memory.
//
// # Inner Workings
//
// The file system is organized as a tree structure where each node (memFileNode)
// represents either a file or directory. Directory nodes contain a map of child
// nodes indexed by name. Files store their content as byte slices ([]byte) in
// the MemFile.FileData field.
//
// All operations are protected by a read-write mutex (sync.RWMutex) to ensure
// thread-safety. Read operations (Stat, OpenReader, ListDirInfo) acquire read
// locks, while write operations (OpenWriter, MakeDir, Remove) acquire write locks.
//
// File I/O is implemented using custom reader/writer types that operate directly
// on the in-memory byte slices:
//   - OpenReader returns a fsimpl.ReadonlyFileBuffer wrapping the file's data
//   - OpenWriter returns a memFileWriter that appends or overwrites data
//   - OpenReadWriter returns a memFileReadWriter with full seek support
//
// The file system supports configurable path separators ("/" or "\") and optional
// volume names (e.g., "C:") for Windows-style paths. Each instance is registered
// globally with a unique ID (memory address by default) and can be accessed via
// URIs like "mem://<id>/path/to/file".
//
// # Use Cases
//
// Useful as a mock file system for tests or for caching of slow file systems.
// All data is lost when the file system is closed or the process terminates.
type MemFileSystem struct {
	fsimpl.PathHelper // Path methods for the current prefix, separator and volume

	id       string       // Unique identifier for this file system instance
	volume   string       // Optional volume name (e.g., "C:")
	readOnly bool         // If true, write operations return ErrReadOnlyFileSystem
	root     memFileNode  // Root directory node
	mtx      sync.RWMutex // Protects all file system operations

	watchMtx    sync.Mutex                     // Protects the watches map only
	watches     map[string]map[uint64]memWatch // keyed by clean watch path
	lastWatchID uint64
}

// memWatch is a single Watch subscription registered with a MemFileSystem.
type memWatch struct {
	callback func(File, Event)
	isDir    bool
}

// NewMemFileSystem creates and registers a new MemFileSystem
// with the passed separator, which must be "/" or "\\",
// and adds the initialFiles with the current time as modification time.
// The FileName of an initial file can be a path with the separator,
// in which case all directories of the path are created.
// Use MemFileSystem.WithID to give the file system a stable URI prefix,
// and Close to unregister it again.
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
		PathHelper: fsimpl.PathHelper{PathSep: separator},
		root: memFileNode{
			MemFile:  MemFile{FileName: separator},
			Modified: now,
			Dir:      make(map[string]*memFileNode, len(initialFiles)),
		},
	}
	memFS.id = fsimpl.RandomString()
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

func newMemDirNode(name string, modified time.Time, perm Permissions) *memFileNode {
	if name == "" {
		panic("empty dir name")
	}
	return &memFileNode{
		MemFile:     MemFile{FileName: name},
		Modified:    modified,
		Permissions: perm.OrDefault(memFileSystemDefaultPermissions),
		Dir:         make(map[string]*memFileNode),
	}
}

func newMemFileNode(f MemFile, modified time.Time, perm Permissions) *memFileNode {
	if f.FileName == "" {
		panic("empty filename")
	}
	return &memFileNode{
		MemFile:     f,
		Modified:    modified,
		Permissions: perm.OrDefault(memFileSystemDefaultPermissions),
		Dir:         nil,
	}
}

// SetReadOnly makes the file system reject every write operation
// with ErrReadOnlyFileSystem, or allow writes again.
func (fs *MemFileSystem) SetReadOnly(readOnly bool) {
	fs.mtx.Lock()
	fs.readOnly = readOnly
	fs.mtx.Unlock()
}

// WithID re-registers the file system under a new id, which becomes
// part of its URI prefix ("mem://<id>"), and returns it to allow
// chaining after NewMemFileSystem. Panics if id is empty.
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

// WithVolume re-registers the file system with a volume name that is
// prepended to all paths ("mem://<id>/<volume>") to emulate a Windows
// drive letter, and returns it to allow chaining after NewMemFileSystem.
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
	if len(pathParts) == 0 {
		return "", errors.New("invalid filename")
	}

	// Create all parent directories if path has multiple parts
	if len(pathParts) > 1 {
		parentPath := fs.CleanPath(pathParts[:len(pathParts)-1]...)
		err := fs.makeAllDirs(parentPath, 0)
		if err != nil {
			return "", err
		}
	}

	// Get parent directory node
	var parentDir *memFileNode
	if len(pathParts) == 1 {
		parentDir = &fs.root
	} else {
		parentDir, _ = fs.pathNodeOrNil(fs.CleanPath(pathParts[:len(pathParts)-1]...))
		if parentDir == nil || !parentDir.IsDir() {
			return "", errors.New("parent directory does not exist")
		}
	}

	// Add file to parent directory
	fileName := pathParts[len(pathParts)-1]
	fileNode := newMemFileNode(f, modified, 0)
	fileNode.FileName = fileName
	parentDir.Dir[fileName] = fileNode

	return fs.JoinCleanFile(pathParts...), nil
}

// closedErr returns an ErrFileSystemClosed error if the file system was closed.
// Must be called with fs.mtx held.
func (fs *MemFileSystem) closedErr() error {
	if fs.root.Dir == nil {
		return fmt.Errorf("%s: %w", fs.Name(), ErrFileSystemClosed)
	}
	return nil
}

func (fs *MemFileSystem) pathNodeOrNil(filePath string) (node, parent *memFileNode) {
	if filePath == "" {
		return nil, nil
	}
	node = &fs.root
	parent = nil
	for _, name := range fs.SplitPath(filePath) {
		parent = node
		subNode, ok := node.Dir[name]
		if !ok {
			return nil, parent
		}
		node = subNode
	}
	return node, parent
}

// resolveSymlinks follows symbolic links starting at node,
// which was found at filePath, and returns the target node
// or nil if a link target does not exist. Link targets are
// interpreted as absolute paths or relative to the directory of the link.
// Must be called with fs.mtx held.
func (fs *MemFileSystem) resolveSymlinks(node *memFileNode, filePath string) *memFileNode {
	for depth := 0; node != nil && node.IsSymlink(); depth++ {
		if depth > 40 {
			return nil // too many levels of symbolic links
		}
		target := node.SymlinkTarget
		if !fs.IsAbsPath(target) {
			dir, _ := fs.SplitDirAndName(filePath)
			target = fs.CleanPath(dir, target)
		}
		node, _ = fs.pathNodeOrNil(target)
		filePath = target
	}
	return node
}

// pathParentDirNode returns the directory node that filePath should live
// under (as dictated by its path) along with the leaf name. Unlike the
// "parent" returned by pathNodeOrNil — which is only the deepest
// existing ancestor — this lookup fails when any intermediate component
// is missing, so callers can refuse to create a file at the wrong depth.
func (fs *MemFileSystem) pathParentDirNode(filePath string) (parent *memFileNode, parentDir, name string, err error) {
	parentDir, name = fs.SplitDirAndName(filePath)
	parent, _ = fs.pathNodeOrNil(parentDir)
	if parent == nil {
		return nil, parentDir, name, NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	if !parent.IsDir() {
		return nil, parentDir, name, NewErrIsNotDirectory(fs.RootDir().Join(parentDir))
	}
	return parent, parentDir, name, nil
}

// MakeDir creates a directory. Returns ErrAlreadyExists if dirPath
// exists and ErrDoesNotExist if the parent directory does not.
func (fs *MemFileSystem) MakeDir(dirPath string, perm Permissions) error {
	fs.mtx.Lock()
	err := fs.makeDir(dirPath, perm)
	fs.mtx.Unlock()
	if err == nil {
		fs.emitEvent(dirPath, fsnotify.Create)
	}
	return err
}

func (fs *MemFileSystem) makeDir(dirPath string, _ Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(dirPath); node != nil {
		return NewErrAlreadyExists(fs.RootDir().Join(dirPath))
	}
	parent, _, name, err := fs.pathParentDirNode(dirPath)
	if err != nil {
		return err
	}
	parent.Dir[name] = newMemDirNode(name, time.Now(), 0)
	return nil
}

// MakeAllDirs creates a directory and all missing parent directories.
// It does nothing if dirPath is an existing directory and returns
// ErrIsNotDirectory if any path component is an existing file.
func (fs *MemFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	fs.mtx.Lock()
	created, err := fs.makeAllDirsCollect(dirPath, perm)
	fs.mtx.Unlock()
	for _, p := range created {
		fs.emitEvent(p, fsnotify.Create)
	}
	return err
}

func (fs *MemFileSystem) makeAllDirs(dirPath string, perm Permissions) error {
	_, err := fs.makeAllDirsCollect(dirPath, perm)
	return err
}

// makeAllDirsCollect creates any missing directory components along
// dirPath and returns the paths of the directories it actually created.
// Caller must hold fs.mtx.Lock().
func (fs *MemFileSystem) makeAllDirsCollect(dirPath string, perm Permissions) ([]string, error) {
	if dirPath == "" {
		return nil, ErrEmptyPath
	}
	if fs.readOnly {
		return nil, ErrReadOnlyFileSystem
	}

	pathParts := fs.SplitPath(dirPath)
	currentNode := &fs.root
	var created []string

	for i, name := range pathParts {
		childNode, exists := currentNode.Dir[name]
		if !exists {
			childNode = newMemDirNode(name, time.Now(), perm.OrDefault(memFileSystemDefaultPermissions))
			currentNode.Dir[name] = childNode
			created = append(created, fs.CleanPath(pathParts[:i+1]...))
		} else if !childNode.IsDir() {
			return created, NewErrIsNotDirectory(fs.RootDir().Join(fs.CleanPath(pathParts...)))
		}
		currentNode = childNode
	}
	return created, nil
}

// ReadableWritable returns true for readable and true for writable
// unless SetReadOnly(true) was called.
func (fs *MemFileSystem) ReadableWritable() (readable, writable bool) {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	return true, !fs.readOnly
}

// RootDir returns the root directory of the file system.
func (fs *MemFileSystem) RootDir() File {
	return File(fs.URIPrefix + fs.Separator())
}

// ID returns the id of the file system, which is part of its URI prefix.
// A random id is assigned by NewMemFileSystem, see WithID.
func (fs *MemFileSystem) ID() string {
	return fs.id
}

func (fs *MemFileSystem) updatePrefix() {
	prefix := "mem://" + fs.id
	if fs.volume != "" {
		prefix += "/" + fs.volume
	}
	fs.PathHelper = fsimpl.PathHelper{
		URIPrefix: prefix,
		PathSep:   fs.Separator(),
		Rooted:    true,
		VolumeLen: func(filePath string) int {
			if fs.volume != "" && strings.HasPrefix(filePath, fs.volume) {
				return len(fs.volume)
			}
			return 0
		},
	}
}

// Name returns "memory file system".
func (*MemFileSystem) Name() string {
	return "memory file system"
}

// String returns a descriptive string of the file system including its prefix.
func (fs *MemFileSystem) String() string {
	return fmt.Sprintf("MemFileSystem(%s)", fs.URIPrefix)
}

// JoinCleanFile joins the URI parts and returns a File
// with the cleaned path and the prefix of this file system.
func (fs *MemFileSystem) JoinCleanFile(uri ...string) File {
	return File(fs.JoinCleanURI(uri...))
}

// VolumeName returns the volume prefix of filePath,
// or an empty string if the file system has no volume, see WithVolume.
func (fs *MemFileSystem) VolumeName(filePath string) string {
	if len(filePath) < len(fs.volume) {
		return ""
	}
	return filePath[:len(fs.volume)]
}

// Volume returns the volume name of the file system, see WithVolume.
func (fs *MemFileSystem) Volume() string {
	return fs.volume
}

// Stat returns the FileInfo of the file, following symbolic links.
func (fs *MemFileSystem) Stat(filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, _ := fs.pathNodeOrNil(filePath)
	target := fs.resolveSymlinks(node, filePath)
	if target == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	info := fs.nodeInfo(fs.JoinCleanFile(filePath), target)
	info.IsSymlink = node.IsSymlink()
	return info, nil
}

// nodeInfo returns the FileInfo of a node. Must be called with fs.mtx held.
func (fs *MemFileSystem) nodeInfo(file File, node *memFileNode) *FileInfo {
	name := node.FileName
	if node == &fs.root {
		name = ""
	}
	return &FileInfo{
		File:        file,
		Name:        name,
		Exists:      true,
		IsDir:       node.IsDir(),
		IsRegular:   !node.IsDir() && !node.IsSymlink(),
		IsSymlink:   node.IsSymlink(),
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        int64(len(node.FileData)),
		Modified:    node.Modified,
		Permissions: node.Permissions,
	}
}

// Exists reports if the file exists.
func (fs *MemFileSystem) Exists(filePath string) (bool, error) {
	if filePath == "" {
		return false, nil
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return false, err
	}
	node, _ := fs.pathNodeOrNil(filePath)
	return node != nil, nil
}

// IsSymbolicLink reports if the file is a symbolic link.
func (fs *MemFileSystem) IsSymbolicLink(filePath string) bool {
	if filePath == "" {
		return false
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(filePath)
	return node != nil && node.IsSymlink()
}

// CreateSymbolicLink creates a symbolic link at linkPath pointing to
// targetPath. targetPath is stored verbatim, so it may be relative to
// linkPath's directory or absolute.
func (fs *MemFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if targetPath == "" || linkPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	if existing, _ := fs.pathNodeOrNil(linkPath); existing != nil {
		fs.mtx.Unlock()
		return NewErrAlreadyExists(fs.RootDir().Join(linkPath))
	}
	parentDir, name := fs.SplitDirAndName(linkPath)
	parent, _ := fs.pathNodeOrNil(parentDir)
	if parent == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(parentDir))
	}
	if !parent.IsDir() {
		fs.mtx.Unlock()
		return NewErrIsNotDirectory(fs.RootDir().Join(parentDir))
	}
	parent.Dir[name] = &memFileNode{
		MemFile:       MemFile{FileName: name},
		Modified:      time.Now(),
		Permissions:   memFileSystemDefaultPermissions,
		SymlinkTarget: targetPath,
	}
	fs.mtx.Unlock()

	fs.emitEvent(linkPath, fsnotify.Create)
	return nil
}

// ReadSymbolicLink returns the target of the symbolic link at linkPath
// as stored on the node. An error is returned if the path is missing or
// is not a symbolic link.
func (fs *MemFileSystem) ReadSymbolicLink(linkPath string) (string, error) {
	if linkPath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(linkPath)
	if node == nil {
		return "", NewErrDoesNotExist(fs.RootDir().Join(linkPath))
	}
	if !node.IsSymlink() {
		return "", fmt.Errorf("%s is not a symbolic link", fs.RootDir().Join(linkPath))
	}
	return node.SymlinkTarget, nil
}

// ListDir calls the callback for every file in the directory that
// matches any of the patterns, or for all files if no patterns are passed.
// The directory is snapshotted before the first callback, so the callback
// may modify the file system.
func (fs *MemFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	// Snapshot the directory entries while holding the read lock, then
	// release it before invoking the callback. The callback must be able to
	// read from or write to this file system (e.g. CopyRecursive copying a
	// listed file into a sibling directory of the same MemFileSystem), which
	// would deadlock against a read lock held across the callback.
	infos, err := fs.dirInfoSnapshot(dirPath, patterns)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := callback(info); err != nil {
			return err
		}
	}
	return nil
}

// dirInfoSnapshot returns FileInfo values for the direct children of dirPath
// that match patterns. It acquires the read lock only for the duration of the
// snapshot so that callers can invoke callbacks without holding the lock.
func (fs *MemFileSystem) dirInfoSnapshot(dirPath string, patterns []string) ([]*FileInfo, error) {
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, _ := fs.pathNodeOrNil(dirPath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(dirPath))
	}
	if !node.IsDir() {
		return nil, NewErrIsNotDirectory(fs.RootDir().Join(dirPath))
	}

	infos := make([]*FileInfo, 0, len(node.Dir))
	for name, childNode := range node.Dir {
		matched, err := fsimpl.MatchAnyPattern(name, patterns)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		infos = append(infos, fs.nodeInfo(fs.JoinCleanFile(dirPath, name), childNode))
	}
	return infos, nil
}

// ListDirMax returns at most max files and directories in dirPath
// matching any of the patterns. A max value of -1 returns all matches.
// Results are sorted by name for deterministic iteration.
func (fs *MemFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if dirPath == "" {
		return nil, ErrEmptyPath
	}
	if max == 0 {
		return nil, nil
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, _ := fs.pathNodeOrNil(dirPath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(dirPath))
	}
	if !node.IsDir() {
		return nil, NewErrIsNotDirectory(fs.RootDir().Join(dirPath))
	}

	names := make([]string, 0, len(node.Dir))
	for name := range node.Dir {
		matched, err := fsimpl.MatchAnyPattern(name, patterns)
		if err != nil {
			return nil, err
		}
		if matched {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if max >= 0 && len(names) > max {
		names = names[:max]
	}

	files := make([]File, 0, len(names))
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		files = append(files, fs.JoinCleanFile(dirPath, name))
	}
	return files, nil
}

// ListDirInfoRecursive calls callback for every file (not directory) in
// dirPath and its sub-directories. Pattern matching applies only to
// files; sub-directories are always descended into. Entries are visited
// in sorted name order for determinism.
func (fs *MemFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	// Snapshot the whole sub-tree while holding the read lock, then release
	// it before invoking the callback so the callback may read from or write
	// to this file system without deadlocking. See ListDirInfo for details.
	infos, err := fs.dirInfoSnapshotRecursive(dirPath, patterns)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := callback(info); err != nil {
			return err
		}
	}
	return nil
}

func (fs *MemFileSystem) dirInfoSnapshotRecursive(dirPath string, patterns []string) ([]*FileInfo, error) {
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, _ := fs.pathNodeOrNil(dirPath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(dirPath))
	}
	if !node.IsDir() {
		return nil, NewErrIsNotDirectory(fs.RootDir().Join(dirPath))
	}
	var infos []*FileInfo
	if err := fs.walkDirInfoRecursive(node, dirPath, patterns, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

// walkDirInfoRecursive appends a FileInfo for every file (not directory) in
// node and its sub-directories to infos, visiting names in sorted order. It
// must be called with fs.mtx held for reading.
func (fs *MemFileSystem) walkDirInfoRecursive(node *memFileNode, dirPath string, patterns []string, infos *[]*FileInfo) error {
	names := make([]string, 0, len(node.Dir))
	for name := range node.Dir {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := node.Dir[name]
		childPath := fs.CleanPath(dirPath, name)
		if child.IsDir() {
			if err := fs.walkDirInfoRecursive(child, childPath, patterns, infos); err != nil {
				return err
			}
			continue
		}
		matched, err := fsimpl.MatchAnyPattern(name, patterns)
		if err != nil {
			return err
		}
		if !matched {
			continue
		}
		*infos = append(*infos, fs.nodeInfo(fs.JoinCleanFile(childPath), child))
	}
	return nil
}

// SetPermissions sets the permissions of the file.
func (fs *MemFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	node.Permissions = perm
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Chmod)
	return nil
}

// User returns the per-node user string previously set via SetUser, or
// the empty string if none was set. MemFileSystem does not consult any
// OS user database.
func (fs *MemFileSystem) User(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return "", NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	return node.User, nil
}

// SetUser sets the user owning the file. The user is a free-form string
// that is only stored, not interpreted.
func (fs *MemFileSystem) SetUser(filePath string, user string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	node.User = user
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Chmod)
	return nil
}

// Group returns the per-node group string previously set via SetGroup,
// or the empty string if none was set.
func (fs *MemFileSystem) Group(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return "", NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	return node.Group, nil
}

// SetGroup sets the group owning the file. The group is a free-form string
// that is only stored, not interpreted.
func (fs *MemFileSystem) SetGroup(filePath string, group string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	node.Group = group
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Chmod)
	return nil
}

// ListXAttr returns the names of all extended attributes set on the
// node at filePath. MemFileSystem does not support symbolic links to
// other nodes, so the followSymlinks argument is ignored.
func (fs *MemFileSystem) ListXAttr(filePath string, _ bool) ([]string, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if len(node.XAttrs) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(node.XAttrs))
	for name := range node.XAttrs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// GetXAttr returns the bytes stored for the named extended attribute.
// If the attribute is not set, an error is returned.
func (fs *MemFileSystem) GetXAttr(filePath string, name string, _ bool) ([]byte, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	value, ok := node.XAttrs[name]
	if !ok {
		return nil, fmt.Errorf("xattr %q not found on %s", name, fs.RootDir().Join(filePath))
	}
	return value, nil
}

// SetXAttr stores data under the named extended attribute. The flags
// argument honors xattr.XATTR_CREATE (fail if the attribute already
// exists) and xattr.XATTR_REPLACE (fail if it does not).
func (fs *MemFileSystem) SetXAttr(filePath string, name string, data []byte, flags int, _ bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	_, exists := node.XAttrs[name]
	if flags&xattrCreate != 0 && exists {
		fs.mtx.Unlock()
		return fmt.Errorf("xattr %q already exists on %s", name, fs.RootDir().Join(filePath))
	}
	if flags&xattrReplace != 0 && !exists {
		fs.mtx.Unlock()
		return fmt.Errorf("xattr %q not found on %s", name, fs.RootDir().Join(filePath))
	}
	if node.XAttrs == nil {
		node.XAttrs = make(map[string][]byte, 1)
	}
	node.XAttrs[name] = data
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Chmod)
	return nil
}

// RemoveXAttr removes the named extended attribute from the node. It
// returns an error if the attribute is not set.
func (fs *MemFileSystem) RemoveXAttr(filePath string, name string, _ bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if _, exists := node.XAttrs[name]; !exists {
		fs.mtx.Unlock()
		return fmt.Errorf("xattr %q not found on %s", name, fs.RootDir().Join(filePath))
	}
	delete(node.XAttrs, name)
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Chmod)
	return nil
}

// Touch creates an empty file if it does not exist,
// else it updates the modification time to the current time.
func (fs *MemFileSystem) Touch(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()

	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		node.Modified = time.Now()
		fs.mtx.Unlock()
		fs.emitEvent(filePath, fsnotify.Chmod)
		return nil
	}

	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		fs.mtx.Unlock()
		return err
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	fs.mtx.Unlock()
	fs.emitEvent(filePath, fsnotify.Create)
	return nil
}

// ReadAll returns a copy of the file's content.
func (fs *MemFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, _ := fs.pathNodeOrNil(filePath)
	node = fs.resolveSymlinks(node, filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if node.IsDir() {
		return nil, NewErrIsDirectory(fs.RootDir().Join(filePath))
	}
	return node.FileData, nil
}

// WriteAll writes data to the file, creating it if it does not exist
// and replacing its content if it does.
func (fs *MemFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()

	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		node.FileData = data
		node.Modified = time.Now()
		fs.mtx.Unlock()
		fs.emitEvent(filePath, fsnotify.Write)
		return nil
	}

	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		fs.mtx.Unlock()
		return err
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name, FileData: data},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	fs.mtx.Unlock()
	fs.emitEvent(filePath, fsnotify.Create|fsnotify.Write)
	return nil
}

// Append appends data to the file, creating it if it does not exist.
func (fs *MemFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()

	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		node.FileData = append(node.FileData, data...)
		node.Modified = time.Now()
		fs.mtx.Unlock()
		fs.emitEvent(filePath, fsnotify.Write)
		return nil
	}

	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		fs.mtx.Unlock()
		return err
	}
	parent.Dir[name] = newMemFileNode(
		MemFile{FileName: name, FileData: data},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	fs.mtx.Unlock()
	fs.emitEvent(filePath, fsnotify.Create|fsnotify.Write)
	return nil
}

// OpenReader opens the file for reading. The returned reader sees the
// content as of the time the file was opened; a later WriteAll or
// OpenWriter replaces the content and does not affect it.
func (fs *MemFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, _ := fs.pathNodeOrNil(filePath)
	node = fs.resolveSymlinks(node, filePath)
	if node == nil {
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if node.IsDir() {
		return nil, NewErrIsDirectory(fs.RootDir().Join(filePath))
	}
	return fsimpl.NewReadonlyFileBuffer(node.FileData, node), nil
}

// OpenWriter opens the file for writing, creating it if it does not exist
// and truncating it if it does. Every Write is immediately visible to
// readers of the file.
func (fs *MemFileSystem) OpenWriter(filePath string, perm Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return nil, ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		if node.IsDir() {
			return nil, NewErrIsDirectory(fs.RootDir().Join(filePath))
		}
		// File exists, truncate and write
		node.FileData = nil
		node.Modified = time.Now()
		return &memFileWriter{fs: fs, node: node, path: filePath, closeEvent: fsnotify.Write}, nil
	}

	// Create new file
	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		return nil, err
	}
	newNode := newMemFileNode(
		MemFile{FileName: name},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	parent.Dir[name] = newNode
	return &memFileWriter{fs: fs, node: newNode, path: filePath, closeEvent: fsnotify.Create | fsnotify.Write}, nil
}

// OpenAppendWriter opens the file for appending,
// creating it if it does not exist.
func (fs *MemFileSystem) OpenAppendWriter(filePath string, perm Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return nil, ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		if node.IsDir() {
			return nil, NewErrIsDirectory(fs.RootDir().Join(filePath))
		}
		return &memFileWriter{fs: fs, node: node, append: true, path: filePath, closeEvent: fsnotify.Write}, nil
	}

	// Create new file
	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		return nil, err
	}
	newNode := newMemFileNode(
		MemFile{FileName: name},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	parent.Dir[name] = newNode
	return &memFileWriter{fs: fs, node: newNode, append: true, path: filePath, closeEvent: fsnotify.Create | fsnotify.Write}, nil
}

// OpenReadWriter opens the file for reading and writing at any offset,
// creating it if it does not exist without truncating existing content.
func (fs *MemFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.readOnly {
		return nil, ErrReadOnlyFileSystem
	}

	if node, _ := fs.pathNodeOrNil(filePath); node != nil {
		if node.IsDir() {
			return nil, NewErrIsDirectory(fs.RootDir().Join(filePath))
		}
		return &memFileReadWriter{fs: fs, node: node, buf: fsimpl.NewFileBuffer(node.FileData), path: filePath, closeEvent: fsnotify.Write}, nil
	}

	// Create new file
	parent, _, name, err := fs.pathParentDirNode(filePath)
	if err != nil {
		return nil, err
	}
	newNode := newMemFileNode(
		MemFile{FileName: name},
		time.Now(),
		perm.OrDefault(memFileSystemDefaultPermissions),
	)
	parent.Dir[name] = newNode
	return &memFileReadWriter{fs: fs, node: newNode, buf: fsimpl.NewFileBuffer(nil), path: filePath, closeEvent: fsnotify.Create | fsnotify.Write}, nil
}

// Watch registers onEvent for changes to filePath. Events are synthesized
// from every mutation that goes through the MemFileSystem API
// (Touch, WriteAll, OpenWriter close, Append, Truncate, Rename, Move,
// Remove, MakeDir, SetPermissions, SetUser, SetGroup, SetXAttr,
// RemoveXAttr, CreateSymbolicLink). Direct mutation of a MemFile.FileData
// byte slice obtained outside the API is not observable and emits no
// event.
//
// When filePath is a directory the callback fires for events on its
// direct children (not deeper sub-directories), matching the
// WatchFileSystem contract.
//
// Callbacks are invoked from a new goroutine per event so a slow consumer
// can never block the mutation that produced it. Panics inside the
// callback are recovered and silently discarded.
//
// Because each event is dispatched in its own goroutine, the delivery
// order of events to a given callback is not guaranteed. Tests
// should assert on the set of events received rather than on a strict
// sequence.
func (fs *MemFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if onEvent == nil {
		return nil, errors.New("nil callback")
	}

	fs.mtx.RLock()
	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.RUnlock()
		return nil, NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	isDir := node.IsDir()
	fs.mtx.RUnlock()

	key := fs.CleanPath(filePath)

	fs.watchMtx.Lock()
	if fs.watches == nil {
		fs.watches = make(map[string]map[uint64]memWatch, 1)
	}
	id := fs.lastWatchID
	fs.lastWatchID++
	subs := fs.watches[key]
	if subs == nil {
		subs = make(map[uint64]memWatch, 1)
		fs.watches[key] = subs
	}
	subs[id] = memWatch{callback: onEvent, isDir: isDir}
	fs.watchMtx.Unlock()

	cancel = func() error {
		fs.watchMtx.Lock()
		defer fs.watchMtx.Unlock()
		if subs, ok := fs.watches[key]; ok {
			delete(subs, id)
			if len(subs) == 0 {
				delete(fs.watches, key)
			}
		}
		return nil
	}
	return cancel, nil
}

// emitEvent dispatches an event for path to every registered watch
// that targets path itself, or whose target is the parent directory of
// path (so callers watching a directory see events for direct children).
// Each callback runs in its own goroutine so dispatch never blocks the
// mutation under fs.mtx that produced the event.
//
// path is interpreted as a raw filesystem path; emitEvent cleans it
// before matching.
func (fs *MemFileSystem) emitEvent(path string, op fsnotify.Op) {
	cleanPath := fs.CleanPath(path)
	parentPath, _ := fs.SplitDirAndName(cleanPath)
	if parentPath == "" {
		parentPath = fs.Separator()
	}

	fs.watchMtx.Lock()
	if fs.watches == nil {
		fs.watchMtx.Unlock()
		return
	}
	var callbacks []func(File, Event)
	for _, w := range fs.watches[cleanPath] {
		callbacks = append(callbacks, w.callback)
	}
	// Skip the parent-dir loop when the event path equals its parent
	// (only happens for the root): otherwise a directory watch on root
	// would receive the event twice.
	if cleanPath != parentPath {
		for _, w := range fs.watches[parentPath] {
			if w.isDir {
				callbacks = append(callbacks, w.callback)
			}
		}
	}
	fs.watchMtx.Unlock()

	if len(callbacks) == 0 {
		return
	}
	eventFile := fs.JoinCleanFile(cleanPath)
	event := Event(op)
	for _, cb := range callbacks {
		go func(cb func(File, Event)) {
			defer func() { _ = recover() }()
			cb(eventFile, event)
		}(cb)
	}
}

// Truncate changes the size of the file,
// padding with zero bytes when growing it.
func (fs *MemFileSystem) Truncate(filePath string, newSize int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()

	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	node, _ := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	currentSize := int64(len(node.FileData))
	if currentSize == newSize {
		fs.mtx.Unlock()
		return nil
	}
	if currentSize > newSize {
		node.FileData = node.FileData[:newSize]
	} else {
		node.FileData = append(node.FileData, make([]byte, newSize-currentSize)...)
	}
	node.Modified = time.Now()
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Write)
	return nil
}

// CopyFile copies a file within the file system
// by copying its content and permissions.
func (fs *MemFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if srcFile == "" || destFile == "" {
		return ErrEmptyPath
	}
	if fs.CleanPath(srcFile) == fs.CleanPath(destFile) {
		return nil
	}

	// Read source file
	srcData, err := fs.ReadAll(ctx, srcFile)
	if err != nil {
		return err
	}

	// Write to destination file
	return fs.WriteAll(ctx, destFile, srcData, 0)
}

// Rename renames the node at filePath to newName within its existing
// parent directory. newName must be a leaf name without the file
// system's path separator. The new full path is returned.
func (fs *MemFileSystem) Rename(filePath string, newName string) (string, error) {
	if filePath == "" || newName == "" {
		return "", ErrEmptyPath
	}
	if strings.Contains(newName, fs.Separator()) {
		return "", fmt.Errorf("newName %q for Rename contains path separator %s", newName, fs.Separator())
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return "", ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return "", NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if parent == nil {
		fs.mtx.Unlock()
		return "", errors.New("cannot rename root directory")
	}
	if _, exists := parent.Dir[newName]; exists {
		fs.mtx.Unlock()
		parentDir, _ := fs.SplitDirAndName(filePath)
		return "", NewErrAlreadyExists(fs.RootDir().Join(parentDir, newName))
	}

	parentDir, srcName := fs.SplitDirAndName(filePath)
	delete(parent.Dir, srcName)
	node.FileName = newName
	node.Modified = time.Now()
	parent.Dir[newName] = node
	newPath := fs.CleanPath(parentDir, newName)
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Rename)
	fs.emitEvent(newPath, fsnotify.Create)
	return newPath, nil
}

// Move moves the node at filePath to destPath. If destPath resolves to
// an existing directory the source is moved *into* it using its base
// name; otherwise destPath is treated as the new full path. The
// destination must not already exist as a file or non-directory node.
//
// When filePath and destPath resolve to the same location after cleaning,
// Move returns nil without touching the node, matching the no-op behavior
// of [os.Rename] required by the [MoveFileSystem] contract.
func (fs *MemFileSystem) Move(filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	filePath = fs.CleanPath(filePath)
	destPath = fs.CleanPath(destPath)
	if filePath == destPath {
		return nil
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	srcNode, srcParent := fs.pathNodeOrNil(filePath)
	if srcNode == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if srcParent == nil {
		fs.mtx.Unlock()
		return errors.New("cannot move root directory")
	}

	// Refuse moves that would put a directory inside one of its
	// descendants. Without this, the moved subtree would be orphaned and
	// self-referential.
	if srcNode.IsDir() && strings.HasPrefix(destPath, filePath+fs.Separator()) {
		fs.mtx.Unlock()
		return fmt.Errorf("cannot move %s into a descendant (%s)", fs.RootDir().Join(filePath), fs.RootDir().Join(destPath))
	}

	if existing, _ := fs.pathNodeOrNil(destPath); existing != nil {
		fs.mtx.Unlock()
		return NewErrAlreadyExists(fs.RootDir().Join(destPath))
	}

	destDir, destName := fs.SplitDirAndName(destPath)
	destParent, _ := fs.pathNodeOrNil(destDir)
	if destParent == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(destDir))
	}
	if !destParent.IsDir() {
		fs.mtx.Unlock()
		return NewErrIsNotDirectory(fs.RootDir().Join(destDir))
	}

	_, srcName := fs.SplitDirAndName(filePath)
	delete(srcParent.Dir, srcName)
	srcNode.FileName = destName
	srcNode.Modified = time.Now()
	destParent.Dir[destName] = srcNode
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Rename)
	fs.emitEvent(destPath, fsnotify.Create)
	return nil
}

// Remove removes a file or an empty directory,
// use File.RemoveRecursive for a directory with content.
func (fs *MemFileSystem) Remove(filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()

	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}

	node, parent := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return NewErrDoesNotExist(fs.RootDir().Join(filePath))
	}
	if parent == nil {
		fs.mtx.Unlock()
		return errors.New("cannot remove root directory")
	}
	if node.IsDir() && len(node.Dir) > 0 {
		fs.mtx.Unlock()
		return fmt.Errorf("directory not empty: %s", fs.RootDir().Join(filePath))
	}

	// Remove from parent directory
	_, name := fs.SplitDirAndName(filePath)
	delete(parent.Dir, name)
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Remove)
	return nil
}

// RemoveAll removes filePath and any children it contains.
// No error is returned if the path does not exist.
func (fs *MemFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	if fs.readOnly {
		fs.mtx.Unlock()
		return ErrReadOnlyFileSystem
	}
	node, parent := fs.pathNodeOrNil(filePath)
	if node == nil {
		fs.mtx.Unlock()
		return nil
	}
	if parent == nil {
		fs.mtx.Unlock()
		return errors.New("cannot remove root directory")
	}
	_, name := fs.SplitDirAndName(filePath)
	delete(parent.Dir, name)
	fs.mtx.Unlock()

	fs.emitEvent(filePath, fsnotify.Remove)
	return nil
}

// Close unregisters the file system and discards all its files.
// Every following operation returns ErrFileSystemClosed.
// Closing an already closed file system is a no-op.
func (fs *MemFileSystem) Close() error {
	fs.mtx.Lock()
	if fs.root.Dir == nil {
		fs.mtx.Unlock()
		return nil // already closed
	}
	fs.root.Dir = nil
	fs.mtx.Unlock() // Unlock before Unregister to avoid deadlock

	fs.watchMtx.Lock()
	fs.watches = nil
	fs.watchMtx.Unlock()

	Unregister(fs)
	return nil
}

// Clear removes all files and directories
// but keeps the file system usable.
func (fs *MemFileSystem) Clear() {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	clear(fs.root.Dir)
	fs.root.Modified = time.Now()
}

// memFileWriter implements WriteCloser for MemFileSystem
type memFileWriter struct {
	fs         *MemFileSystem
	node       *memFileNode
	append     bool
	path       string
	closeEvent fsnotify.Op
	closed     bool
}

func (w *memFileWriter) Write(p []byte) (n int, err error) {
	if w.closed {
		return 0, errors.New("write to closed file")
	}
	w.fs.mtx.Lock()
	defer w.fs.mtx.Unlock()

	w.node.FileData = append(w.node.FileData, p...)
	w.node.Modified = time.Now()
	return len(p), nil
}

func (w *memFileWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.closeEvent != 0 {
		w.fs.emitEvent(w.path, w.closeEvent)
	}
	return nil
}

// memFileReadWriter implements ReadWriteSeekCloser for MemFileSystem
type memFileReadWriter struct {
	fs         *MemFileSystem
	node       *memFileNode
	buf        *fsimpl.FileBuffer
	path       string
	closeEvent fsnotify.Op
	closed     bool
}

func (rw *memFileReadWriter) Read(p []byte) (n int, err error) {
	rw.fs.mtx.RLock()
	defer rw.fs.mtx.RUnlock()
	return rw.buf.Read(p)
}

func (rw *memFileReadWriter) ReadAt(p []byte, off int64) (n int, err error) {
	rw.fs.mtx.RLock()
	defer rw.fs.mtx.RUnlock()
	return rw.buf.ReadAt(p, off)
}

func (rw *memFileReadWriter) Write(p []byte) (n int, err error) {
	rw.fs.mtx.Lock()
	defer rw.fs.mtx.Unlock()
	n, err = rw.buf.Write(p)
	if err == nil {
		rw.node.FileData = rw.buf.Bytes()
		rw.node.Modified = time.Now()
	}
	return n, err
}

func (rw *memFileReadWriter) WriteAt(p []byte, off int64) (n int, err error) {
	rw.fs.mtx.Lock()
	defer rw.fs.mtx.Unlock()
	n, err = rw.buf.WriteAt(p, off)
	if err == nil {
		rw.node.FileData = rw.buf.Bytes()
		rw.node.Modified = time.Now()
	}
	return n, err
}

func (rw *memFileReadWriter) Seek(offset int64, whence int) (int64, error) {
	rw.fs.mtx.Lock()
	defer rw.fs.mtx.Unlock()
	return rw.buf.Seek(offset, whence)
}

func (rw *memFileReadWriter) Close() error {
	rw.fs.mtx.Lock()
	if rw.closed {
		rw.fs.mtx.Unlock()
		return nil
	}
	rw.node.FileData = rw.buf.Bytes()
	rw.node.Modified = time.Now()
	err := rw.buf.Close()
	rw.closed = true
	rw.fs.mtx.Unlock()

	if rw.closeEvent != 0 {
		rw.fs.emitEvent(rw.path, rw.closeEvent)
	}
	return err
}
