package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// emulations otherwise.
//
// Paths can't escape the directory: ".." elements are cleaned away
// at the root of the view, and a name that the parent would split
// into several elements, like `..\x` for a parent with the separator `\`,
// is an error wrapping os.ErrInvalid. So is, for a view of [Local],
// a name that [filepath.Localize] rejects, like a name with a NUL byte,
// and on Windows a name with a colon or a reserved device name like "NUL".
// Symbolic links that already exist in the parent are followed
// by the parent wherever they point to.
//
// The parent must stay registered while the view is used,
// because emulated operations go through the File API.
type SubFileSystem struct {
	paths fsimpl.PathHelper

	parent FileSystem
	dir    string // clean parent path of the root directory
	id     string
	closed atomic.Bool
}

// Prefix returns the URI prefix of the file system.
func (s *SubFileSystem) Prefix() string {
	return s.paths.URIPrefix
}

// Separator returns "/".
func (s *SubFileSystem) Separator() string {
	return s.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (s *SubFileSystem) CleanPath(uriParts ...string) string {
	return s.paths.CleanPath(uriParts...)
}

// NewSubFileSystem returns a file system with the URI prefix "sub://" + id
// whose root is the directory dirPath of the parent file system.
// A random id is used if id is empty. dirPath is a parent path or URI
// and must be an existing directory. The file system is not registered.
// A URI is resolved like by [File], decoding its percent-encoded
// characters, and must be one of the parent: the URI of another file
// system, or a file URI that names no local file, like "file://./x", is
// an error wrapping [os.ErrInvalid] instead of a root somewhere else.
// An empty dirPath is [ErrEmptyPath]; "." is the current directory.
// A relative local root is made absolute, so it doesn't follow later
// changes of the working directory: on Unix with the working directory
// without symbolic links, from which the system resolves "..", on Windows
// like [filepath.Abs], from the path of the current directory.
func NewSubFileSystem(parent FileSystem, dirPath string, id string) (*SubFileSystem, error) {
	if parent == nil {
		return nil, fmt.Errorf("nil parent file system")
	}
	var dir string
	_, local := parent.(*LocalFileSystem)
	switch {
	case dirPath == "":
		// Not the working directory or the root of the parent
		return nil, ErrEmptyPath
	case !hasURIScheme(dirPath):
		// Also a path with a "://" after a "/", like "/cache/https://x"
		dir = parent.CleanPath(dirPath)
	case local && strings.HasPrefix(dirPath, LocalPrefix):
		// Decoded once, uriPath would decode again in CleanPath
		resolved, resolvedPath := parseLocalFileURI(parent, dirPath)
		if resolved != parent || resolvedPath == "" {
			return nil, fmt.Errorf("%w: the file URI %s names no local directory", os.ErrInvalid, fsimpl.RedactURI(dirPath))
		}
		dir = resolvedPath
	case fsPrefixMatchLen(parent, dirPath) >= 0:
		dir = uriPath(parent, dirPath)
	default:
		return nil, fmt.Errorf("%w: %s is no URI of the parent file system %s", os.ErrInvalid, fsimpl.RedactURI(dirPath), fsimpl.RedactURI(parent.Prefix()))
	}
	// A relative root would follow the working directory, cut the dot
	// of a name like ".env" from a parent path below the root ".",
	// and turn the absolute target of a link into a relative one,
	// which the link's directory resolves outside of a root ".."
	switch {
	case local && windowsPaths && !isLongPath(dir):
		// Windows resolves ".." from the path of the current directory,
		// like filepath.Abs, which also gives `\x` and "/" without
		// a drive, for which filepath.IsAbs is false, the current drive.
		// A long path is absolute and keeps the trailing dots and spaces
		// of its names, which filepath.Abs would remove
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		dir = absDir
	case local && !filepath.IsAbs(dir):
		// Unix resolves ".." from the real working directory, not from
		// a path through a symbolic link to it that os.Getwd can return
		// from $PWD
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		if cwd, err = filepath.EvalSymlinks(cwd); err != nil {
			return nil, err
		}
		dir = filepath.Join(cwd, dir)
	}
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
		paths:  fsimpl.PathHelper{URIPrefix: SubFileSystemPrefix + id, Rooted: true},
		parent: parent,
		dir:    dir,
		id:     id,
	}, nil
}

