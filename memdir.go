package fs

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"strings"
	"time"
)

// Ensure MemDir implements interfaces
var (
	_ FileReader     = MemDir("")
	_ io.WriterTo    = MemDir("")
	_ gob.GobEncoder = MemDir("")
	_ gob.GobDecoder = new(MemDir)
	_ fmt.Stringer   = MemDir("")
)

// MemDir implements FileReader for an in-memory directory
// that is represented by nothing but its path string.
//
// It is the directory counterpart of MemFile: a small, simple value type
// that carries no backing file system and no directory contents.
// Like MemFile it is meant to be passed by value.
//
// All path methods always use '/' as separator. A backslash '\' is
// treated as an ordinary character, not as a path separator.
//
// Because a MemDir has no contents, every method that would read
// file data fails with an ErrIsDirectory error. Methods that are valid
// for a directory, like IsDir, CheckIsDir and ContentHash, behave like
// their File counterparts for a directory.
//
// Note that MemDir fails reads more eagerly than a File backed by the
// local file system: there OpenReader succeeds for a directory (because
// os.Open does) and only the subsequent Read returns an "is a directory"
// error. MemDir instead returns ErrIsDirectory immediately from
// OpenReader and OpenReadSeeker, matching the in-memory MemFileSystem
// which also rejects opening a directory for reading up front.
//
// MemDir implements the following interfaces:
//   - FileReader
//   - io.WriterTo
//   - gob.GobEncoder
//   - gob.GobDecoder
//   - fmt.Stringer
type MemDir string

// String returns the metadata of the directory formatted as a string.
// String implements the fmt.Stringer interface.
func (d MemDir) String() string {
	return fmt.Sprintf("MemDir{name: `%s`}", string(d))
}

// PrettyString implements the pretty.Stringer interface
// to provide a compact representation of the MemDir
// in error messages and pretty-printed output.
func (d MemDir) PrettyString() string {
	return d.String()
}

// Name returns the name of the directory.
// If the path contains a slash then only the last path element
// will be returned. Trailing slashes are ignored.
func (d MemDir) Name() string {
	s := strings.TrimRight(string(d), "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Dir returns the parent directory path as a MemDir,
// using '/' as separator and ignoring trailing slashes.
//
// If the path contains no slash then an empty MemDir is returned.
// If the path starts with a slash and has no further slashes
// then the root "/" is returned.
// The Dir of the root "/" is an empty MemDir.
func (d MemDir) Dir() MemDir {
	return dirPath(string(d))
}

// DirAndName returns the parent directory path as a MemDir
// together with the name of the last path element.
// It is equivalent to calling Dir and Name, see those methods for details.
func (d MemDir) DirAndName() (dir MemDir, name string) {
	return d.Dir(), d.Name()
}

// CleanPath returns the path with "." and ".." path elements
// resolved, redundant slashes removed and trailing slashes trimmed,
// using '/' as separator. An empty path returns an empty MemDir.
func (d MemDir) CleanPath() MemDir {
	return MemDir(cleanPath(string(d)))
}

// Join returns a new MemDir with the passed parts appended to the
// current path, using '/' as separator.
//
// A trailing slash on the current path is ignored, empty parts and
// slashes at the boundary between joined elements are collapsed so the
// result has no empty path elements, but special elements like "." or
// ".." are not resolved. Use CleanPath for that.
func (d MemDir) Join(parts ...string) MemDir {
	result := string(d)
	if len(result) > 1 {
		result = strings.TrimRight(result, "/") // ignore trailing slashes, keep a lone root "/"
	}
	for _, part := range parts {
		part = strings.Trim(part, "/")
		switch {
		case part == "":
			continue
		case result == "":
			result = part
		case strings.HasSuffix(result, "/"): // only true for the root "/"
			result += part
		default:
			result += "/" + part
		}
	}
	return MemDir(result)
}

// Ext returns the extension of the directory name including the point,
// or an empty string. '/' is used as separator and trailing slashes
// are ignored.
func (d MemDir) Ext() string {
	return path.Ext(d.Name())
}

// ExtLower returns the lower case extension of the directory name
// including the point, or an empty string.
func (d MemDir) ExtLower() string {
	return strings.ToLower(d.Ext())
}

// LocalPath always returns an empty string for a MemDir.
func (MemDir) LocalPath() string {
	return ""
}

// Size always returns 0 for a MemDir.
func (MemDir) Size() int64 {
	return 0
}

// Exists returns true if the MemDir has a non empty path.
func (d MemDir) Exists() bool {
	return d != ""
}

// CheckExists returns an ErrDoesNotExist error
// if the directory does not exist.
func (d MemDir) CheckExists() error {
	if !d.Exists() {
		return NewErrDoesNotExistFileReader(d)
	}
	return nil
}

// IsDir always returns true for a MemDir.
func (MemDir) IsDir() bool {
	return true
}

// CheckIsDir returns an ErrEmptyPath error
// if the path is empty, or nil because a MemDir
// is always a directory.
func (d MemDir) CheckIsDir() error {
	if d == "" {
		return ErrEmptyPath
	}
	return nil
}

// ContentHash returns an empty string because a MemDir is a directory,
// matching the behavior of File.ContentHash for a directory.
func (d MemDir) ContentHash() (string, error) {
	return "", nil
}

// ContentHashContext returns an empty string because a MemDir is a directory,
// matching the behavior of File.ContentHashContext for a directory.
func (d MemDir) ContentHashContext(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", nil
}

// ReadAll returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadAll() (data []byte, err error) {
	return nil, NewErrIsDirectory(d)
}

// ReadAllContext returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadAllContext(ctx context.Context) (data []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, NewErrIsDirectory(d)
}

