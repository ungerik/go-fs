// Package sftpfs implements a SFTP client file system.
package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
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
	fs.Register(new(SFTPFileSystem))
}

type SFTPFileSystem struct {
	client *sftp.Client
	prefix string
}

// Dial a new SFTP connection and register it as file system.
//
// If hostKeyCallbackOrNil is not nil then it will be called
// during the cryptographic handshake to validate the server's host key,
// else any host key will be accepted.
func Dial(addr, user, password string, hostKeyCallbackOrNil ssh.HostKeyCallback) (*SFTPFileSystem, error) {
	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "sftp://"), "/")

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: hostKeyCallbackOrNil,
	}
	if config.HostKeyCallback == nil {
		config.HostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, err
	}
	return New(addr, conn)
}

func New(addr string, conn *ssh.Client) (*SFTPFileSystem, error) {
	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "sftp://"), "/")

	client, err := sftp.NewClient(conn)
	if err != nil {
		return nil, err
	}
	fileSystem := &SFTPFileSystem{
		client: client,
		prefix: "sftp://" + addr,
	}
	fs.Register(fileSystem)
	return fileSystem, nil
}

func nop() error { return nil }

func (f *SFTPFileSystem) getClient(filePath string) (client *sftp.Client, clientPath string, release func() error, err error) {
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

func (f *SFTPFileSystem) IsReadOnly() bool {
	// f.client.
	return false // TODO
}

func (f *SFTPFileSystem) IsWriteOnly() bool {
	return false
}

func (f *SFTPFileSystem) Close() error {
	fs.Unregister(f)
	return f.client.Close()
}

func (f *SFTPFileSystem) RootDir() fs.File {
	return fs.File(f.prefix + Separator)
}

func (f *SFTPFileSystem) ID() (string, error) {
	return f.prefix, nil
}

func (f *SFTPFileSystem) Prefix() string {
	if f.prefix == "" {
		return Prefix
	}
	return f.prefix
}

func (f *SFTPFileSystem) Name() string {
	return "SFTP"
}

func (f *SFTPFileSystem) String() string {
	return f.prefix + " file system"
}

func (f *SFTPFileSystem) URL(cleanPath string) string {
	return Prefix + cleanPath
}

func (f *SFTPFileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(Prefix + f.JoinCleanPath(uriParts...))
}

func (f *SFTPFileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, Prefix, Separator)
}

func (f *SFTPFileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, f.Prefix(), Separator)
}

func (f *SFTPFileSystem) Separator() string { return Separator }

func (f *SFTPFileSystem) IsAbsPath(filePath string) bool {
	return strings.HasPrefix(filePath, Prefix)
}

func (f *SFTPFileSystem) AbsPath(filePath string) string {
	if f.IsAbsPath(filePath) {
		return filePath
	}
	return Prefix + strings.TrimPrefix(filePath, Separator)
}

func (f *SFTPFileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (f *SFTPFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (f *SFTPFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	client, dirPath, release, err := f.getClient(dirPath)
	if err != nil {
		return err
	}
	defer release()

	return client.Mkdir(dirPath)
}

func (f *SFTPFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return nil, err
	}
	defer release()

	return client.Stat(filePath)
}

func (f *SFTPFileSystem) IsHidden(filePath string) bool       { return false }
func (f *SFTPFileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *SFTPFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) error {
	client, dirPath, release, err := f.getClient(dirPath)
	if err != nil {
		return err
	}
	defer release()

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

func (f *SFTPFileSystem) openFile(filePath string) (*sftpFile, error) {
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

func (f *SFTPFileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	return f.openFile(filePath)
}

func (f *SFTPFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	return f.openFile(filePath)
}

func (f *SFTPFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
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

func (f *SFTPFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.openFile(filePath)
}

func (f *SFTPFileSystem) Truncate(filePath string, size int64) error {
	file, err := f.openFile(filePath)
	if err != nil {
		return err
	}
	return errors.Join(
		file.Truncate(size),
		file.Close(),
	)
}

func (f *SFTPFileSystem) Move(filePath string, destPath string) error {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Rename(filePath, destPath)
}

func (f *SFTPFileSystem) Remove(filePath string) error {
	client, filePath, release, err := f.getClient(filePath)
	if err != nil {
		return err
	}
	defer release()

	return client.Remove(filePath)
}
