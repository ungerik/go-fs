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
	_ ExistsFileSystem       = new(OverlayFileSystem)
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
// access, truncate, touch) is copied up to the upper layer first.
//
// Removing a base entry records a whiteout that hides it and everything
// below it. Whiteouts are kept in memory: a new overlay over the same
// layers starts without them.
//
// Both layers are used with the overlay paths, so the roots of the two
// layers map to the root of the overlay. Wrap a layer in a SubFileSystem
// to overlay a sub directory. The layers must stay registered while the
// overlay is used, because emulated operations go through the File API.
type OverlayFileSystem struct {
	fsimpl.PathHelper

	base   FileSystem
	upper  WriteFileSystem
	id     string
	closed bool

	mtx       sync.RWMutex
	whiteouts map[string]struct{} // overlay paths of removed base entries
}

// NewOverlayFileSystem returns a file system with the URI prefix
// "overlay://" + id that reads from upper and base and writes to upper.
// A random id is used if id is empty. The file system is not registered.
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
		PathHelper: fsimpl.PathHelper{URIPrefix: OverlayFileSystemPrefix + id, Rooted: true},
		base:       base,
		upper:      upper,
		id:         id,
		whiteouts:  make(map[string]struct{}),
	}, nil
}

// NewOverlayFileSystemAndRegister returns a registered overlay
// file system, see NewOverlayFileSystem.
func NewOverlayFileSystemAndRegister(base FileSystem, upper WriteFileSystem, id string) (*OverlayFileSystem, error) {
	overlay, err := NewOverlayFileSystem(base, upper, id)
	if err != nil {
		return nil, err
	}
	Register(overlay)
	return overlay, nil
}

// Base returns the read-only base layer.
func (o *OverlayFileSystem) Base() FileSystem {
	return o.base
}

// Upper returns the writable upper layer.
func (o *OverlayFileSystem) Upper() WriteFileSystem {
	return o.upper
}

