package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	_ VolumeNameFileSystem       = new(MemFileSystem)
	_ WatchFileSystem            = new(MemFileSystem)
	_ PermissionsFileSystem      = new(MemFileSystem)
	_ UserFileSystem             = new(MemFileSystem)
	_ GroupFileSystem            = new(MemFileSystem)
	_ ListDirRecursiveFileSystem = new(MemFileSystem)
	_ XAttrFileSystem            = new(MemFileSystem)
	_ SymbolicLinkFileSystem     = new(MemFileSystem)
)

var memFileSystemDefaultPermissions = UserAndGroupReadWrite

// memMaxSymlinks is the maximum number of symbolic links
// followed while resolving one path, like the limit of Linux.
const memMaxSymlinks = 40

// memFileNode is a file, directory or symbolic link of a MemFileSystem.
//
// A node is one of three kinds:
//   - regular file: Dir == nil && SymlinkTarget == ""
//   - directory:    Dir != nil
//   - symbolic link: SymlinkTarget != "" (Dir is always nil, FileData unused)
//
// The bytes of FileData up to its length are never modified in place,
// readers share them without copying. A change stores a new slice,
// appends behind the length into capacity that no other slice covers,
// or shrinks the slice with its capacity limited to the new length,
// so that a later append can't overwrite bytes that a reader still sees.
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

// IsDir reports if the node is a directory, false for a nil node.
func (n *memFileNode) IsDir() bool {
	return n != nil && n.Dir != nil
}

// IsSymlink reports if the node is a symbolic link, false for a nil node.
func (n *memFileNode) IsSymlink() bool {
	return n != nil && n.SymlinkTarget != ""
}

// isRegular reports if the node is a regular file, false for a nil node.
func (n *memFileNode) isRegular() bool {
	return n != nil && n.Dir == nil && n.SymlinkTarget == ""
}

// size returns the size of a file, the length of the target
// of a symbolic link like os.Lstat, or zero for a directory.
func (n *memFileNode) size() int64 {
	if n.IsSymlink() {
		return int64(len(n.SymlinkTarget))
	}
	return int64(len(n.FileData))
}

// MemFileSystem is a fully featured thread-safe file system implementation
// that stores all files and directories in random access memory.
//
// # Inner Workings
//
// The file system is organized as a tree structure where each node (memFileNode)
// represents a file, a directory or a symbolic link. Directory nodes contain
// a map of child nodes indexed by name. Files store their content as byte slices
// ([]byte) in the MemFile.FileData field.
//
// All operations are protected by a read-write mutex (sync.RWMutex) to ensure
// thread-safety. Read operations (Stat, OpenReader, ListDir) acquire read
// locks, while write operations (OpenWriter, MakeDir, Remove) acquire write locks.
//
// The stored content of a file is never modified in place: a change stores
// new bytes or appends behind the bytes that readers can see. So readers
// share the content without copying it, and callers never share a slice
// with the file system:
//   - OpenReader returns a fsimpl.ReadonlyFileBuffer with a snapshot of the
//     content at the time of the call, later changes don't affect it
//   - ReadAll returns a copy of the content, WriteAll and CopyFile store a copy
//   - OpenWriter and OpenAppendWriter return a writer whose writes are
//     visible to every reader that is opened after them
//   - OpenReadWriter returns a reader-writer with full seek support
//     that works on a copy of the content and stores it on Close
//
// Symbolic links are resolved like by an operating system: links in the
// directories of a path are always followed, a link as last element of a path
// is followed by the operations that work with the content or the attributes
// of a file (Stat, Exists, OpenReader, WriteAll, SetPermissions and so on)
// and not by the operations that work with the directory entry
// (Lstat, Remove, Move, MakeDir, CreateSymbolicLink, ReadSymbolicLink).
// Writing through a link writes the target, or creates it if it doesn't exist.
//
// The file system supports configurable path separators ("/" or "\") and optional
// volume names (e.g., "C:") for Windows-style paths, see MemFileSystemOptions.
// After registering it with Register, its files can be accessed via
// URIs like "mem://<id>/path/to/file", or `mem://<id>/C:\path\to\file`
// for a file system with the volume "C:" and the separator "\".
//
// # Use Cases
//
// Useful as a mock file system for tests or for caching of slow file systems.
// All data is lost when the file system is closed or the process terminates.
type MemFileSystem struct {
	paths fsimpl.PathHelper // Path methods for the prefix, separator and volume

	id       string       // Unique identifier for this file system instance
	volume   string       // Optional volume name (e.g., "C:")
	readOnly bool         // If true, write operations return ErrReadOnlyFileSystem
	closed   bool         // Set by Close, every method returns ErrFileSystemClosed then
	root     memFileNode  // Root directory node
	mtx      sync.RWMutex // Protects all fields except paths, id and volume, and all nodes

	watches     map[string]map[uint64]*memWatch // keyed by the clean watch path
	lastWatchID uint64
}

// MemFileSystemOptions are the options for NewMemFileSystem.
// The zero value (or a nil pointer) creates a file system
// with a random ID and "/" as separator.
type MemFileSystemOptions struct {
	// ID of the file system that is part of its URI prefix "mem://<ID>",
	// or "mem://<ID>/" with a Volume. A random ID is generated if empty.
	ID string

	// Separator of the file system paths, "/" or "\".
	// Defaults to "/" if empty.
	Separator string

	// Volume is an optional volume name like "C:" to emulate
	// a Windows drive letter. Like on Windows the clean paths
	// of the file system start with it, so the URI of `C:\dir`
	// is `mem://<ID>/C:\dir`. The methods also accept paths
	// without it: `C:\dir` and `\dir` are the same directory.
	Volume string

	// ReadOnly makes the file system reject every write operation
	// with ErrReadOnlyFileSystem, see also MemFileSystem.SetReadOnly.
	ReadOnly bool
}

