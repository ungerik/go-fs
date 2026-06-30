// Package ftpfs implements a FTP(S) client file system.
package ftpfs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/textproto"
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
	closed bool
}

// Dial a new FTP or FTPS connection and registers it as file system.
//
// The passed address can be a URL with scheme `ftp:` or `ftps:`.
func Dial(ctx context.Context, address string, credentialsCallback CredentialsCallback, debugOut io.Writer) (fs.FileSystem, error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nil, err
	}
	conn, err := dial(ctx, u.Host, username, password, secure, debugOut)
	if err != nil {
		return nil, err
	}
	return &fileSystem{
		conn:   conn,
		prefix: prefix,
		secure: secure,
	}, nil
}

func dial(ctx context.Context, host, username, password string, secure bool, debugOut io.Writer) (conn *ftp.ServerConn, err error) {
	dialOptions := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithDebugOutput(debugOut),
		ftp.DialWithDisabledEPSV(true), // Disable EPSV to use regular PASV mode
	}
	if secure {
		if !strings.ContainsRune(host, ':') {
			host += ":21" // Use port 21 for explicit TLS
		}
		// Use very permissive TLS configuration for FTPS to work around jlaffaye/ftp library issues
		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,             //#nosec G402 -- Accept any certificate (self-signed, expired, etc.)
			ServerName:         "",               // Don't verify server name
			MinVersion:         tls.VersionTLS10, // Accept TLS 1.0+ (more permissive)
			MaxVersion:         tls.VersionTLS13, // Support up to TLS 1.3
			// Disable certificate verification completely
			VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error { //#nosec G123 -- intentional: FTPS workaround already accepts any certificate via InsecureSkipVerify
				return nil // Accept any certificate
			},
		}
		dialOptions = append(dialOptions, ftp.DialWithExplicitTLS(tlsConfig))
	} else {
		if !strings.ContainsRune(host, ':') {
			host += ":21"
		}
	}
	conn, err = ftp.Dial(host, dialOptions...)
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
func DialAndRegister(ctx context.Context, address string, credentialsCallback CredentialsCallback, debugOut io.Writer) (fs.FileSystem, error) {
	fileSystem, err := Dial(ctx, address, credentialsCallback, debugOut)
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
func EnsureRegistered(ctx context.Context, address string, credentialsCallback CredentialsCallback, debugOut io.Writer) (free func() error, err error) {
	u, username, password, prefix, secure, err := prepareDial(address, credentialsCallback)
	if err != nil {
		return nop, err
	}
	if f := fs.GetFileSystemByPrefixOrNil(prefix); f != nil {
		fs.Register(f) // increase ref count
		return func() error { return f.Close() }, nil
	}

	conn, err := dial(ctx, u.Host, username, password, secure, debugOut)
	if err != nil {
		return nop, err
	}
	newFS := &fileSystem{
		conn:   conn,
		prefix: prefix,
		secure: secure,
	}
	// Register dedups by prefix. If another caller registered a file system
	// with the same prefix while we were dialing, our freshly dialed connection
	// is redundant: close it and hand back a free that only drops the ref count
	// of the file system that actually won the race. The returned free is always
	// ref-count aware (it closes the connection only when the last reference is
	// released), so it can never close a file system another caller still holds.
	fs.Register(newFS)
	if registered := fs.GetFileSystemByPrefixOrNil(prefix); registered != fs.FileSystem(newFS) {
		_ = newFS.closeConn()
		return func() error { return registered.Close() }, nil
	}
	return func() error { return newFS.Close() }, nil
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
	// A closed file system must not be used, and in particular must not
	// silently dial a new connection below using credentials from the URL.
	if f.closed {
		return nil, "", nop, fs.ErrFileSystemClosed
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

	conn, err = dial(ctx, u.Host, username, password, f.secure, nil)
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
		return fsimpl.JoinCleanPath(uriParts, PrefixTLS)
	}
	return fsimpl.JoinCleanPath(uriParts, Prefix)
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
func (i fileInfo) Size() int64         { return int64(i.entry.Size) } //#nosec G115 -- int64 limit will not be exceeded in real world use cases
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
		Size:        int64(entry.Size), //#nosec G115 -- int64 limit will not be exceeded in real world use cases
		Modified:    entry.Time,
		Permissions: 0666,
	}
}

