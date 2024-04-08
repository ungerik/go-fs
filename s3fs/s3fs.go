package s3fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// TODO use multipart download/upload https://aws.github.io/aws-sdk-go-v2/docs/sdk-utilities/s3/

const (
	// Prefix os S3FileSystem URLs
	Prefix = "s3://"

	// Separator used in S3FileSystem paths
	Separator = "/"
)

var (
	// DefaultPermissions used for S3 bucket files
	DefaultPermissions = fs.UserAndGroupReadWrite
	// DefaultDirPermissions used for S3 bucket directories
	DefaultDirPermissions = fs.UserAndGroupReadWrite + fs.AllReadWrite

	// Make sure S3FileSystem implements fs.FileSystem
	_ fs.FileSystem = new(fileSystem)
)

type fileSystem struct {
	client     *s3.Client
	bucketName string
	prefix     string
	readOnly   bool
}

// NewAndRegister initializes a new S3 instance + session and returns a fs.FileSystem
// implementation that contains the required settings to work with an S3 bucket.
func NewAndRegister(client *s3.Client, bucketName string, readOnly bool) fs.FileSystem {
	s3fs := &fileSystem{
		client:     client,
		bucketName: bucketName,
		prefix:     Prefix + bucketName,
		readOnly:   readOnly,
	}
	fs.Register(s3fs)
	return s3fs
}

func NewLoadDefaultConfig(ctx context.Context, bucketName string, readOnly bool) (fs.FileSystem, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg)
	return NewAndRegister(client, bucketName, readOnly), nil
}

func (s *fileSystem) ReadableWritable() (readable, writable bool) {
	return true, !s.readOnly
}

func (s *fileSystem) RootDir() fs.File {
	return fs.File(s.prefix + Separator)
}

func (s *fileSystem) ID() (string, error) {
	return s.bucketName, nil
}

func (s *fileSystem) Prefix() string {
	return s.prefix
}

func (s *fileSystem) Name() string {
	return "S3 file system for bucket: s.bucketName"
}

func (s *fileSystem) String() string {
	return s.Name() + " with prefix " + s.prefix
}

func (s *fileSystem) URL(cleanPath string) string {
	return s.prefix + cleanPath
}

func (f *fileSystem) CleanPathFromURI(uri string) string {
	return strings.TrimPrefix(uri, f.prefix)
}

func (s *fileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(s.prefix + s.JoinCleanPath(uriParts...))
}

func (s *fileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, s.prefix, Separator)
}

func (s *fileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, s.prefix, Separator)
}

func (s *fileSystem) Separator() string {
	return Separator
}

