package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/ungerik/go-fs/fsimpl"
)

// OverlayFileSystemPrefix is the URI prefix of OverlayFileSystem, followed by the id.
const OverlayFileSystemPrefix = "overlay://"

var (
	_ FileSystem             = new(OverlayFileSystem)
	_ WriteFileSystem        = new(OverlayFileSystem)
	_ ReadAllFileSystem      = new(OverlayFileSystem)
	_ WriteAllFileSystem     = new(OverlayFileSystem)
	_ AppendFileSystem       = new(OverlayFileSystem)
	_ AppendWriterFileSystem = new(OverlayFileSystem)
	_ ReadWriterFileSystem   = new(OverlayFileSystem)
	_ TruncateFileSystem     = new(OverlayFileSystem)
	_ TouchFileSystem        = new(OverlayFileSystem)
	_ MakeAllDirsFileSystem  = new(OverlayFileSystem)
	_ RemoveAllFileSystem    = new(OverlayFileSystem)
)

// OverlayFileSystem stacks a writable upper file system on a read-only
// base file system. Reads look in the upper layer first and fall back to
// the base, listings are the union of both layers with the upper layer
// shadowing base entries of the same name, and every write goes to the
// upper layer. A base file that is modified in place (append, random
// access, truncate, touch) is copied up to the upper layer first, with
// the permissions that CopyFile would give the copy.
// A base directory can't be replaced by a file and a base file
// can't be used as directory.
//
// Removing a base entry records a whiteout that hides it and everything
// below it. The whiteout stays when the path is created again in the upper
// layer: a directory created at the path of a removed base directory is
// opaque, it starts empty instead of bringing back the removed base entries.
// Whiteouts are kept in memory: a new overlay over the same layers starts
// without them. They are recorded for the exact overlay path, so both layers
// must be case-sensitive, or every path must always be used with the same
// case: over a case-insensitive base a removed file stays readable through
// a path with another case, like "/README.md" after removing "/readme.md".
//
// Both layers are used with the overlay paths, so the roots of the two
// layers map to the root of the overlay. Wrap a layer in a SubFileSystem
// to overlay a sub directory. The overlay has no volumes: the path "/dir"
// of the overlay is `C:\dir` of a layer with the volume "C:". A name that
// a layer would split into several elements, like `..\x` for a layer with
// the separator `\`, is an error wrapping os.ErrInvalid. The layers must stay
// registered while the overlay is used, because emulated operations go
// through the File API. Concurrent writes to the same path through the
// overlay are serialized, changing the layers directly while the overlay
// is used is not supported.
//
// The overlay has no symbolic links of its own, but it shows the links of its
// layers like their files. A recursive listing doesn't follow them, see
// ListDirRecursiveFileSystem. The overlay can't create links and another file
// system never gets a copy of a link to a directory, so CopyRecursive and
// Move of a link to a directory of a layer fail, see CopyRecursive.
// Copies and moves compare the files of local layers as local files, which
// can resolve a link to another path than the overlay: a copy of a file onto
// itself through a link or a hard link of a layer is a no-op, a move of
// a link onto the file it points to, a move onto another hard link and
// a rename to another case of the name are errors wrapping os.ErrInvalid,
// see CopyFile and Move.
type OverlayFileSystem struct {
	paths fsimpl.PathHelper

	base   FileSystem
	upper  WriteFileSystem
	id     string
	closed bool

	mtx       sync.RWMutex
	whiteouts map[string]struct{} // overlay paths of removed base entries

	pathLocks pathLocks // serialize the writes and copy ups of a path
}

// Prefix returns the URI prefix of the file system.
func (o *OverlayFileSystem) Prefix() string {
	return o.paths.URIPrefix
}

// Separator returns "/".
func (o *OverlayFileSystem) Separator() string {
	return o.paths.Separator()
}

// CleanPath joins the uriParts with the separator and returns
// the cleaned absolute path of the file system without the prefix.
func (o *OverlayFileSystem) CleanPath(uriParts ...string) string {
	return o.paths.CleanPath(uriParts...)
}

