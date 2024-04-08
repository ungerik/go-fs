// Package ftpfs implements a FTP(S) client file system.
package ftpfs

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	Prefix    = "ftp://"
	PrefixTLS = "ftps://"
	Separator = "/"
)

func init() {
	// Register with prefix ftp:// and ftps:// for URLs with
	// ftp(s)://username:password@host:port schema.
	fs.Register(&fileSystem{secure: false, prefix: Prefix})
	fs.Register(&fileSystem{secure: true, prefix: PrefixTLS})
}

// CredentialsCallback is called by Dial to get the username and password for a SFTP connection.
type CredentialsCallback func(*url.URL) (username, password string, err error)

// Password returns a CredentialsCallback that always returns
// the provided password together with the username
// from the URL that is passed to the callback.
func Password(password string) CredentialsCallback {
	return func(u *url.URL) (string, string, error) {
		return u.User.String(), password, nil
	}
}

// UsernameAndPassword returns a CredentialsCallback that always returns
// the provided username and password.
func UsernameAndPassword(username, password string) CredentialsCallback {
	return func(u *url.URL) (string, string, error) {
		return username, password, nil
	}
}

// // UsernameAndPasswordFromURL is a CredentialsCallback that returns
// // the username and password encoded in the passed URL.
// func UsernameAndPasswordFromURL(u *url.URL) (username, password string, err error) {
// 	password, ok := u.User.Password()
// 	if !ok {
// 		return "", "", fmt.Errorf("no password in URL: %s", u.String())
// 	}
// 	return u.User.Username(), password, nil
// }

type fileSystem struct {
	conn   *ftp.ServerConn
	prefix string
	secure bool
}

// Dial a new FTP or FTPS connection and registers it as file system.
//
// The passed address can be a URL with scheme `ftp:` or `ftps:`.
func Dial(ctx context.Context, address string, credentialsCallback CredentialsCallback) (fs.FileSystem, error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nil, err
	}
	conn, err := dial(ctx, u.Host, username, password, secure)
	if err != nil {
		return nil, err
	}
	return &fileSystem{
		conn:   conn,
		prefix: prefix,
		secure: secure,
	}, nil
}

func dial(ctx context.Context, host, username, password string, secure bool) (conn *ftp.ServerConn, err error) {
	if secure {
		if !strings.ContainsRune(host, ':') {
			host += ":990"
		}
		conn, err = ftp.Dial(
			host,
			ftp.DialWithContext(ctx),
			ftp.DialWithTLS(&tls.Config{InsecureSkipVerify: true}), // DialWithExplicitTLS also possible
		)
	} else {
		if !strings.ContainsRune(host, ':') {
			host += ":21"
		}
		conn, err = ftp.Dial(
			host,
			ftp.DialWithContext(ctx),
		)
	}
	if err != nil {
		return nil, err
	}
	err = conn.Login(username, password)
	if err != nil {
		return nil, errors.Join(err, conn.Quit())
	}
	return conn, nil
}