func (s *fileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

func (s *fileSystem) AbsPath(filePath string) string {
	if path.IsAbs(filePath) {
		return filePath
	}
	return Separator + filePath
}

func (s *fileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

func (*fileSystem) SplitDirAndName(filePath string) (dir, name string) {
	return fsimpl.SplitDirAndName(filePath, 0, Separator)
}

func (s *fileSystem) VolumeName(filePath string) string {
	return s.bucketName
}

func (s *fileSystem) Stat(filePath string) (iofs.FileInfo, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	out, err := s.client.HeadObject(
		context.Background(),
		&s3.HeadObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
		}
		return nil, err
	}
	return &fileInfo{
		name: path.Base(filePath),
		size: *out.ContentLength,
		time: *out.LastModified,
	}, nil
}

func (s *fileSystem) Exists(filePath string) bool {
	if filePath == "" || filePath == "/" {
		return false
	}
	_, err := s.client.HeadObject(
		context.Background(),
		&s3.HeadObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	return err == nil
}

// IsHidden returns true if the given file path is not empty and starts with a
// dot. There are no real "hidden" files in S3 buckets, but since dot prefixes
// are the general convention to determine which directories/files are hidden
// and which are not, the function behaves this way.
func (s *fileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

func (s *fileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (s *fileSystem) listDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fs.NewErrUnsupported(s, "listDirInfo")

	// if len(dirPath) > 1 && strings.HasPrefix(dirPath, "/") {
	// 	dirPath = dirPath[1:]
	// }
	// if !strings.HasSuffix(dirPath, "/") {
	// 	dirPath += "/"
	// }
	// info, err := s.Stat(dirPath)
	// if err != nil {
	// 	return err
	// }
	// if !info.IsDir() {
	// 	return fs.NewErrIsNotDirectory(fs.File(dirPath))
	// }

	// // We only need to set the prefix if we're listing the objects in any other
	// // directory than the root directory. For the root dir, the StartAfter param
	// // suffices.
	// var prefix string
	// if dirPath != "/" {
	// 	prefix = dirPath
	// }
	// out, err := s.client.ListObjectsV2WithContext(
	// 	ctx,
	// 	&s3.ListObjectsV2Input{
	// 		Bucket:     &s.bucketName,
	// 		Prefix:     &prefix,
	// 		StartAfter: &dirPath,
	// 	},
	// )
	// for _, c := range out.Contents {
	// 	if ctx.Err() != nil {
	// 		return ctx.Err()
	// 	}

	// 	// Determine the number of slashes we allow in the path. This is only
	// 	// used if the recursive argument is set to false because then we want
	// 	// to filter the results.
	// 	// If the contents of 'test/test/' are to be queried, we need to allow
	// 	// more slashes than we would have to with '/' or 'test/'.
	// 	var nos int
	// 	if dirPath == "/" {
	// 		nos = 1
	// 	} else {
	// 		nos = 1 + strings.Count(dirPath, "/")
	// 	}
	// 	var isNestedObject bool
	// 	// Different rules apply to files than to directories.
	// 	// e.g.:
	// 	//		key:			slashes			ends with slash
	// 	// 		test/ 			1 				true
	// 	// 		test/doc.pdf 	1 				false
	// 	// These would both only have 1 occurrence of the forward slash.
	// 	// We want to allow the directory in this case since it is a direct
	// 	// child of the target directory. We don't want to allow the file.
	// 	if *c.Size == 0 { // Directory
	// 		isNestedObject = strings.Count(*c.Key, "/") > nos
	// 	} else { // File
	// 		isNestedObject = strings.Count(*c.Key, "/") >= nos
	// 	}
	// 	if !recursive && isNestedObject {
	// 		continue
	// 	}
	// 	f := fs.File(*c.Key)
	// 	fi := s.Info(f.Name())
	// 	callback(f, fi)
	// }
	// return nil
}

func (s *fileSystem) ListDirInfo(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return s.listDirInfo(ctx, dirPath, callback, patterns, false)
}

func (s *fileSystem) ListDirInfoRecursive(ctx context.Context, dirPath string, callback func(*fs.FileInfo) error, patterns []string) (err error) {
	return s.listDirInfo(ctx, dirPath, callback, patterns, true)
}

func (s *fileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if s.Exists(filePath) {
		return nil // TODO is this OK, can we change the modified time?
	}
	return s.WriteAll(context.Background(), filePath, make([]byte, 0), perm)
}

func (s *fileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	if dirPath == "" {
		return fs.ErrEmptyPath
	}
	if dirPath == "/" {
		return nil
	}
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	// Touch pretty much does what we need. In order to create a "directory"
	// in the S3 bucket, we need to pass a key that ends on '/' and has no data.
	// We add the slash above and Touch writes an object with no data.
	return s.Touch(dirPath, perm)
}

func (s *fileSystem) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	out, err := s.client.GetObject(
		ctx,
		&s3.GetObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
		}
		return nil, err
	}
	defer out.Body.Close()

	data := make([]byte, int(*out.ContentLength))
	n, err := out.Body.Read(data)
	if err != nil {
		return nil, err
	}
	if n < int(*out.ContentLength) {
		return nil, fmt.Errorf("read %d bytes from body but content-length is %d", n, out.ContentLength)
	}
	return data, nil
}

func (s *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm []fs.Permissions) error {
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	_, err := s.client.PutObject(
		ctx,
		&s3.PutObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
			Body:   bytes.NewReader(data),
		},
	)
	return err
}