// NewOverlayFileSystem returns a file system with the URI prefix
// "overlay://" + id that reads from upper and base and writes to upper.
// A random id is used if id is empty. The file system is not registered.
// Base and upper must be case-sensitive, or every path must be used with
// the same case, because removed base entries are hidden by exact path,
// see OverlayFileSystem.
func NewOverlayFileSystem(base FileSystem, upper WriteFileSystem, id string) (*OverlayFileSystem, error) {
	if base == nil || upper == nil {
		return nil, errors.New("nil base or upper file system")
	}
	if _, writable := upper.ReadableWritable(); !writable {
		return nil, fmt.Errorf("upper file system %s is not writable", upper)
	}
	if id == "" {
		id = fsimpl.RandomString()
	}
	return &OverlayFileSystem{
		paths:     fsimpl.PathHelper{URIPrefix: OverlayFileSystemPrefix + id, Rooted: true},
		base:      base,
		upper:     upper,
		id:        id,
		whiteouts: make(map[string]struct{}),
	}, nil
}

// NewOverlayFileSystemAndRegister registers an overlay file system
// with the URI prefix "overlay://" + id, see NewOverlayFileSystem.
// The id must not be empty.
func NewOverlayFileSystemAndRegister(base FileSystem, upper WriteFileSystem, id string) error {
	if id == "" {
		return errors.New("NewOverlayFileSystemAndRegister needs an id")
	}
	overlay, err := NewOverlayFileSystem(base, upper, id)
	if err != nil {
		return err
	}
	return Register(overlay)
}

// Base returns the read-only base layer.
func (o *OverlayFileSystem) Base() FileSystem {
	return o.base
}

// Upper returns the writable upper layer.
func (o *OverlayFileSystem) Upper() WriteFileSystem {
	return o.upper
}

// ReadableWritable returns true for readable and true for writable,
// because the upper layer is always writable.
func (o *OverlayFileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

// RootDir returns the root directory of the overlay.
func (o *OverlayFileSystem) RootDir() File {
	return File(o.paths.URIPrefix + "/")
}

// ID returns the id of the file system, which is part of its URI prefix.
func (o *OverlayFileSystem) ID() string {
	return o.id
}

// Name returns "overlay file system".
func (o *OverlayFileSystem) Name() string {
	return "overlay file system"
}

// String returns a descriptive string of the overlay
// including its prefix and both layers.
func (o *OverlayFileSystem) String() string {
	return fmt.Sprintf("%s with prefix %s of %s over %s", o.Name(), o.paths.URIPrefix, o.upper, o.base)
}

///////////////////////////////////////////////////////////////////////////////
// Paths, whiteouts and translation

// basePath translates an overlay path to the base layer.
func (o *OverlayFileSystem) basePath(filePath string) string {
	return o.base.CleanPath(filePath)
}

// upperPath translates an overlay path to the upper layer.
func (o *OverlayFileSystem) upperPath(filePath string) string {
	return o.upper.CleanPath(filePath)
}

// overlayPath translates a path of a layer to the overlay.
// The overlay has no volumes, the layer path `C:\dir` is "/dir".
func (o *OverlayFileSystem) overlayPath(layer FileSystem, layerPath string) string {
	layerPath = layerPath[fsVolumeLen(layer, layerPath):]
	if sep := layer.Separator(); sep != "/" {
		layerPath = strings.ReplaceAll(layerPath, sep, "/")
	}
	return o.CleanPath(layerPath)
}

// overlayFile translates a File of a layer to the overlay.
func (o *OverlayFileSystem) overlayFile(layer FileSystem, layerFile File) File {
	return File(o.paths.JoinCleanURI(o.overlayPath(layer, uriPath(layer, string(layerFile)))))
}

// overlayInfo translates a FileInfo of a layer to the overlay.
func (o *OverlayFileSystem) overlayInfo(layer FileSystem, info *FileInfo) *FileInfo {
	overlayInfo := *info
	overlayInfo.File = o.overlayFile(layer, info.File)
	return &overlayInfo
}

// overlayErr translates the file of typed layer errors to the overlay.
func (o *OverlayFileSystem) overlayErr(layer FileSystem, err error) error {
	return translateErrFile(err, func(f File) File { return o.overlayFile(layer, f) })
}

// hidden reports whether the base entry at the overlay path
// or one of its ancestors was removed.
func (o *OverlayFileSystem) hidden(filePath string) bool {
	o.mtx.RLock()
	defer o.mtx.RUnlock()
	if len(o.whiteouts) == 0 {
		return false
	}
	p := o.CleanPath(filePath)
	for {
		if _, ok := o.whiteouts[p]; ok {
			return true
		}
		if p == "/" {
			return false
		}
		p = o.CleanPath(p, "..")
	}
}

// hide records a whiteout for the overlay path.
func (o *OverlayFileSystem) hide(filePath string) {
	o.mtx.Lock()
	defer o.mtx.Unlock()
	o.whiteouts[o.CleanPath(filePath)] = struct{}{}
}

func (o *OverlayFileSystem) checkClosed() error {
	o.mtx.RLock()
	defer o.mtx.RUnlock()
	if o.closed {
		return ErrFileSystemClosed
	}
	return nil
}

// checkPath returns an error if the overlay is closed, filePath is empty,
// or filePath has a name that a layer would split into several elements.
// The overlay cleans its paths with the separator "/", so `..\x` is a single
// name of the overlay, but a layer with the separator `\` would resolve
// "/d/..\x" to "/x" and bypass the whiteout of a removed "/x".
func (o *OverlayFileSystem) checkPath(filePath string) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	for _, name := range o.paths.SplitPath(o.CleanPath(filePath)) {
		for _, layer := range []FileSystem{o.base, o.upper} {
			if !isValidName(layer, name) {
				return fmt.Errorf("%w: the name %q in %s contains the separator %q of the layer %s",
					os.ErrInvalid, name, o.paths.JoinCleanURI(filePath), layer.Separator(), layer)
			}
		}
	}
	return nil
}