func (f *fileSystem) Stat(filePath string) (info iofs.FileInfo, err error) {
	defer f.convertResultError(&err, filePath)

	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	// Try GetEntry first (uses STAT command)
	entry, err := conn.GetEntry(filePath)
	if err != nil {
		// If GetEntry fails (e.g., 502 Command not implemented),
		// fall back to using List on the parent directory
		dir, name := f.SplitDirAndName(filePath)
		if dir == "" {
			dir = "/"
		}

		entries, listErr := conn.List(dir)
		if listErr != nil {
			return nil, err // Return original GetEntry error
		}

		// Find the entry in the list
		for _, e := range entries {
			if e.Name == name {
				return fileInfo{e}, nil
			}
		}

		// If not found in list, return original error
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

	// Try GetEntry first
	entry, err := conn.GetEntry(filePath)
	if err != nil {
		// Fall back to List if GetEntry fails
		dir, name := f.SplitDirAndName(filePath)
		if dir == "" {
			dir = "/"
		}

		entries, listErr := conn.List(dir)
		if listErr != nil {
			return false
		}

		// Find the entry in the list
		for _, e := range entries {
			if e.Name == name {
				return e.Type == ftp.EntryTypeLink
			}
		}
		return false
	}
	return entry.Type == ftp.EntryTypeLink
}

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	defer f.convertResultError(&err, dirPath)

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

func (f *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) (err error) {
	defer f.convertResultError(&err, dirPath)

	conn, dirPath, release, err := f.getConn(context.Background(), dirPath)
	if err != nil {
		return err
	}
	defer release()

	return ignoreFTPSuccessResponse(conn.MakeDir(dirPath))
}

// ignoreFTPSuccessResponse maps error values that actually carry a success
// status reply back to nil. Some FTP servers (and the jlaffaye/ftp library
// when used with the permissive FTPS configuration above) surface 1xx/2xx
// status replies from a data transfer as errors.
func ignoreFTPSuccessResponse(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "226 Transfer complete") ||
		strings.Contains(msg, "227 Entering Passive Mode") ||
		strings.Contains(msg, "257") ||
		strings.Contains(msg, "150 Opening") ||
		strings.Contains(msg, "150 Ok to send data") {
		return nil
	}
	return err
}

type fileReader struct {
	path     string
	conn     *ftp.ServerConn
	response *ftp.Response
	release  func() error
}