// ReadAllContentHash returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadAllContentHash(ctx context.Context) (data []byte, hash string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", NewErrIsDirectory(d)
}

// ReadAllString returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadAllString() (string, error) {
	return "", NewErrIsDirectory(d)
}

// ReadAllStringContext returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadAllStringContext(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", NewErrIsDirectory(d)
}

// WriteTo returns an ErrIsDirectory error because a MemDir is a directory.
// WriteTo implements the io.WriterTo interface.
func (d MemDir) WriteTo(writer io.Writer) (n int64, err error) {
	return 0, NewErrIsDirectory(d)
}

// OpenReader returns an ErrIsDirectory error because a MemDir is a directory.
//
// Unlike a local file system File, whose OpenReader succeeds for a
// directory and only fails on the subsequent Read, MemDir reports the
// ErrIsDirectory error here immediately.
func (d MemDir) OpenReader() (ReadCloser, error) {
	return nil, NewErrIsDirectory(d)
}

// OpenReadSeeker returns an ErrIsDirectory error because a MemDir is a directory.
//
// Unlike a local file system File, whose OpenReadSeeker succeeds for a
// directory and only fails on the subsequent Read, MemDir reports the
// ErrIsDirectory error here immediately.
func (d MemDir) OpenReadSeeker() (ReadSeekCloser, error) {
	return nil, NewErrIsDirectory(d)
}

// ReadJSON returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadJSON(ctx context.Context, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return NewErrIsDirectory(d)
}

// ReadXML returns an ErrIsDirectory error because a MemDir is a directory.
func (d MemDir) ReadXML(ctx context.Context, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return NewErrIsDirectory(d)
}

// GobEncode gob encodes the directory path,
// implementing encoding/gob.GobEncoder.
func (d MemDir) GobEncode() ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 16+len(d)))
	enc := gob.NewEncoder(buf)
	if err := enc.Encode(string(d)); err != nil {
		return nil, fmt.Errorf("MemDir.GobEncode: error encoding dir name: %w", err)
	}
	return buf.Bytes(), nil
}

// GobDecode decodes the directory path from gobBytes,
// implementing encoding/gob.GobDecoder.
func (d *MemDir) GobDecode(gobBytes []byte) error {
	dec := gob.NewDecoder(bytes.NewReader(gobBytes))
	var name string
	if err := dec.Decode(&name); err != nil {
		return fmt.Errorf("MemDir.GobDecode: error decoding dir name: %w", err)
	}
	*d = MemDir(name)
	return nil
}

// Stat returns a io/fs.FileInfo describing the MemDir.
func (d MemDir) Stat() (iofs.FileInfo, error) {
	return memDirInfo{d}, nil
}

var _ iofs.FileInfo = memDirInfo{}

// memDirInfo implements io/fs.FileInfo for a MemDir.
//
// Name(), Size() and IsDir() are derived from the embedded MemDir.
type memDirInfo struct {
	MemDir
}

func (i memDirInfo) Mode() iofs.FileMode { return iofs.ModeDir | 0777 }
func (i memDirInfo) ModTime() time.Time  { return time.Now() }
func (i memDirInfo) Sys() any            { return nil }

// cleanPath returns the shortest equivalent of p by resolving
// "." and ".." path elements, removing redundant slashes and
// trimming trailing slashes, using '/' as separator.
// An empty p returns an empty string.
func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	return strings.TrimRight(path.Clean(p), "/")
}

// dirPath returns the parent directory path of p as a MemDir,
// using '/' as separator and ignoring trailing slashes.
// It implements the shared logic of MemFile.Dir and MemDir.Dir.
func dirPath(p string) MemDir {
	p = strings.TrimRight(p, "/")
	i := strings.LastIndexByte(p, '/')
	switch {
	case i < 0:
		return "" // no slash (also covers a path of only slashes)
	case i == 0:
		return "/" // root
	default:
		return MemDir(p[:i])
	}
}