// lockPath locks the overlay path for a write and returns the unlock function.
func (o *OverlayFileSystem) lockPath(filePath string) (unlock func()) {
	return o.pathLocks.lock(o.CleanPath(filePath))
}

// pathLocks is a set of mutexes by path.
// The zero value is ready to use.
type pathLocks struct {
	mtx   sync.Mutex
	locks map[string]*pathLock
}

// pathLock is the mutex of a path,
// shared by the goroutines that lock or wait for it.
type pathLock struct {
	sync.Mutex
	refs int // guarded by pathLocks.mtx
}

// lock locks the mutex of p and returns the unlock function.
func (l *pathLocks) lock(p string) (unlock func()) {
	l.mtx.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*pathLock)
	}
	pl := l.locks[p]
	if pl == nil {
		pl = new(pathLock)
		l.locks[p] = pl
	}
	pl.refs++
	l.mtx.Unlock()

	pl.Lock()
	return func() {
		pl.Unlock()
		l.mtx.Lock()
		pl.refs--
		if pl.refs == 0 {
			delete(l.locks, p)
		}
		l.mtx.Unlock()
	}
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// statLayer returns the FileInfo of the upper layer, or of the base layer
// if the upper layer has no entry and the base entry is not hidden,
// together with the layer that answered and the path within that layer.
func (o *OverlayFileSystem) statLayer(filePath string) (info *FileInfo, layer FileSystem, layerPath string, err error) {
	if err := o.checkPath(filePath); err != nil {
		return nil, nil, "", err
	}
	layerPath = o.upperPath(filePath)
	info, err = fsStat(o.upper, layerPath)
	if err == nil {
		return o.overlayInfo(o.upper, info), o.upper, layerPath, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, "", o.overlayErr(o.upper, err)
	}
	if o.hidden(filePath) {
		return nil, nil, "", NewErrDoesNotExist(File(o.paths.JoinCleanURI(filePath)))
	}
	layerPath = o.basePath(filePath)
	info, err = fsStat(o.base, layerPath)
	if err != nil {
		return nil, nil, "", o.overlayErr(o.base, err)
	}
	return o.overlayInfo(o.base, info), o.base, layerPath, nil
}

// Stat returns the FileInfo of the file, looking in the upper layer
// first and falling back to the base layer.
func (o *OverlayFileSystem) Stat(filePath string) (*FileInfo, error) {
	info, _, _, err := o.statLayer(filePath)
	return info, err
}