func (f *fileReader) Stat() (iofs.FileInfo, error) {
	// Try GetEntry first
	entry, err := f.conn.GetEntry(f.path)
	if err != nil {
		// Fall back to List if GetEntry fails
		dir, name := path.Split(f.path)
		if dir == "" {
			dir = "/"
		}

		entries, listErr := f.conn.List(dir)
		if listErr != nil {
			return nil, err // Return original GetEntry error
		}

		// Find the entry in the list
		for _, e := range entries {
			if e.Name == name {
				return fileInfo{e}, nil
			}
		}

		// If not found in list, return original error
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

// ReadAll downloads the complete content of the file at filePath
// with a single RETR command.
func (f *fileSystem) ReadAll(ctx context.Context, filePath string) (data []byte, err error) {
	defer f.convertResultError(&err, filePath)

	conn, filePath, release, err := f.getConn(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	response, err := conn.Retr(filePath)
	if err != nil {
		return nil, err
	}
	data, err = io.ReadAll(response)
	return data, errors.Join(err, response.Close())
}

// WriteAll writes data to the file at filePath with a single STOR command,
// creating it if it does not exist or truncating it if it does exist.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) (err error) {
	defer f.convertResultError(&err, filePath)

	conn, filePath, release, err := f.getConn(ctx, filePath)
	if err != nil {
		return err
	}
	defer release()

	return ignoreFTPSuccessResponse(conn.Stor(filePath, bytes.NewReader(data)))
}

// Append appends data to the file at filePath with a single APPE command,
// creating it if it does not exist.
func (f *fileSystem) Append(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) (err error) {
	defer f.convertResultError(&err, filePath)

	conn, filePath, release, err := f.getConn(ctx, filePath)
	if err != nil {
		return err
	}
	defer release()

	return ignoreFTPSuccessResponse(conn.Append(filePath, bytes.NewReader(data)))
}

// OpenWriter opens the file at filePath for writing, creating it if it does
// not exist or truncating it if it does exist.
//
// FTP has no random-access I/O, so the written bytes are buffered in memory
// and flushed to the server with a single STOR command when the returned
// writer is closed. For bulk data prefer WriteAll, which streams directly
// to the server without buffering the whole file in memory.
func (f *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	var buf *fsimpl.FileBuffer
	buf = fsimpl.NewFileBufferWithClose(nil, func() error {
		return f.WriteAll(context.Background(), filePath, buf.Bytes(), perm)
	})
	return buf, nil
}

// OpenAppendWriter opens the file at filePath for appending,
// creating it if it does not exist.
//
// The written bytes are buffered in memory and appended to the server file
// with a single APPE command when the returned writer is closed. Only the
// appended data is held in memory, not the whole file.
func (f *fileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	var buf *fsimpl.FileBuffer
	buf = fsimpl.NewFileBufferWithClose(nil, func() error {
		return f.Append(context.Background(), filePath, buf.Bytes(), perm)
	})
	return buf, nil
}

// OpenReadWriter opens the file at filePath for reading and writing.
//
// FTP has no random-access I/O, so the complete file is downloaded into an
// in-memory buffer that supports Read, Write and Seek. The buffer is flushed
// back to the server with a single STOR command when closed. The file must
// already exist and this is not suitable for very large files.
func (f *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (rw fs.ReadWriteSeekCloser, err error) {
	defer f.convertResultError(&err, filePath)

	data, err := f.ReadAll(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	var buf *fsimpl.FileBuffer
	buf = fsimpl.NewFileBufferWithClose(data, func() error {
		return f.WriteAll(context.Background(), filePath, buf.Bytes(), perm)
	})
	return buf, nil
}

// Move renames filePath to destPath via the FTP RNFR/RNTO commands.
//
// When filePath and destPath resolve to the same location after path
// cleaning, Move returns nil without contacting the server, matching the
// no-op behavior required by the [fs.MoveFileSystem] contract. (Many FTP
// servers would otherwise reject the rename with a "file unavailable"
// reply.)
func (f *fileSystem) Move(filePath string, destPath string) (err error) {
	defer f.convertResultError(&err, filePath)

	filePath = path.Clean(filePath)
	destPath = path.Clean(destPath)
	if filePath == destPath {
		return nil
	}
	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	return conn.Rename(filePath, destPath)
}

func (f *fileSystem) Remove(filePath string) (err error) {
	defer f.convertResultError(&err, filePath)

	conn, filePath, release, err := f.getConn(context.Background(), filePath)
	if err != nil {
		return err
	}
	defer release()

	// Try GetEntry first to determine if it's a file or directory
	entry, err := conn.GetEntry(filePath)
	if err != nil {
		// Fall back to List if GetEntry fails
		dir, name := f.SplitDirAndName(filePath)
		if dir == "" {
			dir = "/"
		}

		entries, listErr := conn.List(dir)
		if listErr != nil {
			// If we can't determine the type, try to delete as file first
			err = conn.Delete(filePath)
			if err != nil {
				// If file deletion fails, try directory deletion
				return conn.RemoveDir(filePath)
			}
			return nil
		}

		// Find the entry in the list
		for _, e := range entries {
			if e.Name == name {
				if e.Type == ftp.EntryTypeFolder {
					return conn.RemoveDir(filePath)
				}
				return conn.Delete(filePath)
			}
		}

		// If not found in list, try both deletion methods
		err = conn.Delete(filePath)
		if err != nil {
			return conn.RemoveDir(filePath)
		}
		return nil
	}

	if entry.Type == ftp.EntryTypeFolder {
		return conn.RemoveDir(filePath)
	}
	return conn.Delete(filePath)
}

// Close quits the underlying FTP connection and unregisters the file system.
// After the last reference is closed all methods return fs.ErrFileSystemClosed
// instead of dialing a new connection. Calling Close more than once, or on a
// file system that never connected, is a safe no-op.
// Close decreases the file system's reference count in the registry and only
// closes the underlying FTP connection once the last reference is released, so
// it never closes a connection another caller still holds. Calling Close on an
// already-closed file system is a safe no-op.
func (f *fileSystem) Close() error {
	if f.closed || f.conn == nil {
		return nil // already closed or never connected
	}
	if fs.Unregister(f) > 0 {
		return nil // still referenced by another caller
	}
	return f.closeConn()
}

// closeConn closes the underlying FTP connection without touching the registry.
// It is used both by Close once the last reference is released and to discard a
// redundant connection that lost the registration race in EnsureRegistered.
func (f *fileSystem) closeConn() error {
	if f.closed || f.conn == nil {
		return nil
	}
	err := f.conn.Quit()
	f.conn = nil
	f.closed = true
	return err
}

func (f *fileSystem) convertResultError(err *error, path string) {
	if err == nil || *err == nil {
		return
	}
	var e *textproto.Error
	if errors.As(*err, &e) {
		if e.Code == ftp.StatusFileUnavailable {
			*err = fs.NewErrDoesNotExist(f.JoinCleanFile(path))
			return
		}
		if e.Msg == "" {
			e.Msg = ftp.StatusText(e.Code)
			*err = e
			return
		}
	}
}