// NewSubFileSystemAndRegister registers a file system rooted at dirPath
// of the parent with the URI prefix "sub://" + id, see NewSubFileSystem.
// The id must not be empty.
func NewSubFileSystemAndRegister(parent FileSystem, dirPath string, id string) error {
	if id == "" {
		return errors.New("NewSubFileSystemAndRegister needs an id")
	}
	subFS, err := NewSubFileSystem(parent, dirPath, id)
	if err != nil {
		return err
	}
	return Register(subFS)
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

// parentPath translates a path of this file system to the parent
// element by element. The view cleans its paths with the separator "/",
// so a name like `..\secret.txt` is a single element of the view.
// A parent with the separator `\` would split that name into the
// elements ".." and "secret.txt" and resolve the path outside of the
// root directory, so a name that the parent can't represent as one
// element, see validName, is an error wrapping os.ErrInvalid.
func (s *SubFileSystem) parentPath(filePath string) (string, error) {
	names := s.paths.SplitPath(s.CleanPath(filePath))
	for _, name := range names {
		if !s.validName(name) {
			return "", s.errInvalidName(filePath, name)
		}
	}
	return s.parent.CleanPath(append([]string{s.dir}, names...)...), nil
}

// validName reports if the parent can represent name as one element
// of a path: the name must not contain the separator of the parent.
// For a LocalFileSystem parent the name must also pass filepath.Localize,
// which rejects the names that os.DirFS rejects: on Windows names with
// a colon, which Windows takes for an alternate data stream, or a reserved
// device name like "NUL" or "COM1", which opens the device instead of
// a file in the directory, and on every platform names with a NUL byte.
func (s *SubFileSystem) validName(name string) bool {
	if !isValidName(s.parent, name) {
		return false
	}
	if _, isLocal := s.parent.(*LocalFileSystem); isLocal {
		if _, err := filepath.Localize(name); err != nil {
			return false
		}
	}
	return true
}

// errInvalidName returns an error wrapping os.ErrInvalid
// for a name in filePath that the parent can't represent.
func (s *SubFileSystem) errInvalidName(filePath, name string) error {
	if sep := s.parent.Separator(); strings.Contains(name, sep) {
		return fmt.Errorf("%w: the name %q in %s contains the separator %q of the parent file system",
			os.ErrInvalid, name, s.paths.JoinCleanURI(filePath), sep)
	}
	return fmt.Errorf("%w: the name %q in %s is not valid for the parent %s",
		os.ErrInvalid, name, s.paths.JoinCleanURI(filePath), s.parent.Name())
}

// linkTarget translates the target of a symbolic link at linkPath
// to the parent. An absolute target is a path of the view and translated
// like every path. A relative target without ".." elements is stored
// verbatim, because the parent resolves it relative to the directory
// of the link just like the view.
//
// A relative target with ".." elements is resolved lexically against
// the directory of the link in the view and stored as the corresponding
// path of the parent. The parent resolves ".." from the real directory
// of the link, which differs from the view's directory if the link path
// goes through other links: with "/d/up" linking to "..", the target
// "../../x" of "/d/up/d/up/link" would point above the root directory.
//
// A relative target that would leave the view, like "../x" for a link
// in the root directory, or that contains a name the parent can't
// represent, see validName, is an error wrapping os.ErrInvalid.
func (s *SubFileSystem) linkTarget(targetPath, linkPath string) (string, error) {
	if strings.HasPrefix(targetPath, "/") {
		return s.parentPath(targetPath)
	}
	linkDir, _ := s.paths.SplitDirAndName(s.CleanPath(linkPath))
	depth := len(s.paths.SplitPath(linkDir))
	hasDotDot := false
	for name := range strings.SplitSeq(targetPath, "/") {
		switch name {
		case "", ".":
			continue
		case "..":
			hasDotDot = true
			depth--
		default:
			if !s.validName(name) {
				return "", s.errInvalidName(targetPath, name)
			}
			depth++
		}
		if depth < 0 {
			return "", fmt.Errorf("%w: the link target %q of %s leaves the root directory of %s",
				os.ErrInvalid, targetPath, s.paths.JoinCleanURI(linkPath), s)
		}
	}
	if hasDotDot {
		return s.parentPath(s.CleanPath(linkDir, targetPath))
	}
	return targetPath, nil
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

// checkPath returns the parent path of filePath,
// or an error if the file system is closed, filePath is empty
// or can't be translated to the parent.
func (s *SubFileSystem) checkPath(filePath string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	if filePath == "" {
		return "", ErrEmptyPath
	}
	return s.parentPath(filePath)
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
	return File(s.paths.URIPrefix + "/")
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
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	info, err := fsStat(s.parent, parentPath)
	if err != nil {
		return nil, s.subErr(err)
	}
	return s.subInfo(info), nil
}

// subFile translates a File of the parent below the root directory.
func (s *SubFileSystem) subFile(parentFile File) File {
	return File(s.paths.JoinCleanURI(s.subPath(uriPath(s.parent, string(parentFile)))))
}

// subErr translates the file of typed parent errors to this file system.
func (s *SubFileSystem) subErr(err error) error {
	return translateErrFile(err, s.subFile)
}

// ListDir calls the callback for every file in the directory that
// matches any of the patterns, or for all files if no patterns are passed.
// The FileInfo passed to the callback has a File of this file system.
func (s *SubFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	parentPath, err := s.checkPath(dirPath)
	if err != nil {
		return err
	}
	err = fsListDir(ctx, s.parent, parentPath, patterns, func(info *FileInfo) error {
		return callback(s.subInfo(info))
	})
	return s.subErr(err)
}

// OpenReader opens the file of the parent file system for reading.
func (s *SubFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	r, err := fsOpenReader(s.parent, parentPath)
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
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	w, err := fsOpenWriter(s.parent, parentPath, perm)
	return w, s.subErr(err)
}

// MakeDir creates a directory in the parent file system.
func (s *SubFileSystem) MakeDir(dirPath string, perm Permissions) error {
	parentPath, err := s.checkPath(dirPath)
	if err != nil {
		return err
	}
	return s.subErr(fsMakeDir(s.parent, parentPath, perm))
}

// Remove removes a file or empty directory from the parent file system.
// The root directory of the view can't be removed.
func (s *SubFileSystem) Remove(filePath string) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	if parentPath == s.dir {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, s)
	}
	return s.subErr(fsRemove(s.parent, parentPath))
}