// pathLayer returns the layer that holds the overlay path and the path
// within it, without following a symbolic link at the path: the upper layer
// if it has an entry at the path, else the base layer if it has one that is
// not hidden, else the upper layer, where a file written at the path is
// created. Copies and moves use it to compare the files of local layers,
// see fsLocalPath.
func (o *OverlayFileSystem) pathLayer(filePath string) (layer FileSystem, layerPath string, err error) {
	if err := o.checkPath(filePath); err != nil {
		return nil, "", err
	}
	upperPath := o.upperPath(filePath)
	_, err = fsLstat(o.upper, upperPath)
	switch {
	case err == nil, errors.Is(err, os.ErrNotExist) && o.hidden(filePath):
		return o.upper, upperPath, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, "", o.overlayErr(o.upper, err)
	}
	basePath := o.basePath(filePath)
	if _, err := fsLstat(o.base, basePath); err != nil {
		return o.upper, upperPath, nil
	}
	return o.base, basePath, nil
}

// readSymbolicLink returns the target of the symbolic link at the overlay
// path, read from the layer that has the link, with an absolute target
// translated to the overlay. It implements linkTargetReader, so that a move
// within the overlay can tell if a link of a layer points to the destination,
// see moveByCopy. The overlay can't create links,
// so it doesn't implement SymbolicLinkFileSystem.
func (o *OverlayFileSystem) readSymbolicLink(linkPath string) (string, error) {
	if err := o.checkPath(linkPath); err != nil {
		return "", err
	}
	layer, layerPath := FileSystem(o.upper), o.upperPath(linkPath)
	_, err := fsLstat(layer, layerPath)
	switch {
	case errors.Is(err, os.ErrNotExist) && o.hidden(linkPath):
		return "", NewErrDoesNotExist(File(o.paths.JoinCleanURI(linkPath)))
	case errors.Is(err, os.ErrNotExist):
		layer, layerPath = o.base, o.basePath(linkPath)
	case err != nil:
		return "", o.overlayErr(layer, err)
	}
	links, ok := layer.(SymbolicLinkFileSystem)
	if !ok {
		return "", NewErrUnsupported(layer, "ReadSymbolicLink")
	}
	target, err := links.ReadSymbolicLink(layerPath)
	if err != nil {
		return "", o.overlayErr(layer, err)
	}
	// The overlay has no volumes and the separator "/",
	// a relative target stays relative
	target = target[fsVolumeLen(layer, target):]
	if sep := layer.Separator(); sep != "/" {
		target = strings.ReplaceAll(target, sep, "/")
	}
	return target, nil
}