func (o *OverlayFileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (o *OverlayFileSystem) RootDir() File {
	return File(o.URIPrefix + "/")
}

func (o *OverlayFileSystem) ID() string {
	return o.id
}

func (o *OverlayFileSystem) Name() string {
	return "overlay file system"
}

func (o *OverlayFileSystem) String() string {
	return fmt.Sprintf("%s with prefix %s of %s over %s", o.Name(), o.URIPrefix, o.upper, o.base)
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
func (o *OverlayFileSystem) overlayPath(layer FileSystem, layerPath string) string {
	if sep := layer.Separator(); sep != "/" {
		layerPath = strings.ReplaceAll(layerPath, sep, "/")
	}
	return o.CleanPath(layerPath)
}

// overlayFile translates a File of a layer to the overlay.
func (o *OverlayFileSystem) overlayFile(layer FileSystem, layerFile File) File {
	return File(o.JoinCleanURI(o.overlayPath(layer, layerFile.Path())))
}

// overlayInfo translates a FileInfo of a layer to the overlay.
func (o *OverlayFileSystem) overlayInfo(layer FileSystem, info *FileInfo) *FileInfo {
	overlayInfo := *info
	overlayInfo.File = o.overlayFile(layer, info.File)
	return &overlayInfo
}

// overlayErr translates the file of typed layer errors to the overlay.
func (o *OverlayFileSystem) overlayErr(layer FileSystem, err error) error {
	var (
		notExist ErrDoesNotExist
		exists   ErrAlreadyExists
		isDir    ErrIsDirectory
		isNotDir ErrIsNotDirectory
	)
	switch {
	case errors.As(err, &notExist):
		if f, ok := notExist.file.(File); ok {
			return NewErrDoesNotExist(o.overlayFile(layer, f))
		}
	case errors.As(err, &exists):
		return NewErrAlreadyExists(o.overlayFile(layer, exists.file))
	case errors.As(err, &isDir):
		if f, ok := isDir.file.(File); ok {
			return NewErrIsDirectory(o.overlayFile(layer, f))
		}
	case errors.As(err, &isNotDir):
		if f, ok := isNotDir.file.(File); ok {
			return NewErrIsNotDirectory(o.overlayFile(layer, f))
		}
	}
	return err
}

// hidden reports whether the base entry at the overlay path
// or one of its ancestors was removed.
func (o *OverlayFileSystem) hidden(filePath string) bool {
	o.mtx.RLock()
	defer o.mtx.RUnlock()
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

// unhide removes the whiteouts of the overlay path and its ancestors,
// because the path exists again in the upper layer.
func (o *OverlayFileSystem) unhide(filePath string) {
	o.mtx.Lock()
	defer o.mtx.Unlock()
	p := o.CleanPath(filePath)
	for {
		delete(o.whiteouts, p)
		if p == "/" {
			return
		}
		p = o.CleanPath(p, "..")
	}
}

func (o *OverlayFileSystem) checkClosed() error {
	o.mtx.RLock()
	defer o.mtx.RUnlock()
	if o.closed {
		return ErrFileSystemClosed
	}
	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Reading

// stat returns the FileInfo of the upper layer, or of the base layer
// if the upper layer has no entry and the base entry is not hidden.
func (o *OverlayFileSystem) stat(filePath string) (*FileInfo, error) {
	if err := o.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	info, err := fsStat(o.upper, o.upperPath(filePath))
	if err == nil {
		return o.overlayInfo(o.upper, info), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, o.overlayErr(o.upper, err)
	}
	if o.hidden(filePath) {
		return nil, NewErrDoesNotExist(File(o.JoinCleanURI(filePath)))
	}
	info, err = fsStat(o.base, o.basePath(filePath))
	if err != nil {
		return nil, o.overlayErr(o.base, err)
	}
	return o.overlayInfo(o.base, info), nil
}

func (o *OverlayFileSystem) Stat(filePath string) (*FileInfo, error) {
	return o.stat(filePath)
}

func (o *OverlayFileSystem) Exists(filePath string) (bool, error) {
	_, err := o.stat(filePath)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, err
}

// ListDir lists the union of both layers sorted by name,
// with upper entries shadowing base entries of the same name.
func (o *OverlayFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	info, err := o.stat(dirPath)
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
		if err != nil && !errors.Is(err, os.ErrNotExist) {
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

// inUpper reports whether the path exists in the upper layer.
func (o *OverlayFileSystem) inUpper(filePath string) (bool, error) {
	return fsExists(o.upper, o.upperPath(filePath))
}

func (o *OverlayFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	info, err := o.stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, NewErrIsDirectory(info.File)
	}
	upper, err := o.inUpper(filePath)
	if err != nil {
		return nil, err
	}
	if upper {
		r, err := fsOpenReader(o.upper, o.upperPath(filePath))
		return r, o.overlayErr(o.upper, err)
	}
	r, err := fsOpenReader(o.base, o.basePath(filePath))
	return r, o.overlayErr(o.base, err)
}

func (o *OverlayFileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	info, err := o.stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, NewErrIsDirectory(info.File)
	}
	upper, err := o.inUpper(filePath)
	if err != nil {
		return nil, err
	}
	if upper {
		data, err := fsReadAll(ctx, o.upper, o.upperPath(filePath))
		return data, o.overlayErr(o.upper, err)
	}
	data, err := fsReadAll(ctx, o.base, o.basePath(filePath))
	return data, o.overlayErr(o.base, err)
}

///////////////////////////////////////////////////////////////////////////////
// Writing

// prepareUpper makes sure the parent directory of the overlay path exists
// in the upper layer if it exists in the overlay, and drops whiteouts of
// the path because it is about to exist in the upper layer.
func (o *OverlayFileSystem) prepareUpper(filePath string) error {
	parent := o.CleanPath(filePath, "..")
	info, err := o.stat(parent)
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
	o.unhide(filePath)
	return nil
}

// copyUp copies a file that only exists in the base layer to the upper
// layer, so it can be modified in place. Files that already exist in the
// upper layer or don't exist at all are left alone.
func (o *OverlayFileSystem) copyUp(ctx context.Context, filePath string) error {
	upper, err := o.inUpper(filePath)
	if err != nil || upper {
		return err
	}
	info, err := o.stat(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir {
		return NewErrIsDirectory(info.File)
	}
	data, err := fsReadAll(ctx, o.base, o.basePath(filePath))
	if err != nil {
		return o.overlayErr(o.base, err)
	}
	err = o.prepareUpper(filePath)
	if err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsWriteAll(ctx, o.upper, o.upperPath(filePath), data, info.Permissions))
}

func (o *OverlayFileSystem) OpenWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := o.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := o.prepareUpper(filePath); err != nil {
		return nil, err
	}
	w, err := fsOpenWriter(o.upper, o.upperPath(filePath), perm)
	return w, o.overlayErr(o.upper, err)
}

func (o *OverlayFileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := o.prepareUpper(filePath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsWriteAll(ctx, o.upper, o.upperPath(filePath), data, perm))
}

func (o *OverlayFileSystem) Append(ctx context.Context, filePath string, data []byte, perm Permissions) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := o.copyUp(ctx, filePath); err != nil {
		return err
	}
	if err := o.prepareUpper(filePath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsAppend(ctx, o.upper, o.upperPath(filePath), data, perm))
}

func (o *OverlayFileSystem) OpenAppendWriter(filePath string, perm Permissions) (io.WriteCloser, error) {
	if err := o.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := o.copyUp(context.Background(), filePath); err != nil {
		return nil, err
	}
	if err := o.prepareUpper(filePath); err != nil {
		return nil, err
	}
	w, err := fsOpenAppendWriter(o.upper, o.upperPath(filePath), perm)
	return w, o.overlayErr(o.upper, err)
}

func (o *OverlayFileSystem) OpenReadWriter(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
	if err := o.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, ErrEmptyPath
	}
	if err := o.copyUp(context.Background(), filePath); err != nil {
		return nil, err
	}
	if err := o.prepareUpper(filePath); err != nil {
		return nil, err
	}
	rw, err := fsOpenReadWriter(o.upper, o.upperPath(filePath), perm)
	return rw, o.overlayErr(o.upper, err)
}

func (o *OverlayFileSystem) Truncate(filePath string, size int64) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := o.copyUp(context.Background(), filePath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsTruncate(context.Background(), o.upper, o.upperPath(filePath), size))
}