// DialAndRegister dials a new FTP or FTPS connection and register it as file system.
//
// The passed address can be a URL with scheme `ftp:` or `ftps:`.
func DialAndRegister(ctx context.Context, address string, credentialsCallback CredentialsCallback) (fs.FileSystem, error) {
	fileSystem, err := Dial(ctx, address, credentialsCallback)
	if err != nil {
		return nil, err
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

// EnsureRegistered first checks if a FTP(S) file system with the passed address
// is already registered. If not, then a new connection is dialed and registered.
// The returned free function has to be called to decrease the file system's
// reference count and close it when the reference count reaches 0.
// The returned free function will never be nil.
func EnsureRegistered(ctx context.Context, address string, credentialsCallback CredentialsCallback) (free func() error, err error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nop, err
	}
	f := fs.GetFileSystemByPrefixOrNil(prefix)
	if f != nil {
		fs.Register(f) // Increase ref count
		return func() error { fs.Unregister(f); return nil }, nil
	}

	conn, err := dial(ctx, u.Host, username, password, secure)
	if err != nil {
		return nop, err
	}
	f = &fileSystem{
		conn:   conn,
		prefix: prefix,
		secure: secure,
	}
	fs.Register(f) // TODO somone else might have registered, so free should not close it
	return func() error { return f.Close() }, nil
}

func prepareDial(address string, credentialsCallback CredentialsCallback) (u *url.URL, username, password, prefix string, secure bool, err error) {
	if !strings.HasPrefix(address, "ftp://") && !strings.HasPrefix(address, "ftps://") {
		if strings.Contains(address, "://") {
			return nil, "", "", "", false, fmt.Errorf("not an FTP or FTPS URL scheme: %s", address)
		}
		address = "ftp://" + address
	}
	if credentialsCallback == nil {
		return nil, "", "", "", false, errors.New("nil credentialsCall")
	}
	u, err = url.Parse(address)
	if err != nil {
		return nil, "", "", "", false, err
	}
	if u.Scheme != "ftp" && u.Scheme != "ftps" {
		return nil, "", "", "", false, fmt.Errorf("not an FTP or FTPS URL scheme: %s", address)
	}
	// Trim default port number
	switch u.Scheme {
	case "ftp":
		u.Host = strings.TrimSuffix(u.Host, ":21")
	case "ftps":
		u.Host = strings.TrimSuffix(u.Host, ":990")
	}

	username, password, err = credentialsCallback(u)
	if err != nil {
		return nil, "", "", "", false, err
	}
	if username == "" {
		return nil, "", "", "", false, fmt.Errorf("missing FTP username for: %s", address)
	}
	if password == "" {
		return nil, "", "", "", false, fmt.Errorf("missing FTP password for: %s", address)
	}
	prefix = fmt.Sprintf("%s://%s@%s", u.Scheme, url.User(username), u.Host)
	secure = u.Scheme == "ftps"

	return u, username, password, prefix, secure, nil
}

func nop() error { return nil }

func (f *fileSystem) getConn(ctx context.Context, filePath string) (conn *ftp.ServerConn, clientPath string, release func() error, err error) {
	if err = ctx.Err(); err != nil {
		return nil, "", nop, err
	}
	if f.conn != nil {
		return f.conn, filePath, nop, nil
	}

	// fmt.Printf("%s file system not registered, trying to dial with credentials from URL: %s", f.Name(), f.URL(filePath))

	u, err := url.Parse(f.URL(filePath))
	if err != nil {
		return nil, "", nop, err
	}
	username := u.User.Username()
	if username == "" {
		return nil, "", nop, fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
	}
	password, ok := u.User.Password()
	if !ok {
		return nil, "", nop, fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
	}

	conn, err = dial(ctx, u.Host, username, password, f.secure)
	if err != nil {
		return nil, "", nop, err
	}
	return conn, u.Path, func() error { return conn.Quit() }, nil
}

func (f *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, true
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.prefix + Separator)
}

func (f *fileSystem) ID() (string, error) {
	return f.prefix, nil
}

func (f *fileSystem) Prefix() string {
	return f.prefix
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) Name() string {
	if f.secure {
		return "FTPS"
	}
	return "FTP"
}

func (f *fileSystem) String() string {
	return f.prefix + " file system"
}

func (f *fileSystem) URL(cleanPath string) string {
	return f.prefix + cleanPath
}

func (f *fileSystem) CleanPathFromURI(uri string) string {
	port := ":21"
	if f.secure {
		port = ":990"
	}
	return path.Clean(
		strings.TrimPrefix(
			strings.TrimPrefix(uri, f.prefix),
			port, // In case f.prefix has no port number and url has the default port number
		),
	)
}

func (f *fileSystem) JoinCleanPath(uriParts ...string) string {
	if f.secure {
		return fsimpl.JoinCleanPath(uriParts, PrefixTLS, Separator)
	}
	return fsimpl.JoinCleanPath(uriParts, Prefix, Separator)
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	path := f.JoinCleanPath(uriParts...)
	if strings.HasSuffix(f.prefix, Separator) && strings.HasPrefix(path, Separator) {
		// For example: "sftp://" + "/example.com/absolute/path"
		// should not result in 3 slashes: "sftp:///example.com/absolute/path"
		path = path[len(Separator):]
	}
	return fs.File(f.prefix + path)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.prefix, Separator)
}