// ListDir lists the union of both layers sorted by name,
// with upper entries shadowing base entries of the same name.
// The base layer is not listed for an opaque directory
// that was created at the path of a removed base entry.
func (o *OverlayFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := o.checkPath(dirPath); err != nil {
		return err
	}
	info, err := o.Stat(dirPath)
	if err != nil {
		return err
	}
	if !info.IsDir {
		return NewErrIsNotDirectory(info.File)
	}
	entries := make(map[string]*FileInfo)
	err = fsListDir(ctx, o.upper, o.upperPath(dirPath), patterns, func(info *FileInfo) error {
		entries[info.Name] = o.overlayInfo(o.upper, info)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return o.overlayErr(o.upper, err)
	}
	if !o.hidden(dirPath) {
		err = fsListDir(ctx, o.base, o.basePath(dirPath), patterns, func(info *FileInfo) error {
			if _, shadowed := entries[info.Name]; shadowed || o.hidden(o.CleanPath(dirPath, info.Name)) {
				return nil
			}
			entries[info.Name] = o.overlayInfo(o.base, info)
			return nil
		})
		// The base has nothing to add to a directory
		// that is missing or a file in the base
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrIsNotDirectory{}) {
			return o.overlayErr(o.base, err)
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := callback(entries[name]); err != nil {
			return err
		}
	}
	return nil
}

// OpenReader opens the file for reading from the upper layer,
// or from the base layer if it only exists there.
func (o *OverlayFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	info, layer, layerPath, err := o.statLayer(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, NewErrIsDirectory(info.File)
	}
	r, err := fsOpenReader(layer, layerPath)
	return r, o.overlayErr(layer, err)
}

// ReadAll reads the complete file from the upper layer,
// or from the base layer if it only exists there.
func (o *OverlayFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	info, layer, layerPath, err := o.statLayer(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, NewErrIsDirectory(info.File)
	}
	data, err := fsReadAll(ctx, layer, layerPath)
	return data, o.overlayErr(layer, err)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// prepareUpper makes sure the parent directory of the overlay path exists
// in the upper layer if it exists in the overlay.
func (o *OverlayFileSystem) prepareUpper(filePath string) error {
	parent := o.CleanPath(filePath, "..")
	info, err := o.Stat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir {
		return NewErrIsNotDirectory(info.File)
	}
	err = fsMakeAllDirs(o.upper, o.upperPath(parent), 0)
	if err != nil {
		return o.overlayErr(o.upper, err)
	}
	return nil
}

// prepareWrite prepares the upper layer for writing the file at filePath:
// a directory of either layer is an ErrIsDirectory, the parent directory
// is created in the upper layer, and if copyUp is true a file that only
// exists in the base layer is copied up, so it can be modified in place.
// The caller must hold the path lock of filePath,
// so that no other write of the path interleaves with the copy up.
func (o *OverlayFileSystem) prepareWrite(ctx context.Context, filePath string, copyUp bool) error {
	info, layer, layerPath, err := o.statLayer(filePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		copyUp = false // A new file
	case err != nil:
		return err
	case info.IsDir:
		return NewErrIsDirectory(info.File)
	case layer == o.upper:
		copyUp = false // Already in the upper layer
	}
	if err := o.prepareUpper(filePath); err != nil {
		return err
	}
	if !copyUp {
		return nil
	}
	data, err := fsReadAll(ctx, layer, layerPath)
	if err != nil {
		return o.overlayErr(layer, err)
	}
	// The copy up gets permissions like a copy with CopyFile,
	// a file of a read-only base must be writable in the upper layer
	err = fsWriteAll(ctx, o.upper, o.upperPath(filePath), data, copyPermissions(layer, info.Permissions))
	return o.overlayErr(o.upper, err)
}

// OpenWriter opens the file in the upper layer for writing,
// creating it if it does not exist and truncating it if it does.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := o.checkPath(filePath); err != nil {
		return nil, err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if err := o.prepareWrite(context.Background(), filePath, false); err != nil {
		return nil, err
	}
	w, err := fsOpenWriter(o.upper, o.upperPath(filePath), perm)
	return w, o.overlayErr(o.upper, err)
}

// WriteAll writes data to the file in the upper layer, creating it
// if it does not exist and replacing its content if it does.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if err := o.prepareWrite(ctx, filePath, false); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsWriteAll(ctx, o.upper, o.upperPath(filePath), data, perm))
}

// Append copies a base-only file up to the upper layer
// and appends data to it there.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if err := o.prepareWrite(ctx, filePath, true); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsAppend(ctx, o.upper, o.upperPath(filePath), data, perm))
}

// OpenAppendWriter copies a base-only file up to the upper layer
// and opens it for appending there.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := o.checkPath(filePath); err != nil {
		return nil, err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if err := o.prepareWrite(context.Background(), filePath, true); err != nil {
		return nil, err
	}
	w, err := fsOpenAppendWriter(o.upper, o.upperPath(filePath), perm)
	return w, o.overlayErr(o.upper, err)
}

// OpenReadWriter copies a base-only file up to the upper layer and
// opens it there for reading and writing at any offset.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if err := o.checkPath(filePath); err != nil {
		return nil, err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if err := o.prepareWrite(context.Background(), filePath, true); err != nil {
		return nil, err
	}
	rw, err := fsOpenReadWriter(o.upper, o.upperPath(filePath), perm)
	return rw, o.overlayErr(o.upper, err)
}

// Truncate copies a base-only file up to the upper layer
// and changes its size there.
// A directory is an ErrIsDirectory.
func (o *OverlayFileSystem) Truncate(ctx context.Context, filePath string, size int64) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	// Truncate doesn't create a missing file
	if _, err := o.Stat(filePath); err != nil {
		return err
	}
	if err := o.prepareWrite(ctx, filePath, true); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsTruncate(ctx, o.upper, o.upperPath(filePath), size))
}