// NewMemFileSystem creates a new MemFileSystem with the passed options,
// nil options use the defaults of MemFileSystemOptions,
// and adds copies of the initialFiles with the current time as modification time.
// The FileName of an initial file can be a path with the separator,
// in which case all directories of the path are created.
//
// The file system is not registered. Pass it to Register
// to make its files accessible via their URIs,
// and call Close to unregister it again:
//
//	memFS, err := fs.NewMemFileSystem(nil)
//	if err != nil {
//		return err
//	}
//	if err := fs.Register(memFS); err != nil {
//		return err
//	}
//	defer memFS.Close()
func NewMemFileSystem(opts *MemFileSystemOptions, initialFiles ...MemFile) (*MemFileSystem, error) {
	var o MemFileSystemOptions
	if opts != nil {
		o = *opts
	}
	// Validate arguments
	if o.Separator == "" {
		o.Separator = "/"
	}
	if o.Separator != `/` && o.Separator != `\` {
		return nil, fmt.Errorf("invalid separator %q", o.Separator)
	}
	if strings.ContainsAny(o.ID, `/\`) {
		return nil, fmt.Errorf("invalid MemFileSystem ID %q", o.ID)
	}
	if strings.ContainsAny(o.Volume, `/\`) {
		return nil, fmt.Errorf("invalid MemFileSystem volume %q", o.Volume)
	}
	for i, f := range initialFiles {
		if f.FileName == "" {
			return nil, fmt.Errorf("empty initialFiles[%d].FileName", i)
		}
	}
	if o.ID == "" {
		o.ID = fsimpl.RandomString()
	}

	// Create MemFileSystem
	now := time.Now()
	memFS := &MemFileSystem{
		paths:    fsimpl.PathHelper{PathSep: o.Separator},
		id:       o.ID,
		volume:   o.Volume,
		readOnly: o.ReadOnly,
		root: memFileNode{
			MemFile:     MemFile{FileName: o.Separator},
			Modified:    now,
			Permissions: memFileSystemDefaultPermissions,
			Dir:         make(map[string]*memFileNode, len(initialFiles)),
		},
	}
	memFS.updatePrefix()

	// Add initial files
	for _, f := range initialFiles {
		_, err := memFS.addMemFile(f, now)
		if err != nil {
			return nil, err
		}
	}
	return memFS, nil
}

// NewMemFileSystemAndRegister creates a new MemFileSystem like NewMemFileSystem
// and registers it. The options must have an ID, so that the files of the
// file system can be accessed via the URI prefix "mem://<ID>".
func NewMemFileSystemAndRegister(opts *MemFileSystemOptions, initialFiles ...MemFile) error {
	if opts == nil || opts.ID == "" {
		return errors.New("NewMemFileSystemAndRegister needs options with an ID")
	}
	memFS, err := NewMemFileSystem(opts, initialFiles...)
	if err != nil {
		return err
	}
	return Register(memFS)
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
	defer fs.mtx.Unlock()

	fs.readOnly = readOnly
}

// AddMemFile adds a copy of a MemFile to the file system with the given modified time.
// The MemFile.FileName can be a path with the path separator of the MemFileSystem,
// in which case all directories of the path are created.
// An existing file or symbolic link with that path is replaced,
// an existing directory is an ErrIsDirectory.
func (fs *MemFileSystem) AddMemFile(f MemFile, modified time.Time) (File, error) {
	if f.FileName == "" {
		return "", errors.New("empty filename")
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return "", err
	}
	return fs.addMemFile(f, modified)
}

// addMemFile adds a copy of a MemFile without checking if the file system
// is closed or read-only. Must be called with fs.mtx held for writing.
func (fs *MemFileSystem) addMemFile(f MemFile, modified time.Time) (File, error) {
	elems := fs.elems(f.FileName)
	if len(elems) == 0 {
		return "", errors.New("invalid filename")
	}
	if len(elems) > 1 {
		if _, err := fs.makeAllDirs(elems[:len(elems)-1], 0); err != nil {
			return "", err
		}
	}
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return "", err
	}
	if r.node.IsDir() {
		return "", NewErrIsDirectory(fs.elemsFile(elems))
	}
	if err := fs.checkParent(r, elems); err != nil {
		return "", err
	}
	r.parent.Dir[r.name] = newMemFileNode(MemFile{FileName: r.name, FileData: bytes.Clone(f.FileData)}, modified, 0)
	return fs.elemsFile(elems), nil
}

// closedErr returns an ErrFileSystemClosed error if the file system was closed.
// Must be called with fs.mtx held.
func (fs *MemFileSystem) closedErr() error {
	if fs.closed {
		return fmt.Errorf("%s: %w", fs, ErrFileSystemClosed)
	}
	return nil
}

// writableErr returns an ErrFileSystemClosed error if the file system was closed
// or ErrReadOnlyFileSystem if it is read-only. Must be called with fs.mtx held.
func (fs *MemFileSystem) writableErr() error {
	if err := fs.closedErr(); err != nil {
		return err
	}
	if fs.readOnly {
		return ErrReadOnlyFileSystem
	}
	return nil
}

// elems returns the elements of the cleaned filePath
// without prefix and volume, nil for the root.
func (fs *MemFileSystem) elems(filePath string) []string {
	return fs.paths.SplitPath(fs.CleanPath(filePath))
}

// elemsPath returns the clean path of path elements,
// starting with the volume if the file system has one.
func (fs *MemFileSystem) elemsPath(elems []string) string {
	return fs.volume + fs.Separator() + strings.Join(elems, fs.Separator())
}

// elemsFile returns the File of path elements, built from the prefix of the
// file system, so it is valid even if the file system is not registered.
func (fs *MemFileSystem) elemsFile(elems []string) File {
	return File(fs.paths.URL(fs.elemsPath(elems)))
}

// fileOf returns the File of filePath, see elemsFile.
func (fs *MemFileSystem) fileOf(filePath string) File {
	return fs.elemsFile(fs.elems(filePath))
}

// parentElems returns the elements of the parent directory of elems.
func parentElems(elems []string) []string {
	if len(elems) == 0 {
		return nil
	}
	return elems[:len(elems)-1]
}

// lastElem returns the last of elems, or "" for the root.
func lastElem(elems []string) string {
	if len(elems) == 0 {
		return ""
	}
	return elems[len(elems)-1]
}

// hasElemsPrefix reports if elems starts with prefix.
func hasElemsPrefix(elems, prefix []string) bool {
	return len(elems) >= len(prefix) && slices.Equal(elems[:len(prefix)], prefix)
}

// memResolved is a path of a MemFileSystem resolved by resolveElems.
type memResolved struct {
	node   *memFileNode // the node at the path, nil if it does not exist
	parent *memFileNode // the node at the parent path if it exists (it may not be a directory), nil for the root
	dir    []string     // the elements of the parent directory with all symbolic links resolved, if it exists
	name   string       // the last element, "" for the root
}

// path returns the elements of the resolved path without symbolic links.
func (r memResolved) path() []string {
	if r.name == "" {
		return nil
	}
	return append(slices.Clone(r.dir), r.name)
}

// resolve resolves filePath like resolveElems.
func (fs *MemFileSystem) resolve(filePath string, followLast bool) (memResolved, error) {
	return fs.resolveElems(fs.elems(filePath), followLast)
}

// resolveElems walks the path elements from the root. Symbolic links
// in the directory elements are always followed, a link as last element
// only if followLast is true. A link target is an absolute path
// or relative to the directory of the link.
// If the path does not exist, the result has a nil node, and a parent
// if the parent directory exists. Following more than memMaxSymlinks
// links is an error. Must be called with fs.mtx held.
func (fs *MemFileSystem) resolveElems(elems []string, followLast bool) (memResolved, error) {
	var (
		pending = elems
		dir     = &fs.root
		dirPath []string
		links   int
	)
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		node := dir.Dir[name]
		if node.IsSymlink() && (len(pending) > 0 || followLast) {
			links++
			if links > memMaxSymlinks {
				return memResolved{}, fmt.Errorf("too many levels of symbolic links: %s", fs.elemsFile(elems))
			}
			pending = append(fs.linkTargetElems(dirPath, node.SymlinkTarget), pending...)
			dir, dirPath = &fs.root, nil
			continue
		}
		if len(pending) == 0 {
			return memResolved{node: node, parent: dir, dir: dirPath, name: name}, nil
		}
		if !node.IsDir() {
			// A directory of the path is missing or not a directory
			r := memResolved{name: pending[len(pending)-1]}
			if node != nil && len(pending) == 1 {
				r.parent = node // The parent of the last element is not a directory
			}
			return r, nil
		}
		dir = node
		dirPath = append(dirPath, name)
	}
	return memResolved{node: &fs.root}, nil
}

// linkTargetElems returns the path elements of the target of a symbolic
// link that is in the directory with the elements dir.
func (fs *MemFileSystem) linkTargetElems(dir []string, target string) []string {
	if !fs.isAbsPath(target) {
		target = fs.elemsPath(dir) + fs.Separator() + target
	}
	return fs.elems(target)
}

// checkParent returns an error if the parent directory of the
// path with the elements elems, resolved as r, does not exist
// or is not a directory.
func (fs *MemFileSystem) checkParent(r memResolved, elems []string) error {
	switch {
	case r.parent == nil:
		return NewErrDoesNotExist(fs.elemsFile(parentElems(elems)))
	case !r.parent.IsDir():
		return NewErrIsNotDirectory(fs.elemsFile(parentElems(elems)))
	}
	return nil
}

// fileForWrite returns the node of the regular file at filePath for
// writing its content, following symbolic links. If the file does not
// exist and create is true, an empty file with perm is created in its
// existing parent directory, else an ErrDoesNotExist is returned.
// A directory is an ErrIsDirectory. Must be called with fs.mtx held
// for writing and after the checks of writableErr.
func (fs *MemFileSystem) fileForWrite(filePath string, perm Permissions, create bool) (node *memFileNode, created bool, err error) {
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, true)
	if err != nil {
		return nil, false, err
	}
	switch {
	case r.node.IsDir():
		return nil, false, NewErrIsDirectory(fs.elemsFile(elems))
	case r.node != nil:
		return r.node, false, nil
	case !create:
		return nil, false, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	if err := fs.checkParent(r, elems); err != nil {
		return nil, false, err
	}
	node = newMemFileNode(MemFile{FileName: r.name}, time.Now(), perm)
	r.parent.Dir[r.name] = node
	return node, true, nil
}

// writeEvent returns the event of a write to a file
// that was created by the write or existed before.
func writeEvent(created bool) Event {
	if created {
		return EventCreate | EventWrite
	}
	return EventWrite
}

// MakeDir creates a directory with perm, or the default permissions
// if perm is zero. Returns ErrAlreadyExists if dirPath exists
// and ErrDoesNotExist if the parent directory does not.
func (fs *MemFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	elems := fs.elems(dirPath)
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return err
	}
	if r.node != nil {
		return NewErrAlreadyExists(fs.elemsFile(elems))
	}
	if err := fs.checkParent(r, elems); err != nil {
		return err
	}
	r.parent.Dir[r.name] = newMemDirNode(r.name, time.Now(), perm)
	fs.emitEvent(dirPath, EventCreate)
	return nil
}

// MakeAllDirs creates a directory and all missing parent directories
// with perm, or the default permissions if perm is zero.
// It does nothing if dirPath is an existing directory and returns
// ErrIsNotDirectory if any path element is an existing file.
// Symbolic links to directories are followed.
func (fs *MemFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if dirPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	created, err := fs.makeAllDirs(fs.elems(dirPath), perm)
	for _, elems := range created {
		fs.emitEvent(fs.elemsPath(elems), EventCreate)
	}
	return err
}

// makeAllDirs creates the missing directories of the path elements
// and returns the elements of the directories it created.
// Must be called with fs.mtx held for writing.
func (fs *MemFileSystem) makeAllDirs(elems []string, perm Permissions) (created [][]string, err error) {
	for i := range elems {
		dirElems := elems[:i+1]
		r, err := fs.resolveElems(dirElems, true)
		if err != nil {
			return created, err
		}
		switch {
		case r.node.IsDir():
			continue
		case r.node != nil:
			return created, NewErrIsNotDirectory(fs.elemsFile(dirElems))
		}
		if link, _ := fs.resolveElems(dirElems, false); link.node.IsSymlink() {
			// A symbolic link to a missing target is not a directory
			return created, NewErrIsNotDirectory(fs.elemsFile(dirElems))
		}
		if err := fs.checkParent(r, dirElems); err != nil {
			return created, err
		}
		r.parent.Dir[r.name] = newMemDirNode(r.name, time.Now(), perm)
		created = append(created, dirElems)
	}
	return created, nil
}

// ReadableWritable returns true for readable and true for writable
// unless SetReadOnly(true) was called.
func (fs *MemFileSystem) ReadableWritable() (readable, writable bool) {
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	return true, !fs.readOnly
}

// RootDir returns the root directory of the file system.
func (fs *MemFileSystem) RootDir() File {
	return fs.elemsFile(nil)
}

// ID returns the id of the file system, which is part of its URI prefix.
// A random id is assigned by NewMemFileSystem if MemFileSystemOptions.ID is empty.
func (fs *MemFileSystem) ID() string {
	return fs.id
}

// Prefix returns the URI prefix "mem://<ID>" of the file system,
// or "mem://<ID>/" if it has a volume, because its paths start with the volume.
func (fs *MemFileSystem) Prefix() string {
	return fs.paths.URIPrefix
}

// Separator returns the path separator of the file system, "/" or "\\".
func (fs *MemFileSystem) Separator() string {
	return fs.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
// If the file system has a volume, the path starts with it,
// also if the uriParts don't: `\dir` becomes `C:\dir`.
func (fs *MemFileSystem) CleanPath(uriParts ...string) string {
	cleanPath := fs.paths.CleanPath(uriParts...)
	if fs.volume != "" && fs.VolumeName(cleanPath) == "" {
		cleanPath = fs.volume + cleanPath
	}
	return cleanPath
}

// isAbsPath reports if filePath starts with a slash, the separator or the volume.
func (fs *MemFileSystem) isAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, "/") ||
		strings.HasPrefix(filePath, fs.Separator()) ||
		fs.VolumeName(filePath) != ""
}

func (fs *MemFileSystem) updatePrefix() {
	prefix := "mem://" + fs.id
	if fs.volume != "" {
		// The paths start with the volume, so a slash has to
		// separate it from the ID, see the prefix boundary rule
		// of FileSystem: mem://<ID>/C:\dir
		prefix += "/"
	}
	fs.paths = fsimpl.PathHelper{
		URIPrefix: prefix,
		PathSep:   fs.paths.Separator(),
		Rooted:    true,
		VolumeLen: func(filePath string) int {
			return len(fs.VolumeName(filePath))
		},
	}
}

// Name returns "memory file system".
func (*MemFileSystem) Name() string {
	return "memory file system"
}

// String returns a descriptive string of the file system including its prefix.
func (fs *MemFileSystem) String() string {
	return fmt.Sprintf("MemFileSystem(%s)", fs.paths.URIPrefix)
}

// JoinCleanFile joins the URI parts and returns a File
// with the cleaned path and the prefix of this file system.
// The path of the File starts with the volume if the file system has one.
func (fs *MemFileSystem) JoinCleanFile(uri ...string) File {
	return fs.fileOf(fs.CleanPath(uri...))
}

// VolumeName returns the volume of the file system if filePath starts with it,
// else an empty string, see MemFileSystemOptions.Volume.
func (fs *MemFileSystem) VolumeName(filePath string) string {
	if fs.volume != "" && strings.HasPrefix(filePath, fs.volume) {
		return fs.volume
	}
	return ""
}

// Volume returns the volume name of the file system, see MemFileSystemOptions.Volume.
func (fs *MemFileSystem) Volume() string {
	return fs.volume
}

// memNodeInfo returns the FileInfo of node, the entry named name at file.
// For a symbolic link node, target is the node the link resolves to
// or nil if it is dangling: IsDir and IsRegular describe the target,
// the other fields the link itself. Must be called with fs.mtx held.
func memNodeInfo(file File, name string, node, target *memFileNode) *FileInfo {
	info := &FileInfo{
		File:        file,
		Name:        name,
		Exists:      true,
		IsDir:       node.IsDir(),
		IsRegular:   node.isRegular(),
		IsSymlink:   node.IsSymlink(),
		IsHidden:    strings.HasPrefix(name, "."),
		Size:        node.size(),
		Modified:    node.Modified,
		Permissions: node.Permissions,
	}
	if node.IsSymlink() {
		info.IsDir = target.IsDir()
		info.IsRegular = target.isRegular()
	}
	return info
}

// linkTarget returns the node that the symbolic link node at the
// resolved path r points to, or nil if it is dangling or r is no link.
// Must be called with fs.mtx held.
func (fs *MemFileSystem) linkTarget(r memResolved) *memFileNode {
	if !r.node.IsSymlink() {
		return nil
	}
	target, err := fs.resolveElems(r.path(), true)
	if err != nil {
		return nil
	}
	return target.node
}

// Stat returns the FileInfo of the file, following symbolic links.
// For a symbolic link IsSymlink is set and the other fields describe the target.
func (fs *MemFileSystem) Stat(filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, true)
	if err != nil {
		return nil, err
	}
	if r.node == nil {
		return nil, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	info := memNodeInfo(fs.elemsFile(elems), lastElem(elems), r.node, nil)
	if link, _ := fs.resolveElems(elems, false); link.node.IsSymlink() {
		info.IsSymlink = true
	}
	return info, nil
}

// Exists reports if the file exists. Like Stat it follows symbolic links,
// so a symbolic link to a missing target does not exist.
func (fs *MemFileSystem) Exists(filePath string) (bool, error) {
	if filePath == "" {
		return false, nil
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return false, err
	}
	r, err := fs.resolve(filePath, true)
	if err != nil {
		return false, err
	}
	return r.node != nil, nil
}

// Lstat returns the FileInfo of the file without following
// a symbolic link: for a symbolic link the FileInfo
// describes the link itself with IsSymlink set,
// IsDir and IsRegular describe its target.
func (fs *MemFileSystem) Lstat(filePath string) (*FileInfo, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return nil, err
	}
	if r.node == nil {
		return nil, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	return memNodeInfo(fs.elemsFile(elems), lastElem(elems), r.node, fs.linkTarget(r)), nil
}

// CreateSymbolicLink creates a symbolic link at linkPath pointing to
// targetPath. targetPath is stored verbatim, so it may be relative to
// linkPath's directory or absolute, and it doesn't need to exist.
func (fs *MemFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	if targetPath == "" || linkPath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	elems := fs.elems(linkPath)
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return err
	}
	if r.node != nil {
		return NewErrAlreadyExists(fs.elemsFile(elems))
	}
	if err := fs.checkParent(r, elems); err != nil {
		return err
	}
	r.parent.Dir[r.name] = &memFileNode{
		MemFile:       MemFile{FileName: r.name},
		Modified:      time.Now(),
		Permissions:   memFileSystemDefaultPermissions,
		SymlinkTarget: targetPath,
	}
	fs.emitEvent(linkPath, EventCreate)
	return nil
}

// ReadSymbolicLink returns the target of the symbolic link at linkPath
// as stored on the node. An error is returned if the path is missing,
// and an error wrapping os.ErrInvalid if it is not a symbolic link.
func (fs *MemFileSystem) ReadSymbolicLink(linkPath string) (string, error) {
	if linkPath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return "", err
	}
	elems := fs.elems(linkPath)
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return "", err
	}
	if r.node == nil {
		return "", NewErrDoesNotExist(fs.elemsFile(elems))
	}
	if !r.node.IsSymlink() {
		return "", fmt.Errorf("%w: %s is not a symbolic link", os.ErrInvalid, fs.elemsFile(elems))
	}
	return r.node.SymlinkTarget, nil
}

// sortedNames returns the names of the entries of the directory node in sorted order.
func sortedNames(dir *memFileNode) []string {
	names := make([]string, 0, len(dir.Dir))
	for name := range dir.Dir {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListDir calls the callback for every file in the directory that
// matches any of the patterns, or for all files if no patterns are passed,
// in the order of the names. A symbolic link to a directory is listed
// like the directory. The FileInfo of a symbolic link in the directory
// describes the link itself, see Lstat.
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

// resolveDir resolves dirPath following symbolic links
// and returns an error if it is not an existing directory.
// Must be called with fs.mtx held.
func (fs *MemFileSystem) resolveDir(dirPath string) (elems []string, r memResolved, err error) {
	if err := fs.closedErr(); err != nil {
		return nil, r, err
	}
	elems = fs.elems(dirPath)
	r, err = fs.resolveElems(elems, true)
	if err != nil {
		return nil, r, err
	}
	if r.node == nil {
		return nil, r, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	if !r.node.IsDir() {
		return nil, r, NewErrIsNotDirectory(fs.elemsFile(elems))
	}
	return elems, r, nil
}

// dirInfoSnapshot returns FileInfo values for the direct children of dirPath
// that match patterns. It acquires the read lock only for the duration of the
// snapshot so that callers can invoke callbacks without holding the lock.
func (fs *MemFileSystem) dirInfoSnapshot(dirPath string, patterns []string) ([]*FileInfo, error) {
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	elems, dir, err := fs.resolveDir(dirPath)
	if err != nil {
		return nil, err
	}
	dirResolved := dir.path()
	infos := make([]*FileInfo, 0, len(dir.node.Dir))
	for _, name := range sortedNames(dir.node) {
		matched, err := fsimpl.MatchAnyPattern(name, patterns)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		child := memResolved{node: dir.node.Dir[name], parent: dir.node, dir: dirResolved, name: name}
		file := fs.elemsFile(append(slices.Clone(elems), name))
		infos = append(infos, memNodeInfo(file, name, child.node, fs.linkTarget(child)))
	}
	return infos, nil
}

// ListDirRecursive calls callback for every file (not directory) in
// dirPath and its sub-directories. Pattern matching applies only to
// files; sub-directories are always descended into, also through symbolic
// links to directories unless that would list a directory twice
// on one path. Entries are visited in sorted name order for determinism.
func (fs *MemFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dirPath == "" {
		return ErrEmptyPath
	}

	// Snapshot the whole sub-tree while holding the read lock, then release
	// it before invoking the callback so the callback may read from or write
	// to this file system without deadlocking. See ListDir for details.
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

	elems, dir, err := fs.resolveDir(dirPath)
	if err != nil {
		return nil, err
	}
	var infos []*FileInfo
	visiting := make(map[*memFileNode]bool)
	if err := fs.walkDirInfoRecursive(dir.node, elems, dir.path(), patterns, visiting, &infos); err != nil {
		return nil, err
	}
	return infos, nil
}

// walkDirInfoRecursive appends a FileInfo for every file (not directory) in
// the directory node and its sub-directories to infos, visiting names in
// sorted order. elems are the elements of the path of the directory as listed,
// resolved its elements without symbolic links. Directories in visiting are
// on the path that is walked, a symbolic link to one of them is skipped.
// Must be called with fs.mtx held for reading.
func (fs *MemFileSystem) walkDirInfoRecursive(dir *memFileNode, elems, resolved, patterns []string, visiting map[*memFileNode]bool, infos *[]*FileInfo) error {
	visiting[dir] = true
	defer delete(visiting, dir)

	for _, name := range sortedNames(dir) {
		child := memResolved{node: dir.Dir[name], parent: dir, dir: resolved, name: name}
		childElems := append(slices.Clone(elems), name)
		target, targetResolved := child.node, child.path()
		if child.node.IsSymlink() {
			if t, err := fs.resolveElems(targetResolved, true); err == nil {
				target, targetResolved = t.node, t.path()
			} else {
				target = nil
			}
		}
		if target.IsDir() {
			if visiting[target] {
				continue // A symbolic link to a directory of the walked path
			}
			if err := fs.walkDirInfoRecursive(target, childElems, targetResolved, patterns, visiting, infos); err != nil {
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
		*infos = append(*infos, memNodeInfo(fs.elemsFile(childElems), name, child.node, target))
	}
	return nil
}

// nodeForAttrs returns the node at filePath for reading or writing
// its attributes, following a symbolic link as last element if follow is true.
// Must be called with fs.mtx held and after the closed check.
func (fs *MemFileSystem) nodeForAttrs(filePath string, follow bool) (*memFileNode, error) {
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, follow)
	if err != nil {
		return nil, err
	}
	if r.node == nil {
		return nil, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	return r.node, nil
}

// SetPermissions sets the permissions of the file,
// following symbolic links.
func (fs *MemFileSystem) SetPermissions(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, err := fs.nodeForAttrs(filePath, true)
	if err != nil {
		return err
	}
	node.Permissions = perm
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventChmod)
	return nil
}

// User returns the per-node user string previously set via SetUser, or
// the empty string if none was set. MemFileSystem does not consult any
// OS user database. Symbolic links are followed.
func (fs *MemFileSystem) User(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return "", err
	}
	node, err := fs.nodeForAttrs(filePath, true)
	if err != nil {
		return "", err
	}
	return node.User, nil
}

// SetUser sets the user owning the file. The user is a free-form string
// that is only stored, not interpreted. Symbolic links are followed.
func (fs *MemFileSystem) SetUser(filePath string, user string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, err := fs.nodeForAttrs(filePath, true)
	if err != nil {
		return err
	}
	node.User = user
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventChmod)
	return nil
}

// Group returns the per-node group string previously set via SetGroup,
// or the empty string if none was set. Symbolic links are followed.
func (fs *MemFileSystem) Group(filePath string) (string, error) {
	if filePath == "" {
		return "", ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return "", err
	}
	node, err := fs.nodeForAttrs(filePath, true)
	if err != nil {
		return "", err
	}
	return node.Group, nil
}

// SetGroup sets the group owning the file. The group is a free-form string
// that is only stored, not interpreted. Symbolic links are followed.
func (fs *MemFileSystem) SetGroup(filePath string, group string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, err := fs.nodeForAttrs(filePath, true)
	if err != nil {
		return err
	}
	node.Group = group
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventChmod)
	return nil
}

// ListXAttr returns the sorted names of all extended attributes set on the
// node at filePath. If followSymlinks is true, a symbolic link is resolved
// to its target, else the attributes of the link itself are listed.
func (fs *MemFileSystem) ListXAttr(filePath string, followSymlinks bool) ([]string, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, err := fs.nodeForAttrs(filePath, followSymlinks)
	if err != nil {
		return nil, err
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

// GetXAttr returns a copy of the bytes stored for the named extended attribute.
// If the attribute is not set, an error is returned.
// If followSymlinks is true, a symbolic link is resolved to its target.
func (fs *MemFileSystem) GetXAttr(filePath string, name string, followSymlinks bool) ([]byte, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	node, err := fs.nodeForAttrs(filePath, followSymlinks)
	if err != nil {
		return nil, err
	}
	value, ok := node.XAttrs[name]
	if !ok {
		return nil, fmt.Errorf("xattr %q not found on %s", name, fs.fileOf(filePath))
	}
	return bytes.Clone(value), nil
}

// SetXAttr stores a copy of data under the named extended attribute.
// The flags argument honors XAttrCreate (fail if the attribute already
// exists) and XAttrReplace (fail if it does not).
// If followSymlinks is true, a symbolic link is resolved to its target.
func (fs *MemFileSystem) SetXAttr(filePath string, name string, data []byte, flags XAttrFlags, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, err := fs.nodeForAttrs(filePath, followSymlinks)
	if err != nil {
		return err
	}
	_, exists := node.XAttrs[name]
	if flags&XAttrCreate != 0 && exists {
		return fmt.Errorf("xattr %q already exists on %s", name, fs.fileOf(filePath))
	}
	if flags&XAttrReplace != 0 && !exists {
		return fmt.Errorf("xattr %q not found on %s", name, fs.fileOf(filePath))
	}
	if node.XAttrs == nil {
		node.XAttrs = make(map[string][]byte, 1)
	}
	node.XAttrs[name] = bytes.Clone(data)
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventChmod)
	return nil
}

// RemoveXAttr removes the named extended attribute from the node. It
// returns an error if the attribute is not set.
// If followSymlinks is true, a symbolic link is resolved to its target.
func (fs *MemFileSystem) RemoveXAttr(filePath string, name string, followSymlinks bool) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, err := fs.nodeForAttrs(filePath, followSymlinks)
	if err != nil {
		return err
	}
	if _, exists := node.XAttrs[name]; !exists {
		return fmt.Errorf("xattr %q not found on %s", name, fs.fileOf(filePath))
	}
	delete(node.XAttrs, name)
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventChmod)
	return nil
}

// Touch creates an empty file if it does not exist,
// else it updates the modification time to the current time.
// Symbolic links are followed.
func (fs *MemFileSystem) Touch(filePath string, perm Permissions) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, true)
	if err != nil {
		return err
	}
	if r.node != nil {
		r.node.Modified = time.Now()
		fs.emitEvent(filePath, EventChmod)
		return nil
	}
	if err := fs.checkParent(r, elems); err != nil {
		return err
	}
	r.parent.Dir[r.name] = newMemFileNode(MemFile{FileName: r.name}, time.Now(), perm)
	fs.emitEvent(filePath, EventCreate)
	return nil
}

// ReadAll returns a copy of the file's content
// that the caller may modify. Symbolic links are followed.
func (fs *MemFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, err := fs.nodeForRead(filePath)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(node.FileData), nil
}

// nodeForRead returns the node of the file at filePath for reading its
// content, following symbolic links. Must be called with fs.mtx held.
func (fs *MemFileSystem) nodeForRead(filePath string) (*memFileNode, error) {
	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, true)
	if err != nil {
		return nil, err
	}
	if r.node == nil {
		return nil, NewErrDoesNotExist(fs.elemsFile(elems))
	}
	if r.node.IsDir() {
		return nil, NewErrIsDirectory(fs.elemsFile(elems))
	}
	return r.node, nil
}

// WriteAll writes a copy of data to the file, creating it if it does not exist
// and replacing its content if it does. A directory is an ErrIsDirectory.
// Symbolic links are followed.
func (fs *MemFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, created, err := fs.fileForWrite(filePath, perm, true)
	if err != nil {
		return err
	}
	node.FileData = bytes.Clone(data)
	node.Modified = time.Now()
	fs.emitEvent(filePath, writeEvent(created))
	return nil
}

// Append appends data to the file, creating it if it does not exist.
// A directory is an ErrIsDirectory. Symbolic links are followed.
func (fs *MemFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, created, err := fs.fileForWrite(filePath, perm, true)
	if err != nil {
		return err
	}
	// Appends behind the bytes that readers see, see memFileNode
	node.FileData = append(node.FileData, data...)
	node.Modified = time.Now()
	fs.emitEvent(filePath, writeEvent(created))
	return nil
}

// OpenReader opens the file for reading, following symbolic links.
// The returned reader reads a snapshot of the content at the time
// the file was opened, later changes of the file don't affect it.
func (fs *MemFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.RLock()
	defer fs.mtx.RUnlock()

	node, err := fs.nodeForRead(filePath)
	if err != nil {
		return nil, err
	}
	elems := fs.elems(filePath)
	info := memNodeInfo(fs.elemsFile(elems), lastElem(elems), node, nil)
	// Limit the capacity, so the snapshot can't be extended
	// into bytes that a later append stores behind it
	snapshot := node.FileData[:len(node.FileData):len(node.FileData)]
	return fsimpl.NewReadonlyFileBuffer(snapshot, info.StdFileInfo()), nil
}

// OpenWriter opens the file for writing, creating it if it does not exist
// and truncating it if it does. A directory is an ErrIsDirectory.
// Symbolic links are followed. Every Write is visible to the readers
// that are opened after it, readers opened before keep their snapshot.
func (fs *MemFileSystem) OpenWriter(filePath string, perm Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return nil, err
	}
	node, created, err := fs.fileForWrite(filePath, perm, true)
	if err != nil {
		return nil, err
	}
	node.FileData = nil
	node.Modified = time.Now()
	return &memFileWriter{fs: fs, node: node, path: filePath, closeEvent: writeEvent(created)}, nil
}

// OpenAppendWriter opens the file for appending, creating it if it does not exist.
// A directory is an ErrIsDirectory. Symbolic links are followed.
// Every Write is visible to the readers that are opened after it,
// readers opened before keep their snapshot.
func (fs *MemFileSystem) OpenAppendWriter(filePath string, perm Permissions) (WriteCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return nil, err
	}
	node, created, err := fs.fileForWrite(filePath, perm, true)
	if err != nil {
		return nil, err
	}
	return &memFileWriter{fs: fs, node: node, path: filePath, closeEvent: writeEvent(created)}, nil
}

// OpenReadWriter opens the file for reading and writing at any offset,
// creating it if it does not exist without truncating existing content.
// A directory is an ErrIsDirectory. Symbolic links are followed.
//
// The returned ReadWriteSeekCloser works on a copy of the content,
// its writes are stored in the file system when it is closed.
func (fs *MemFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return nil, err
	}
	node, created, err := fs.fileForWrite(filePath, perm, true)
	if err != nil {
		return nil, err
	}
	return &memFileReadWriter{
		fs:      fs,
		node:    node,
		buf:     fsimpl.NewFileBuffer(bytes.Clone(node.FileData)),
		path:    filePath,
		created: created,
	}, nil
}

// Watch registers onEvent for changes to filePath. Events are synthesized
// from every mutation that goes through the MemFileSystem API
// (Touch, WriteAll, OpenWriter close, Append, Truncate, Rename, Move,
// Remove, MakeDir, SetPermissions, SetUser, SetGroup, SetXAttr,
// RemoveXAttr, CreateSymbolicLink), for the path that was passed
// to the mutating method. Direct mutation of a MemFile.FileData
// byte slice obtained outside the API is not observable and emits no event.
//
// When filePath is a directory the callback fires for events on its
// direct children (not deeper sub-directories), matching the
// WatchFileSystem contract.
//
// The callbacks of one watch are called serially, in the order of the
// changes, on a goroutine of the file system, so a slow callback delays
// the later events of its watch but never blocks the mutation that
// produced an event or the callbacks of other watches.
// Panics inside the callback are recovered and silently discarded.
// The events that are not delivered yet are dropped when the watch
// is canceled or the file system is closed.
func (fs *MemFileSystem) Watch(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if onEvent == nil {
		return nil, errors.New("nil callback")
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.closedErr(); err != nil {
		return nil, err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, true)
	if err != nil {
		return nil, err
	}
	if r.node == nil {
		return nil, NewErrDoesNotExist(fs.elemsFile(elems))
	}

	key := fs.elemsPath(elems)
	w := &memWatch{callback: onEvent, isDir: r.node.IsDir()}
	fs.lastWatchID++
	id := fs.lastWatchID
	if fs.watches == nil {
		fs.watches = make(map[string]map[uint64]*memWatch, 1)
	}
	subs := fs.watches[key]
	if subs == nil {
		subs = make(map[uint64]*memWatch, 1)
		fs.watches[key] = subs
	}
	subs[id] = w

	cancel = func() error {
		fs.mtx.Lock()
		defer fs.mtx.Unlock()

		w.cancel()
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

// emitEvent queues an event for filePath with every watch that targets
// filePath itself, or whose target is the parent directory of filePath
// (so callers watching a directory see events for direct children).
// Queueing never blocks, every watch delivers its events on its own goroutine.
// Must be called with fs.mtx held for writing, so that the events
// of a watch are queued in the order of the changes.
func (fs *MemFileSystem) emitEvent(filePath string, event Event) {
	if len(fs.watches) == 0 {
		return
	}
	elems := fs.elems(filePath)
	file := fs.elemsFile(elems)
	for _, w := range fs.watches[fs.elemsPath(elems)] {
		w.enqueue(file, event)
	}
	// The root has no parent: a directory watch on the root
	// must not receive its own events twice
	if len(elems) == 0 {
		return
	}
	for _, w := range fs.watches[fs.elemsPath(parentElems(elems))] {
		if w.isDir {
			w.enqueue(file, event)
		}
	}
}

// memWatch is a single Watch subscription of a MemFileSystem.
// Its events are queued and delivered in order by at most one
// goroutine at a time, which exits when the queue is empty.
type memWatch struct {
	callback func(File, Event)
	isDir    bool

	mtx      sync.Mutex
	queue    []memEvent
	running  bool // a goroutine delivers the queue
	canceled bool
}

// memEvent is an event queued for a memWatch.
type memEvent struct {
	file  File
	event Event
}

// enqueue queues an event and starts a goroutine
// that delivers the queue if none is running.
func (w *memWatch) enqueue(file File, event Event) {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	if w.canceled {
		return
	}
	w.queue = append(w.queue, memEvent{file: file, event: event})
	if !w.running {
		w.running = true
		go w.deliver()
	}
}

// deliver calls the callback for the queued events in order
// until the queue is empty or the watch is canceled.
func (w *memWatch) deliver() {
	for {
		w.mtx.Lock()
		if w.canceled || len(w.queue) == 0 {
			w.queue = nil
			w.running = false
			w.mtx.Unlock()
			return
		}
		e := w.queue[0]
		w.queue = w.queue[1:]
		w.mtx.Unlock()

		w.call(e)
	}
}

// call calls the callback and discards a panic of it.
func (w *memWatch) call(e memEvent) {
	defer func() { _ = recover() }()
	w.callback(e.file, e.event)
}

// cancel drops the queued events and stops the delivery.
func (w *memWatch) cancel() {
	w.mtx.Lock()
	defer w.mtx.Unlock()

	w.canceled = true
	w.queue = nil
}

// Truncate changes the size of the file,
// padding with zero bytes when growing it.
// A directory is an ErrIsDirectory. Symbolic links are followed.
func (fs *MemFileSystem) Truncate(ctx context.Context, filePath string, newSize int64) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if newSize < 0 {
		return fmt.Errorf("%w: negative file size %d", os.ErrInvalid, newSize)
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	node, _, err := fs.fileForWrite(filePath, 0, false)
	if err != nil {
		return err
	}
	currentSize := int64(len(node.FileData))
	switch {
	case newSize == currentSize:
		return nil
	case newSize < currentSize:
		// Limit the capacity, so that a later append doesn't
		// overwrite the truncated bytes that readers still see
		node.FileData = node.FileData[:newSize:newSize]
	default:
		grown := make([]byte, newSize)
		copy(grown, node.FileData)
		node.FileData = grown
	}
	node.Modified = time.Now()
	fs.emitEvent(filePath, EventWrite)
	return nil
}

// CopyFile copies a file within the file system, following symbolic links.
// destFile is the final path of the copy: an existing file is replaced,
// an existing directory is an ErrIsDirectory, copying a file onto itself
// is a no-op. The copy gets a copy of the content, a new file also
// the permissions of the source, a replaced file keeps its permissions
// like with LocalFileSystem.CopyFile.
func (fs *MemFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if srcFile == "" || destFile == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	src, err := fs.nodeForRead(srcFile)
	if err != nil {
		return err
	}
	destElems := fs.elems(destFile)
	dest, err := fs.resolveElems(destElems, true)
	if err != nil {
		return err
	}
	switch {
	case dest.node == src:
		return nil // Copying a file onto itself is a no-op
	case dest.node.IsDir():
		return NewErrIsDirectory(fs.elemsFile(destElems))
	case dest.node != nil:
		dest.node.FileData = bytes.Clone(src.FileData)
		dest.node.Modified = time.Now()
		fs.emitEvent(destFile, EventWrite)
		return nil
	}
	if err := fs.checkParent(dest, destElems); err != nil {
		return err
	}
	dest.parent.Dir[dest.name] = newMemFileNode(
		MemFile{FileName: dest.name, FileData: bytes.Clone(src.FileData)},
		time.Now(),
		src.Permissions,
	)
	fs.emitEvent(destFile, EventCreate|EventWrite)
	return nil
}

// Move moves the file, directory or symbolic link (not its target)
// at filePath to destPath, which is always the final path of the moved node,
// never a directory to move into (File.MoveTo resolves that before calling Move).
//
// Like os.Rename, an existing file or symbolic link at destPath is replaced,
// which is an ErrIsNotDirectory if filePath is a directory.
// An existing directory at destPath is an ErrAlreadyExists, wrapping os.ErrExist,
// and it is never merged or deleted. Moving a directory (including the root)
// into itself or one of its descendants is an error wrapping os.ErrInvalid,
// also if destPath reaches the descendant through a symbolic link.
//
// When filePath and destPath are the same file after cleaning,
// Move returns nil without touching the node, matching the no-op behavior
// of os.Rename required by the MoveFileSystem contract.
func (fs *MemFileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	if filePath == "" || destPath == "" {
		return ErrEmptyPath
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	srcElems, destElems := fs.elems(filePath), fs.elems(destPath)
	src, err := fs.resolveElems(srcElems, false)
	if err != nil {
		return err
	}
	if src.node == nil {
		return NewErrDoesNotExist(fs.elemsFile(srcElems))
	}
	if slices.Equal(srcElems, destElems) {
		return nil
	}
	intoItself := fmt.Errorf("%w: can't move %s into itself at %s", os.ErrInvalid, fs.elemsFile(srcElems), fs.elemsFile(destElems))
	if src.node.IsDir() && hasElemsPrefix(destElems, srcElems) {
		return intoItself
	}
	dest, err := fs.resolveElems(destElems, false)
	if err != nil {
		return err
	}
	switch {
	case dest.node == src.node:
		return nil // The same node reached through symbolic links
	case src.node.IsDir() && dest.parent.IsDir() && hasElemsPrefix(dest.dir, src.path()):
		return intoItself // Through a symbolic link into a descendant
	case dest.node.IsDir():
		return NewErrAlreadyExists(fs.elemsFile(destElems))
	case dest.node != nil && src.node.IsDir():
		return NewErrIsNotDirectory(fs.elemsFile(destElems))
	case dest.node == nil:
		if err := fs.checkParent(dest, destElems); err != nil {
			return err
		}
	}

	delete(src.parent.Dir, src.name)
	src.node.FileName = dest.name
	src.node.Modified = time.Now()
	dest.parent.Dir[dest.name] = src.node
	fs.emitEvent(filePath, EventRename)
	fs.emitEvent(destPath, EventCreate)
	return nil
}

// Remove removes a file, a symbolic link (not its target) or an empty
// directory. A non-empty directory is an ErrDirectoryNotEmpty,
// use File.RemoveRecursive for a directory with content.
func (fs *MemFileSystem) Remove(filePath string) error {
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	elems := fs.elems(filePath)
	r, err := fs.resolveElems(elems, false)
	if err != nil {
		return err
	}
	switch {
	case r.node == nil:
		return NewErrDoesNotExist(fs.elemsFile(elems))
	case r.node == &fs.root:
		return fmt.Errorf("%w: can't remove the root directory %s", os.ErrInvalid, fs.RootDir())
	case r.node.IsDir() && len(r.node.Dir) > 0:
		return NewErrDirectoryNotEmpty(fs.elemsFile(elems))
	}
	delete(r.parent.Dir, r.name)
	fs.emitEvent(filePath, EventRemove)
	return nil
}

// RemoveAll removes filePath and any children it contains.
// A symbolic link is removed, not its target.
// No error is returned if the path does not exist.
func (fs *MemFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if err := fs.writableErr(); err != nil {
		return err
	}
	r, err := fs.resolve(filePath, false)
	if err != nil {
		return err
	}
	switch {
	case r.node == nil:
		return nil
	case r.node == &fs.root:
		return fmt.Errorf("%w: can't remove the root directory %s", os.ErrInvalid, fs.RootDir())
	}
	delete(r.parent.Dir, r.name)
	fs.emitEvent(filePath, EventRemove)
	return nil
}

// Close unregisters the file system and discards all its files.
// Every following operation returns an error wrapping ErrFileSystemClosed,
// and the events of watches that are not delivered yet are dropped.
// Readers opened before Close can still read their snapshot.
// Closing an already closed file system is a no-op.
func (fs *MemFileSystem) Close() error {
	fs.mtx.Lock()
	if fs.closed {
		fs.mtx.Unlock()
		return nil
	}
	fs.closed = true
	fs.root.Dir = nil
	for _, subs := range fs.watches {
		for _, w := range subs {
			w.cancel()
		}
	}
	fs.watches = nil
	fs.mtx.Unlock() // Unlock before Unregister, which may call methods of the file system

	Unregister(fs)
	return nil
}

// Clear removes all files and directories
// but keeps the file system usable.
// Clear does nothing after Close.
func (fs *MemFileSystem) Clear() {
	fs.mtx.Lock()
	defer fs.mtx.Unlock()

	if fs.closed {
		return
	}
	clear(fs.root.Dir)
	fs.root.Modified = time.Now()
}

// memFileWriter implements WriteCloser for MemFileSystem.
// Every Write appends to the content of the node.
type memFileWriter struct {
	fs         *MemFileSystem
	node       *memFileNode
	path       string
	closeEvent Event
	closed     bool // protected by fs.mtx
}

func (w *memFileWriter) Write(p []byte) (n int, err error) {
	w.fs.mtx.Lock()
	defer w.fs.mtx.Unlock()

	if w.closed {
		return 0, os.ErrClosed
	}
	if err := w.fs.closedErr(); err != nil {
		return 0, err
	}
	// Appends behind the bytes that readers see, see memFileNode
	w.node.FileData = append(w.node.FileData, p...)
	w.node.Modified = time.Now()
	return len(p), nil
}

func (w *memFileWriter) Close() error {
	w.fs.mtx.Lock()
	defer w.fs.mtx.Unlock()

	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.fs.closedErr(); err != nil {
		return err
	}
	w.fs.emitEvent(w.path, w.closeEvent)
	return nil
}

// memFileReadWriter implements ReadWriteSeekCloser for MemFileSystem
// on a copy of the content of the node, that is stored in the node
// by Close if anything was written.
type memFileReadWriter struct {
	fs      *MemFileSystem
	node    *memFileNode
	path    string
	created bool // the file was created by OpenReadWriter

	mtx     sync.Mutex // protects the fields below
	buf     *fsimpl.FileBuffer
	written bool
	closed  bool
}

func (rw *memFileReadWriter) Read(p []byte) (n int, err error) {
	rw.mtx.Lock()
	defer rw.mtx.Unlock()

	return rw.buf.Read(p)
}

func (rw *memFileReadWriter) ReadAt(p []byte, off int64) (n int, err error) {
	rw.mtx.Lock()
	defer rw.mtx.Unlock()

	return rw.buf.ReadAt(p, off)
}

func (rw *memFileReadWriter) Write(p []byte) (n int, err error) {
	rw.mtx.Lock()
	defer rw.mtx.Unlock()

	n, err = rw.buf.Write(p)
	rw.written = rw.written || n > 0
	return n, err
}

func (rw *memFileReadWriter) WriteAt(p []byte, off int64) (n int, err error) {
	rw.mtx.Lock()
	defer rw.mtx.Unlock()

	n, err = rw.buf.WriteAt(p, off)
	rw.written = rw.written || n > 0
	return n, err
}

func (rw *memFileReadWriter) Seek(offset int64, whence int) (int64, error) {
	rw.mtx.Lock()
	defer rw.mtx.Unlock()

	return rw.buf.Seek(offset, whence)
}

func (rw *memFileReadWriter) Close() error {
	rw.mtx.Lock()
	if rw.closed {
		rw.mtx.Unlock()
		return nil
	}
	rw.closed = true
	data, written := rw.buf.Bytes(), rw.written
	err := rw.buf.Close()
	rw.mtx.Unlock()

	rw.fs.mtx.Lock()
	defer rw.fs.mtx.Unlock()

	if closedErr := rw.fs.closedErr(); closedErr != nil {
		return errors.Join(err, closedErr)
	}
	switch {
	case written:
		// The buffer is not used anymore, so the node can own it
		rw.node.FileData = data
		rw.node.Modified = time.Now()
		rw.fs.emitEvent(rw.path, writeEvent(rw.created))
	case rw.created:
		rw.fs.emitEvent(rw.path, EventCreate)
	}
	return err
}