func (o *OverlayFileSystem) Touch(filePath string, perm Permissions) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if err := o.copyUp(context.Background(), filePath); err != nil {
		return err
	}
	if err := o.prepareUpper(filePath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsTouch(o.upper, o.upperPath(filePath), perm))
}

func (o *OverlayFileSystem) MakeDir(dirPath string, perm Permissions) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	if info, err := o.stat(dirPath); err == nil {
		return NewErrAlreadyExists(info.File)
	}
	if err := o.prepareUpper(dirPath); err != nil {
		return err
	}
	return o.overlayErr(o.upper, fsMakeDir(o.upper, o.upperPath(dirPath), perm))
}

func (o *OverlayFileSystem) MakeAllDirs(dirPath string, perm Permissions) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if dirPath == "" {
		return ErrEmptyPath
	}
	if info, err := o.stat(dirPath); err == nil {
		if !info.IsDir {
			return NewErrIsNotDirectory(info.File)
		}
		return nil
	}
	o.unhide(dirPath)
	return o.overlayErr(o.upper, fsMakeAllDirs(o.upper, o.upperPath(dirPath), perm))
}

// Remove removes the entry from the upper layer and hides it in the base
// layer. A directory must be empty in the overlay.
func (o *OverlayFileSystem) Remove(filePath string) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	info, err := o.stat(filePath)
	if err != nil {
		return err
	}
	if o.CleanPath(filePath) == "/" {
		return fmt.Errorf("can't remove root directory of %s", o)
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
			return fmt.Errorf("directory not empty: %s", info.File)
		}
	}
	upper, err := o.inUpper(filePath)
	if err != nil {
		return err
	}
	if upper {
		err = fsRemove(o.upper, o.upperPath(filePath))
		if err != nil {
			return o.overlayErr(o.upper, err)
		}
	}
	o.hide(filePath)
	return nil
}

// RemoveAll removes the tree from the upper layer and hides it in the
// base layer. A missing path is not an error.
func (o *OverlayFileSystem) RemoveAll(ctx context.Context, filePath string) error {
	if err := o.checkClosed(); err != nil {
		return err
	}
	if filePath == "" {
		return ErrEmptyPath
	}
	if o.CleanPath(filePath) == "/" {
		return fmt.Errorf("can't remove root directory of %s", o)
	}
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
