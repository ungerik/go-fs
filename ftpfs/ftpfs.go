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
	fs.Register(&fileSystem{secure: false})
	fs.Register(&fileSystem{secure: true})
}

type fileSystem struct {
	conn   *ftp.ServerConn
	prefix string
	secure bool
}

// DialAndRegister dials a new FTP connection and registers it as file system.
//
// If hostKeyCallbackOrNil is not nil then it will be called
// during the cryptographic handshake to validate the server's host key,
// else any host key will be accepted.
func DialAndRegister(ctx context.Context, addr, user, password string) (fs.FileSystem, error) {
	addr = strings.TrimSuffix(addr, "/")

	var err error
	f := &fileSystem{
		prefix: addr,
		secure: strings.HasPrefix(addr, "ftps://"),
	}
	if f.secure {
		f.conn, err = ftp.Dial(
			strings.TrimPrefix(addr, "ftps://"),
			ftp.DialWithContext(ctx),
			ftp.DialWithTLS(&tls.Config{InsecureSkipVerify: true}),
		)
	} else {
		f.conn, err = ftp.Dial(
			strings.TrimPrefix(addr, "ftp://"),
			ftp.DialWithContext(ctx),
		)
	}
	if err != nil {
		return nil, err
	}
	err = f.conn.Login(user, password)
	if err != nil {
		return nil, errors.Join(err, f.conn.Quit())
	}

	fs.Register(f)
	return f, nil
}

func nop() error { return nil }

func (f *fileSystem) getConn(filePath string) (conn *ftp.ServerConn, clientPath string, release func() error, err error) {
	if f.conn != nil {
		return f.conn, filePath, nop, nil
	}

	// Dial with credentials from URL to create client on the fly for caller:
	url, err := url.Parse(f.URL(filePath))
	if err != nil {
		return nil, "", nop, err
	}
	username := url.User.Username()
	if username == "" {
		return nil, "", nop, fmt.Errorf("no username in %s URL: %s", f.Name(), f.URL(filePath))
	}
	password, ok := url.User.Password()
	if !ok {
		return nil, "", nop, fmt.Errorf("no password in %s URL: %s", f.Name(), f.URL(filePath))
	}
	panic("TODO" + password)
}

func (f *fileSystem) IsReadOnly() bool {
	return false
}

func (f *fileSystem) IsWriteOnly() bool {
	return false
}

func (f *fileSystem) RootDir() fs.File {
	return fs.File(f.prefix + Separator)
}

func (f *fileSystem) ID() (string, error) {
	return f.prefix, nil
}

func (f *fileSystem) Prefix() string {
	if f.prefix == "" {
		return Prefix
	}
	return f.prefix
}

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
	if f.secure {
		return PrefixTLS + cleanPath
	}
	return Prefix + cleanPath
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	if f.secure {
		return fs.File(PrefixTLS + f.JoinCleanPath(uriParts...))
	}
	return fs.File(Prefix + f.JoinCleanPath(uriParts...))
}

func (f *fileSystem) JoinCleanPath(uriParts ...string) string {
	if f.secure {
		return fsimpl.JoinCleanPath(uriParts, PrefixTLS, Separator)
	}
	return fsimpl.JoinCleanPath(uriParts, Prefix, Separator)
}

func (f *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), Separator)
}

func (f *fileSystem) Separator() string { return Separator }

func (f *fileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, Prefix)
}

func (f *fileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return Prefix + strings.TrimPrefix(filePath, Separator)
}

func (f *fileSystem) SplitDirAndName(filePath string) (dir, name string) {
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
	conn, filePath, release, err := f.getConn(filePath)
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
	conn, filePath, release, err := f.getConn(filePath)
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
	conn, dirPath, release, err := f.getConn(dirPath)
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
	conn, dirPath, release, err := f.getConn(dirPath)
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
	conn, filePath, release, err := f.getConn(filePath)
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
	conn, filePath, release, err := f.getConn(filePath)
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
	conn, filePath, release, err := f.getConn(filePath)
	if err != nil {
		return err
	}
	defer release()

	return conn.Rename(filePath, destPath)
}

func (f *fileSystem) Remove(filePath string) error {
	conn, filePath, release, err := f.getConn(filePath)
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
	fs.Unregister(f)
	err := f.conn.Quit()
	f.conn = nil
	return err
}