func (f *fileSystem) IsAbsPath(filePath string) bool {
	if f.secure {
		return strings.HasPrefix(filePath, PrefixTLS)
	}
	return strings.HasPrefix(filePath, Prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return Prefix + strings.TrimPrefix(filePath, Separator)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

type fileInfo struct {
	entry *ftp.Entry
}

func (i fileInfo) Name() string        { return i.entry.Name }
func (i fileInfo) Size() int64         { return int64(i.entry.Size) }
func (i fileInfo) Mode() iofs.FileMode { return 0666 }
func (i fileInfo) ModTime() time.Time  { return i.entry.Time }
func (i fileInfo) IsDir() bool         { return i.entry.Type == ftp.EntryTypeFolder }
func (i fileInfo) Sys() any            { return nil }

func entryToFileInfo(entry *ftp.Entry, file fs.File) *fs.FileInfo {
	return &fs.FileInfo{
		File:        file,
		Name:        entry.Name,
		Exists:      true,
		IsDir:       entry.Type == ftp.EntryTypeFolder,
		IsRegular:   entry.Type != ftp.EntryTypeLink,
		IsHidden:    false,
		Size:        int64(entry.Size),
		Modified:    entry.Time,
		Permissions: 0666,
	}
}

func (f *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	entry, err := conn.GetEntry(filePath)
	if err != nil {
		return nil, err
	}
	return fileInfo{entry}, nil
}

func (f *fileSystem) IsHidden(filePath string) bool { return false }

func (f *fileSystem) IsSymbolicLink(filePath string) bool {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return false
	}
	defer release()

	entry, err := conn.GetEntry(filePath)
	if err != nil {
		return false
	}
	return entry.Type == ftp.EntryTypeLink
}

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	conn, dirPath, release, err := f.getConn(ctx, dirPath)
	if err != nil {
		return err
	}
	defer release()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	entries, err := conn.List(dirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		match, err := fsimpl.MatchAnyPattern(entry.Name, patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		err = callback(entryToFileInfo(entry, f.JoinCleanFile(dirPath, entry.Name)))
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (f *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	conn, dirPath, release, err := f.getConn(context.Background(), dirPath)
	if err != nil {
		return err
	}
	defer release()

	return conn.MakeDir(dirPath)
}

type fileReader struct {
	path     string
	conn     *ftp.ServerConn
	response *ftp.Response
	release  func() error
}

func (f *fileReader) Stat() (iofs.FileInfo, error) {
	entry, err := f.conn.GetEntry(f.path)
	if err != nil {
		return nil, err
	}
	return fileInfo{entry}, nil
}

func (f *fileReader) Read(buf []byte) (int, error) {
	return f.response.Read(buf)
}

func (f *fileReader) Close() error {
	return errors.Join(f.response.Close(), f.release())
}

func (f *fileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return nil, err
	}

	response, err := conn.Retr(filePath)
	if err != nil {
		return nil, errors.Join(err, release())
	}

	return &fileReader{
		path:     filePath,
		conn:     conn,
		response: response,
		release:  release,
	}, nil
}

func (f *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	return f.OpenReadWriter(filePath, perm)
}

// func (f *FTPFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {

// }

type file struct {
	path    string
	offset  int64
	conn    *ftp.ServerConn
	release func() error
}

func (f *file) Read(p []byte) (n int, err error) {
	return f.ReadAt(p, f.offset)
}

func (f *file) ReadAt(p []byte, offset int64) (n int, err error) {
	response, err := f.conn.RetrFrom(f.path, uint64(offset))
	if err != nil {
		return 0, err
	}
	return response.Read(p)
}

func (f *file) Write(p []byte) (n int, err error) {
	return f.WriteAt(p, f.offset)
}

func (f *file) WriteAt(p []byte, offset int64) (n int, err error) {
	r := bytes.NewReader(p)
	err = f.conn.StorFrom(f.path, r, uint64(offset))
	return len(p) - r.Len(), err
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		size, err := f.conn.FileSize(f.path)
		if err != nil {
			return 0, err
		}
		f.offset = size + offset
	}
	return f.offset, nil
}

func (f *file) Close() error {
	return f.release()
}

func (f *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	return &file{
		path:    filePath,
		conn:    conn,
		release: release,
	}, nil
}

func (f *fileSystem) Move(filePath string, destPath string) error {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	return conn.Rename(filePath, destPath)
}

func (f *fileSystem) Remove(filePath string) error {
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	entry, err := conn.GetEntry(filePath)
	if err != nil {
		return err
	}
	if entry.Type == ftp.EntryTypeFolder {
		return conn.RemoveDir(filePath)
	}
	return conn.Delete(filePath)
}

func (f *fileSystem) Close() error {
	if f.conn == nil {
		return nil // already closed
	}
	count := fs.Unregister(f)
	if count > 1 {
		return nil // still referenced
	}
	err := f.conn.Quit()
	f.conn = nil
	return err
}