///////////////////////////////////////////////////////////////////////////////
// Optional interfaces, forwarded via the fs package dispatch
// so the parent's native implementation or the emulation is used

// Exists reports if the file exists in the parent file system.
// A path with a name that the parent can't represent does not exist.
func (s *SubFileSystem) Exists(filePath string) (bool, error) {
	if err := s.checkClosed(); err != nil {
		return false, err
	}
	if filePath == "" {
		return false, nil
	}
	parentPath, err := s.parentPath(filePath)
	if err != nil {
		// A name that the parent can't represent can't exist
		return false, nil
	}
	return fsExists(s.parent, parentPath)
}

// ReadAll reads the complete file from the parent file system.
func (s *SubFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	data, err := fsReadAll(ctx, s.parent, parentPath)
	return data, s.subErr(err)
}

// WriteAll writes data to the file of the parent file system,
// creating it if it does not exist and replacing its content if it does.
func (s *SubFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	return s.subErr(fsWriteAll(ctx, s.parent, parentPath, data, perm))
}

// Append appends data to the file of the parent file system,
// creating it if it does not exist.
func (s *SubFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	return s.subErr(fsAppend(ctx, s.parent, parentPath, data, perm))
}

// OpenAppendWriter opens the file of the parent file system for appending,
// creating it if it does not exist.
func (s *SubFileSystem) OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	w, err := fsOpenAppendWriter(s.parent, parentPath, perm)
	return w, s.subErr(err)
}

// OpenReadWriter opens the file of the parent file system
// for reading and writing at any offset.
func (s *SubFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	rw, err := fsOpenReadWriter(s.parent, parentPath, perm)
	return rw, s.subErr(err)
}

// Truncate changes the size of the file in the parent file system.
func (s *SubFileSystem) Truncate(ctx context.Context, filePath string, size int64) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	return s.subErr(fsTruncate(ctx, s.parent, parentPath, size))
}

// Touch creates the file in the parent file system if it does not exist,
// else it updates its modification time.
func (s *SubFileSystem) Touch(filePath string, perm Permissions) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	return s.subErr(fsTouch(s.parent, parentPath, perm))
}

// MakeAllDirs creates a directory and all missing parent directories
// within the view, never above its root.
func (s *SubFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	parentPath, err := s.checkPath(dirPath)
	if err != nil {
		return err
	}
	// File.MakeAllDirs checked the names
	return s.subErr(fsMakeAllDirs(s.parent, parentPath, perm))
}

// RemoveAll removes the file or directory with all its content
// from the parent file system. The root directory of the view
// can't be removed.
func (s *SubFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	if parentPath == s.dir {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, s)
	}
	return s.subErr(fsRemoveAll(ctx, s.parent, parentPath))
}

