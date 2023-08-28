// Package ftpfs implements a (S)FTP client file system.
package ftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"strings"

	"github.com/pkg/sftp"
	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"golang.org/x/crypto/ssh"
)

const (
	Prefix    = "sftp://"
	Separator = "/"
)

var (
	_ fs.FileSystem = new(SFTPFileSystem)
)

type SFTPFileSystem struct {
	client *sftp.Client
	prefix string
}

func Dial(addr, user, password string) (*SFTPFileSystem, error) {
	addr = strings.TrimSuffix(strings.TrimPrefix(addr, "sftp://"), "/")

	var hostKey ssh.PublicKey
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
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

func (f *SFTPFileSystem) IsReadOnly() bool {
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
	return strings.Split(strings.TrimPrefix(filePath, Prefix), Separator)
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

func (f *SFTPFileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	return f.client.Stat(filePath)
}

func (f *SFTPFileSystem) IsHidden(filePath string) bool       { return false }
func (f *SFTPFileSystem) IsSymbolicLink(filePath string) bool { return false }

func (f *SFTPFileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(fs.FileInfo) error, patterns []string) error {
	return fmt.Errorf("HTTPFileSystem.ListDirInfo: %w", errors.ErrUnsupported)
}

func (f *SFTPFileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(fs.FileInfo) error, patterns []string) error {
	return fmt.Errorf("HTTPFileSystem.ListDirInfoRecursive: %w", errors.ErrUnsupported)
}

func (f *SFTPFileSystem) ListDirMax(ctx context.Context, dirPath string, max int, patterns []string) (files []fs.File, err error) {
	if max == 0 {
		return nil, nil
	}
	infos, err := f.client.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		files = append(files, f.JoinCleanFile(dirPath, info.Name()))
		if max > 0 && len(files) == max {
			break
		}
	}
	return files, nil
}

func (f *SFTPFileSystem) OpenReader(filePath string) (reader iofs.File, err error) {
	return f.client.Open(filePath)
}

func (f *SFTPFileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (f *SFTPFileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	return f.client.Mkdir(dirPath)
}

func (f *SFTPFileSystem) OpenWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	return f.client.Open(filePath)
}

func (f *SFTPFileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	file, err := f.client.Open(filePath)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	_, err = file.Seek(info.Size(), io.SeekStart)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *SFTPFileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return f.client.Open(filePath)
}

func (f *SFTPFileSystem) Truncate(filePath string, size int64) error {
	file, err := f.client.Open(filePath)
	if err != nil {
		return err
	}
	return errors.Join(
		file.Truncate(size),
		file.Close(),
	)
}

func (f *SFTPFileSystem) Move(filePath string, destPath string) error {
	return f.client.Rename(filePath, destPath)
}

func (f *SFTPFileSystem) Remove(filePath string) error {
	return f.client.Remove(filePath)
}
