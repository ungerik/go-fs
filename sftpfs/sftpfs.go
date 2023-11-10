// Package sftpfs implements a SFTP client file system.
package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net"
	"net/url"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

const (
	Prefix    = "sftp://"
	Separator = "/"
)

func init() {
	// Register with prefix sftp:// for URLs with
	// sftp://username:password@host:port schema.
	fs.Register(new(fileSystem))
}

type fileSystem struct {
	client *sftp.Client
	prefix string
}

// DialAndRegister a new SFTP connection and register it as file system.
//
// If hostKeyCallbackOrNil is not nil then it will be called
// during the cryptographic handshake to validate the server's host key,
// else any host key will be accepted.
func DialAndRegister(ctx context.Context, address, username, password string, hostKeyCallbackOrNil ssh.HostKeyCallback) (fs.FileSystem, error) {
	if !strings.HasPrefix(address, "sftp://") {
		if strings.Contains(address, "://") {
			return nil, fmt.Errorf("URL must start with sftp:// but got %s", address)
		}
		address = "sftp://" + address
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "sftp" {
		return nil, fmt.Errorf("URL scheme must be sftp:// but got %s", u.Scheme)
	}
	if u.Port() == "" {
		u.Host += ":22"
	}
	if username == "" {
		username = u.User.Username()
	}
	if password == "" {
		password, _ = u.User.Password()
	}

	prefix := "sftp://" + url.User(username).String() + "@" + u.Host

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: hostKeyCallbackOrNil,
	}
	if config.HostKeyCallback == nil {
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, u.Host, config)
	if err != nil {
		return nil, err
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	// conn, err := ssh.Dial("tcp", u.Host, config)
	// if err != nil {
	// 	return nil, err
	// }
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, err
	}
	fileSystem := &fileSystem{
		client: client,
		prefix: prefix,
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

// func NewFileSystem(addr string, conn *ssh.Client) (*FileSystem, error) {
// 	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "sftp://"), "/")

// 	client, err := sftp.NewClient(conn)
// 	if err != nil {
// 		return nil, err
// 	}
// 	fileSystem := &FileSystem{
// 		client: client,
// 		prefix: "sftp://" + addr,
// 	}
// 	fs.Register(fileSystem)
// 	return fileSystem, nil
// }

func nop() error { return nil }

func (f *fileSystem) getClient(filePath string) (client *sftp.Client, clientPath string, release func() error, err error) {
	if f.client != nil {
		return f.client, filePath, nop, nil
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
	config := &ssh.ClientConfig{
		User: url.User.Username(),
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	conn, err := ssh.Dial("tcp", url.Host, config)
	if err != nil {
		return nil, "", nop, err
	}
	client, err = sftp.NewClient(conn)
	if err != nil {
		return nil, "", func() error { return conn.Close() }, err
	}
	return client, url.Path, func() error { return client.Close() }, nil
}

func (f *fileSystem) IsReadOnly() bool {
	// f.client.
	return false // TODO
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
	return "SFTP"
}

func (f *fileSystem) String() string {
	return f.prefix + " file system"
}

func (f *fileSystem) URL(cleanPath string) string {
	return Prefix + cleanPath
}

func (f *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(Prefix + f.JoinCleanPath(uriParts...))
}

func (f *fileSystem) JoinCleanPath(uriParts ...string) string {
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

func (f *fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (f *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	client, dirPath, release, err := f.getClient(dirPath)
	if err != nil {
		return err
	}
	defer release()

	return client.Mkdir(dirPath)
}

func (f *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	return client.Stat(filePath)
}

func (f *fileSystem) IsHidden(filePath string) bool       { return false }
func (f *fileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	client, dirPath, release, err := f.getClient(dirPath)
	if err != nil {
		return err
	}
	defer release()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	infos, err := client.ReadDir(dirPath)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		match, err := fsimpl.MatchAnyPattern(info.Name(), patterns)
		if err != nil {
			return err
		}
		if !match {
			continue
		}
		err = callback(fs.NewFileInfo(f.JoinCleanFile(dirPath, info.Name()), info, false))
		if err != nil {
			return err
		}
	}
	return nil
}

type sftpFile struct {
	*sftp.File
	release func() error
}

func (f *sftpFile) Close() error {
	return errors.Join(f.File.Close(), f.release())
}

func (f *fileSystem) openFile(filePath string) (*sftpFile, error) {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return nil, err
	}
	file, err := client.Open(filePath)
	if err != nil {
		return nil, errors.Join(err, release())
	}
	return &sftpFile{file, release}, nil
}

func (f *fileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	return f.openFile(filePath)
}

func (f *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	return f.openFile(filePath)
}

func (f *fileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	file, err := f.openFile(filePath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	_, err = file.Seek(info.Size(), io.SeekStart)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (f *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.openFile(filePath)
}

func (f *fileSystem) Truncate(filePath string, size int64) error {
	file, err := f.openFile(filePath)
	if err != nil {
		return err
	}
	return errors.Join(
		file.Truncate(size),
		file.Close(),
	)
}

func (f *fileSystem) Move(filePath string, destPath string) error {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Rename(filePath, destPath)
}

func (f *fileSystem) Remove(filePath string) error {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Remove(filePath)
}

func (f *fileSystem) Close() error {
	fs.Unregister(f)
	return f.client.Close()
}