// Touch creates a missing file in the upper layer, or copies a
// base-only file up and updates its modification time there.
// A directory of the upper layer is touched there,
// a directory that only exists in the base layer is an ErrIsDirectory.
func (o *OverlayFileSystem) Touch(filePath string, perm Permissions) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	if info, layer, _, err := o.statLayer(filePath); err != nil || !info.IsDir || layer != o.upper {
		if err := o.prepareWrite(context.Background(), filePath, true); err != nil {
			return err
		}
	}
	return o.overlayErr(o.upper, fsTouch(o.upper, o.upperPath(filePath), perm))
}

// MakeDir creates a directory in the upper layer.
// A directory created at the path of a removed base entry is opaque,
// the removed base entries below it stay hidden.
func (o *OverlayFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if err := o.checkPath(dirPath); err != nil {
		return err
	}
	if info, err := o.Stat(dirPath); err == nil {
		return NewErrAlreadyExists(info.File)
	}
	if err := o.prepareUpper(dirPath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsMakeDir(o.upper, o.upperPath(dirPath), perm))
}

// MakeAllDirs creates a directory and all missing parent directories
// in the upper layer. A file of either layer in the path is an
// ErrIsNotDirectory. Like MakeDir it creates opaque directories
// at the paths of removed base entries.
func (o *OverlayFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if err := o.checkPath(dirPath); err != nil {
		return err
	}
	return o.makeAllDirs(o.CleanPath(dirPath), perm)
}

// makeAllDirs creates the clean dirPath and its missing parent
// directories from the deepest existing one down, which must be
// a directory of the overlay.
func (o *OverlayFileSystem) makeAllDirs(dirPath string, perm Permissions) error {
	info, err := o.Stat(dirPath)
	switch {
	case err == nil && info.IsDir:
		return nil
	case err == nil:
		return NewErrIsNotDirectory(info.File)
	case dirPath == "/":
		return err
	}
	if err := o.makeAllDirs(o.CleanPath(dirPath, ".."), perm); err != nil {
		return err
	}
	if err := o.prepareUpper(dirPath); err != nil {
		return err
	}
	err = fsMakeDir(o.upper, o.upperPath(dirPath), perm)
	if errors.Is(err, os.ErrExist) {
		// Created concurrently
		if info, statErr := o.Stat(dirPath); statErr == nil && info.IsDir {
			return nil
		}
	}
	return o.overlayErr(o.upper, err)
}

// Remove removes the entry from the upper layer and hides it in the base
// layer. A directory must be empty in the overlay, else Remove returns
// an ErrDirectoryNotEmpty.
func (o *OverlayFileSystem) Remove(filePath string) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	info, layer, layerPath, err := o.statLayer(filePath)
	if err != nil {
		return err
	}
	if o.CleanPath(filePath) == "/" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, o)
	}
	if info.IsDir {
		empty := true
		err = o.ListDir(context.Background(), filePath, nil, func(*FileInfo) error {
			empty = false
			return errStopListing
		})
		if err != nil && !errors.Is(err, errStopListing) {
			return err
		}
		if !empty {
			return NewErrDirectoryNotEmpty(info.File)
		}
	}
	if layer == o.upper {
		err = fsRemove(layer, layerPath)
		if err != nil {
			return o.overlayErr(layer, err)
		}
	}
	o.hide(filePath)
	return nil
}

// RemoveAll removes the tree from the upper layer and hides it in the
// base layer. A missing path is not an error.
func (o *OverlayFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := o.checkPath(filePath); err != nil {
		return err
	}
	if o.CleanPath(filePath) == "/" {
		return fmt.Errorf("%w: can't remove root directory of %s", os.ErrInvalid, o)
	}
	unlock := o.lockPath(filePath)
	defer unlock()
	err := fsRemoveAll(ctx, o.upper, o.upperPath(filePath))
	if err != nil {
		return o.overlayErr(o.upper, err)
	}
	o.hide(filePath)
	return nil
}

// Close unregisters the overlay if it was registered
// without closing the layers.
func (o *OverlayFileSystem) Close() error {
	o.mtx.Lock()
	if o.closed {
		o.mtx.Unlock()
		return nil
	}
	o.closed = true
	o.mtx.Unlock()
	Unregister(o)
	return nil
}