func (s *fileSystem) OpenReader(filePath string) (iofs.File, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	out, err := s.client.GetObject(
		context.Background(),
		&s3.GetObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		},
	)
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return nil, fs.NewErrDoesNotExist(fs.File(s.prefix + filePath))
		}
		return nil, err
	}
	defer out.Body.Close()

	data := make([]byte, int(*out.ContentLength))
	n, err := out.Body.Read(data)
	if err != nil {
		return nil, err
	}
	if n < int(*out.ContentLength) {
		return nil, fmt.Errorf("read %d bytes from body but content-length is %d", n, out.ContentLength)
	}

	info := &fileInfo{
		name: path.Base(filePath),
		size: *out.ContentLength,
		time: *out.LastModified,
	}
	return fsimpl.NewReadonlyFileBuffer(data, info), nil
}

func (s *fileSystem) OpenWriter(filePath string, perm []fs.Permissions) (fs.WriteCloser, error) {
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	if s.readOnly {
		return nil, fs.ErrReadOnlyFileSystem
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return s.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), perm)
	})
	return fileBuffer, nil
}

func (s *fileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	return s.openFileBuffer(filePath)
}

func (s *fileSystem) openFileBuffer(filePath string) (fileBuffer *fsimpl.FileBuffer, err error) {
	if s.readOnly {
		return nil, fs.ErrReadOnlyFileSystem
	}
	current, err := s.ReadAll(context.Background(), filePath)
	if err != nil {
		return nil, err
	}
	fileBuffer = fsimpl.NewFileBufferWithClose(current, func() error {
		return s.WriteAll(context.Background(), filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

func (s *fileSystem) CopyFile(ctx context.Context, srcFile string, destFile string, buf *[]byte) error {
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	if srcFile == "" || destFile == "" {
		return fs.ErrEmptyPath
	}
	srcFile = s.bucketName + "/" + srcFile
	_, err := s.client.CopyObject(
		ctx, &s3.CopyObjectInput{
			Bucket:     &s.bucketName,
			CopySource: &srcFile,
			Key:        &destFile,
		},
	)
	var notFound *types.NotFound
	if err != nil && errors.As(err, &notFound) {
		err = fs.NewErrDoesNotExist(fs.File(s.prefix + srcFile))
	}
	return err
}

func (s *fileSystem) Remove(filePath string) error {
	if s.readOnly {
		return fs.ErrReadOnlyFileSystem
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	_, err := s.client.DeleteObject(
		context.Background(),
		&s3.DeleteObjectInput{
			Bucket: &s.bucketName,
			Key:    &filePath,
		})
	return err
}

func (s *fileSystem) Watch(filePath string, onEvent func(fs.File, fs.Event)) (cancel func() error, err error) {
	// https://stackoverflow.com/questions/18049717/waituntilobjectexists-amazon-s3-php-sdk-method-exactly-how-does-it-work
	// s.client.WaitUntilObjectExists
	// s.client.WaitUntilObjectNotExists
	/*retChan := make(chan fs.WatchEvent)
	go func() {
		err := s.client.WaitUntilObjectExists(&s3.HeadObjectInput{
			Bucket: &s.bucketName),
			Key:    &filePath),
		})
		if err != nil {
			retChan <- fs.WatchEvent{
				Err: err,
			}
		}
	}()*/
	//return retChan, nil
	return nil, errors.ErrUnsupported
}

func (s *fileSystem) Close() error {
	if s.client == nil {
		return nil // already closed
	}
	fs.Unregister(s)
	s.client = nil
	return nil
}