// CopyFile copies a file within the view using the parent file system.
// destFile is the final path of the copy: an existing file is replaced,
// an existing directory is an ErrIsDirectory.
func (s *SubFileSystem) CopyFile(ctx context.Context, srcFile string, destFile string) error {
	srcPath, err := s.checkPath(srcFile)
	if err != nil {
		return err
	}
	destPath, err := s.checkPath(destFile)
	if err != nil {
		return err
	}
	src, dest := fsFile(s.parent, srcPath), fsFile(s.parent, destPath)
	return s.subErr(copyFileFinal(ctx, src, dest, nil, nil))
}

// Move moves and/or renames a file within the view
// using the parent file system, see [MoveFileSystem].
// The root directory of the view can't be moved,
// and as an existing directory it can't be replaced.
func (s *SubFileSystem) Move(ctx context.Context, filePath string, destPath string) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	parentDest, err := s.checkPath(destPath)
	if err != nil {
		return err
	}
	switch {
	case parentPath == parentDest:
		return nil
	case parentPath == s.dir:
		return fmt.Errorf("%w: can't move the root directory of %s", os.ErrInvalid, s)
	case parentDest == s.dir:
		return NewErrAlreadyExists(s.RootDir())
	}
	return s.subErr(fsMove(ctx, s.parent, parentPath, parentDest))
}

// ListDirRecursive calls the callback for every file below the directory
// that matches any of the patterns.
func (s *SubFileSystem) ListDirRecursive(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	parentPath, err := s.checkPath(dirPath)
	if err != nil {
		return err
	}
	err = fsListDirRecursive(ctx, s.parent, parentPath, patterns, func(info *FileInfo) error {
		return callback(s.subInfo(info))
	})
	return s.subErr(err)
}

// SetPermissions sets the permissions of the file in the parent file system.
func (s *SubFileSystem) SetPermissions(filePath string, perm Permissions) error {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return err
	}
	if p, ok := s.parent.(PermissionsFileSystem); ok {
		return s.subErr(p.SetPermissions(parentPath, perm))
	}
	return NewErrUnsupported(s, "SetPermissions")
}

// Lstat returns the FileInfo of the file in the parent file system
// without following a symbolic link.
func (s *SubFileSystem) Lstat(filePath string) (*FileInfo, error) {
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	info, err := fsLstat(s.parent, parentPath)
	if err != nil {
		return nil, s.subErr(err)
	}
	return s.subInfo(info), nil
}

// CreateSymbolicLink creates a link at linkPath to targetPath.
// An absolute targetPath is a path of the view and stored as the
// corresponding path of the parent file system. A relative targetPath
// without ".." elements is stored verbatim, the parent resolves it
// relative to the directory of the link. A relative targetPath with ".."
// elements is resolved against the directory of linkPath in the view
// and stored as the corresponding path of the parent, because the parent
// would resolve ".." from the real directory of the link, which a link
// in linkPath can move outside of the view. A relative targetPath that
// leaves the root directory of the view is an error wrapping os.ErrInvalid.
func (s *SubFileSystem) CreateSymbolicLink(targetPath, linkPath string) error {
	parentLink, err := s.checkPath(linkPath)
	if err != nil {
		return err
	}
	if targetPath == "" {
		return ErrEmptyPath
	}
	p, ok := s.parent.(SymbolicLinkFileSystem)
	if !ok {
		return NewErrUnsupported(s, "CreateSymbolicLink")
	}
	parentTarget, err := s.linkTarget(targetPath, linkPath)
	if err != nil {
		return err
	}
	return s.subErr(p.CreateSymbolicLink(parentTarget, parentLink))
}

// ReadSymbolicLink returns the target of the link. An absolute target
// that points below the root directory is translated to a path
// of this file system, other targets are returned as stored.
func (s *SubFileSystem) ReadSymbolicLink(linkPath string) (targetPath string, err error) {
	parentLink, err := s.checkPath(linkPath)
	if err != nil {
		return "", err
	}
	p, ok := s.parent.(SymbolicLinkFileSystem)
	if !ok {
		return "", NewErrUnsupported(s, "ReadSymbolicLink")
	}
	target, err := p.ReadSymbolicLink(parentLink)
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
	parentPath, err := s.checkPath(filePath)
	if err != nil {
		return nil, err
	}
	p, ok := s.parent.(WatchFileSystem)
	if !ok {
		return nil, NewErrUnsupported(s, "Watch")
	}
	cancel, err = p.Watch(parentPath, func(file File, event Event) {
		onEvent(s.subFile(file), event)
	})
	return cancel, s.subErr(err)
}
