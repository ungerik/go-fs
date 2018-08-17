package s3fs

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

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
)

// S3FileSystem implements fs.FileSystem for an S3 bucket.
type S3FileSystem struct {
	bucketName    string
	prefix        string
	s3Client      *s3.S3
	fileInfoCache *fs.FileInfoCache
}

// New initializes a new S3 instance + session and returns an S3FileSystem
// instance that contains the required settings to work with an S3 bucket.
func New(bucketName string, region Region, cacheTimeout time.Duration) *S3FileSystem {
	session := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(region.String()),
	}))
	s3fs := &S3FileSystem{
		bucketName:    bucketName,
		prefix:        Prefix + bucketName,
		s3Client:      s3.New(session),
		fileInfoCache: fs.NewFileInfoCache(cacheTimeout),
	}
	fs.Register(s3fs)
	return s3fs
}

func (s3fs *S3FileSystem) wrapErrNotExist(filePath string, err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "path/not_found/") {
		return fs.NewErrDoesNotExist(s3fs.File(filePath))
	}
	return err
}

// Close removes the file system from the registry.
func (s3fs *S3FileSystem) Close() error {
	fs.Unregister(s3fs)
	return nil
}

// IsReadOnly returns a boolean value indicating whether the file system is
// read-only.
func (s3fs *S3FileSystem) IsReadOnly() bool {
	return false
}

// IsWriteOnly returns a boolean value indicating whether the file system is
// write-only.
func (s3fs *S3FileSystem) IsWriteOnly() bool {
	return false
}

// Root returns the root path of the file system (which includes the file
// system's prefix).
// e.g.: s3://<bucket name>/
func (s3fs *S3FileSystem) Root() fs.File {
	return fs.File(s3fs.prefix + Separator)
}

// ID returns the file system's unique bucket name.
func (s3fs *S3FileSystem) ID() (string, error) {
	return s3fs.bucketName, nil
}

// Prefix returns the "s3://" prefix + the name of the bucket that's
// been configured for this file system.
func (s3fs *S3FileSystem) Prefix() string {
	return s3fs.prefix
}

// Name returns the file system's name.
func (s3fs *S3FileSystem) Name() string {
	return s3fs.bucketName + " S3 bucket file system"
}

// String returns a string that described the file system.
func (s3fs *S3FileSystem) String() string {
	return s3fs.Name() + " with prefix " + s3fs.Prefix()
}

// File creates and returns a file for the S3 file system.
func (s3fs *S3FileSystem) File(filePath string) fs.File {
	return s3fs.JoinCleanFile(filePath)
}

// JoinCleanFile creates and returns a file for this file system with the
// prefix + a clean joined path.
func (s3fs *S3FileSystem) JoinCleanFile(uriParts ...string) fs.File {
	return fs.File(s3fs.Prefix() + s3fs.JoinCleanPath(uriParts...))
}

// URL returns the URL for the given file.
func (s3fs *S3FileSystem) URL(cleanPath string) string {
	return s3fs.Prefix() + cleanPath
}

// JoinCleanPath returns joined path without the file system's prefix.
// Also, if the path does not start with a forward slash, the function
// will attach one to the start.
func (s3fs *S3FileSystem) JoinCleanPath(uriParts ...string) string {
	return fsimpl.JoinCleanPath(uriParts, s3fs.Prefix(), Separator)
}

// SplitPath returns a string array containing the parts of the filePath
// argument. The file system's prefix and leading and trailing separators are
// removed.
func (s3fs *S3FileSystem) SplitPath(filePath string) []string {
	return fsimpl.SplitPath(filePath, s3fs.prefix, Separator)
}

// Separator returns the file system's separator (/).
func (s3fs *S3FileSystem) Separator() string {
	return Separator
}

// MatchAnyPattern returns whether the given name mathces any of the patterns.
func (s3fs *S3FileSystem) MatchAnyPattern(name string, patterns []string) (bool, error) {
	return fsimpl.MatchAnyPattern(name, patterns)
}

// DirAndName returns the directory and name portions of the given file path.
func (s3fs *S3FileSystem) DirAndName(filePath string) (dir, name string) {
	return fsimpl.DirAndName(filePath, 0, Separator)
}

// VolumeName returns nothing for the S3 file system since there are no volume
// names in S3 buckets.
func (s3fs *S3FileSystem) VolumeName(filePath string) string {
	return ""
}

// IsAbsPath checks if a file path is an absolute path
func (s3fs *S3FileSystem) IsAbsPath(filePath string) bool {
	return path.IsAbs(filePath)
}

// AbsPath returns the absolute version of a file path
func (s3fs *S3FileSystem) AbsPath(filePath string) string {
	// If the path already is an absolute path or even starts with the
	// entire prefix, we can just return what we got.
	isAbs := s3fs.IsAbsPath(filePath)
	isAbsWithPrefix := (len(filePath) >= len(s3fs.Prefix()) && filePath[0:len(s3fs.Prefix())] == s3fs.Prefix())
	if isAbs || isAbsWithPrefix {
		return filePath
	}
	return Separator + filePath
}

// Stat returns a FileInfo instance for the file under the given file path.
// If the file does not exist in the S3 bucket, an empty FileInfo instance
// will be returned.
func (s3fs *S3FileSystem) Stat(filePath string) (info fs.FileInfo) {
	if filePath == "/" {
		info.Exists = true
		info.IsRegular = true
		info.IsDir = true
		info.Permissions = DefaultPermissions
		return
	}

	if cachedInfo, ok := s3fs.fileInfoCache.Get(filePath); ok {
		return *cachedInfo
	}

	out, err := s3fs.s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s3fs.bucketName),
		Key:    aws.String(filePath),
	})

	if err != nil {
		s3fs.fileInfoCache.Delete(filePath)
		return fs.FileInfo{}
	}
	return objectOutputToFileInfo(filePath, out)
}

func objectOutputToFileInfo(key string, output *s3.GetObjectOutput) (info fs.FileInfo) {
	info.Name = key
	info.Exists = true
	info.IsRegular = true
	info.IsDir = *output.ContentType == "application/x-directory" || key[len(key)-1] == '/'
	info.IsHidden = len(key) > 0 && key[0] == '.'
	info.Size = *output.ContentLength
	info.ModTime = *output.LastModified
	if info.IsDir {
		info.Permissions = DefaultDirPermissions
	} else {
		info.Permissions = DefaultPermissions
	}
	h, err := fsimpl.ContentHash(output.Body)
	if err == nil {
		info.ContentHash = h
	}
	return
}

// IsHidden returns true if the given file path is not empty and starts with a
// dot. There are no real "hidden" files in S3 buckets, but since dot prefixes
// are the general convention to determine which directories/files are hidden
// and which are not, the function behaves this way.
func (s3fs *S3FileSystem) IsHidden(filePath string) bool {
	name := path.Base(filePath)
	return len(name) > 0 && name[0] == '.'
}

// IsSymbolicLink always returns false for the S3 file system since there are no
// symbolic links in S3 buckets.
func (s3fs *S3FileSystem) IsSymbolicLink(filePath string) bool {
	return false
}

func (s3fs *S3FileSystem) listDirInfo(dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string, recursive bool) (err error) {
	if len(dirPath) > 1 && strings.HasPrefix(dirPath, "/") {
		dirPath = dirPath[1:]
	}
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	info := s3fs.Stat(dirPath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(s3fs.File(dirPath))
	}
	if !info.IsDir {
		return fs.NewErrIsNotDirectory(s3fs.File(dirPath))
	}

	// We only need to set the prefix if we're listing the objects in any other
	// directory than the root directory. For the root dir, the StartAfter param
	// suffices.
	var prefix string
	if dirPath != "/" {
		prefix = dirPath
	}
	out, err := s3fs.s3Client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket:     aws.String(s3fs.bucketName),
		Prefix:     aws.String(prefix),
		StartAfter: aws.String(dirPath),
	})
	for _, c := range out.Contents {
		// Determine the number of slashes we allow in the path. This is only
		// used if the recursive argument is set to false because then we want
		// to filter the results.
		// If the contents of 'test/test/' are to be queried, we need to allow
		// more slashes than we would have to with '/' or 'test/'.
		var nos int
		if dirPath == "/" {
			nos = 1
		} else {
			nos = 1 + strings.Count(dirPath, "/")
		}
		var isNestedObject bool
		// Different rules apply to files than to directories.
		// e.g.:
		//		key:			slashes			ends with slash
		// 		test/ 			1 				true
		// 		test/doc.pdf 	1 				false
		// These would both only have 1 occurrence of the forward slash.
		// We want to allow the directory in this case since it is a direct
		// child of the target directory. We don't want to allow the file.
		if *c.Size == 0 { // Directory
			isNestedObject = strings.Count(*c.Key, "/") > nos
		} else { // File
			isNestedObject = strings.Count(*c.Key, "/") >= nos
		}
		if !recursive && isNestedObject {
			continue
		}
		f := fs.File(*c.Key)
		fi := s3fs.Stat(f.Name())
		callback(f, fi)
	}
	return nil
}

// ListDirInfo lists all objects in the given directory and their infos.
func (s3fs *S3FileSystem) ListDirInfo(dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	return s3fs.listDirInfo(dirPath, callback, patterns, false)
}

// ListDirInfoRecursive lists all objects in the given directory recursively.
func (s3fs *S3FileSystem) ListDirInfoRecursive(dirPath string, callback func(fs.File, fs.FileInfo) error, patterns []string) (err error) {
	return s3fs.listDirInfo(dirPath, callback, patterns, true)
}

// ListDirMax lists a max objects in the dirPath.
func (s3fs *S3FileSystem) ListDirMax(dirPath string, max int, patterns []string) (files []fs.File, err error) {
	return fs.ListDirMaxImpl(max, func(callback func(fs.File) error) error {
		return s3fs.ListDirInfo(dirPath, fs.FileCallback(callback).FileInfoCallback, patterns)
	})
}

// SetPermissions does not do anything. Setting permissions is not yet
// implemented.
// https://docs.aws.amazon.com/sdk-for-java/v1/developer-guide/examples-s3-access-permissions.html
func (s3fs *S3FileSystem) SetPermissions(filePath string, perm fs.Permissions) error {
	return errors.New("SetPermissions not possible on S3 buckets")
}

// User is not implemented for S3 file system.
func (s3fs *S3FileSystem) User(filePath string) string {
	return ""
}

// SetUser is not possible for S3 file  system.
func (s3fs *S3FileSystem) SetUser(filePath string, user string) error {
	return errors.New("SetUser not possible on S3 buckets")
}

// Group is not implemented for S3 file system.
func (s3fs *S3FileSystem) Group(filePath string) string {
	return ""
}

// SetGroup is not implemented for S3 file system.
func (s3fs *S3FileSystem) SetGroup(filePath string, group string) error {
	return errors.New("SetGroup not possible on S3 buckets")
}

// Touch creates a file in the S3 bucket (no data).
func (s3fs *S3FileSystem) Touch(filePath string, perm []fs.Permissions) error {
	if s3fs.Stat(filePath).Exists {
		return errors.New("Touch can't recreate file on S3 buckets")
	}
	return s3fs.WriteAll(filePath, nil, perm)
}

// MakeDir creates a directory in the S3 bucket.
func (s3fs *S3FileSystem) MakeDir(dirPath string, perm []fs.Permissions) error {
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	// Touch pretty much does what we need. In order to create a "directory"
	// in the S3 bucket, we need to pass a key that ends on '/' and has no data.
	// We add the slash above and Touch writes an object with no data.
	return s3fs.Touch(dirPath, perm)
}

// ReadAll returns a byte array containing all data of an object.
func (s3fs *S3FileSystem) ReadAll(filePath string) ([]byte, error) {
	out, err := s3fs.s3Client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s3fs.bucketName),
		Key:    aws.String(filePath),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return ioutil.ReadAll(out.Body)
}

// WriteAll writes data to an object at filePath.
func (s3fs *S3FileSystem) WriteAll(filePath string, data []byte, perm []fs.Permissions) error {
	_, err := s3fs.s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(s3fs.bucketName),
		Key:    aws.String(filePath),
		Body:   bytes.NewReader(data),
	})
	return s3fs.wrapErrNotExist(filePath, err)
}

// Append appends data to the object at filePath.
func (s3fs *S3FileSystem) Append(filePath string, data []byte, perm []fs.Permissions) error {
	writer, err := s3fs.OpenAppendWriter(filePath, perm)
	if err != nil {
		return err
	}
	defer writer.Close()
	n, err := writer.Write(data)
	if err == nil && n < len(data) {
		return io.ErrShortWrite
	}
	return err
}

// OpenAppendWriter returns a WriteCloser that appends to the existing object
// when calling .Write.
func (s3fs *S3FileSystem) OpenAppendWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	data, err := s3fs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(data, func() error {
		return s3fs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// OpenReader returns a ReadCloser for the data in the object at filePath.
func (s3fs *S3FileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	data, err := s3fs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	return fsimpl.NewReadonlyFileBuffer(data), nil
}

// OpenWriter returns a WriteCloser for the object at filePath.
func (s3fs *S3FileSystem) OpenWriter(filePath string, perm []fs.Permissions) (io.WriteCloser, error) {
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(nil, func() error {
		return s3fs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// OpenReadWriter opens a ReadWriteSeekCloser the the object at filePath.
func (s3fs *S3FileSystem) OpenReadWriter(filePath string, perm []fs.Permissions) (fs.ReadWriteSeekCloser, error) {
	data, err := s3fs.ReadAll(filePath)
	if err != nil {
		return nil, err
	}
	var fileBuffer *fsimpl.FileBuffer
	fileBuffer = fsimpl.NewFileBufferWithClose(data, func() error {
		return s3fs.WriteAll(filePath, fileBuffer.Bytes(), nil)
	})
	return fileBuffer, nil
}

// Watch is not supported yet for S3.
func (s3fs *S3FileSystem) Watch(filePath string) (<-chan fs.WatchEvent, error) {
	// https://stackoverflow.com/questions/18049717/waituntilobjectexists-amazon-s3-php-sdk-method-exactly-how-does-it-work
	// s3fs.s3Client.WaitUntilObjectExists
	// s3fs.s3Client.WaitUntilObjectNotExists
	/*retChan := make(chan fs.WatchEvent)
	go func() {
		err := s3fs.s3Client.WaitUntilObjectExists(&s3.HeadObjectInput{
			Bucket: aws.String(s3fs.bucketName),
			Key:    aws.String(filePath),
		})
		if err != nil {
			retChan <- fs.WatchEvent{
				Err: err,
			}
		}
	}()*/
	//return retChan, nil
	return nil, fs.ErrFileWatchNotSupported
}

// Truncate shortens an object's data to size (number of bytes).
func (s3fs *S3FileSystem) Truncate(filePath string, size int64) error {
	info := s3fs.Stat(filePath)
	if !info.Exists {
		return fs.NewErrDoesNotExist(s3fs.File(filePath))
	}
	if info.IsDir {
		return fs.NewErrIsDirectory(s3fs.File(filePath))
	}
	if info.Size <= size {
		return nil
	}

	data, err := s3fs.ReadAll(filePath)
	if err != nil {
		return s3fs.wrapErrNotExist(filePath, err)
	}
	if int64(len(data)) <= size {
		return nil
	}
	return s3fs.WriteAll(filePath, data[:size], []fs.Permissions{info.Permissions})
}

// CopyFile does what its name suggests.
func (s3fs *S3FileSystem) CopyFile(srcFile string, destFile string, buf *[]byte) error {
	_, err := s3fs.s3Client.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(s3fs.bucketName),
		CopySource: aws.String(s3fs.bucketName + "/" + srcFile),
		Key:        aws.String(destFile),
	})
	return s3fs.wrapErrNotExist(srcFile, err)
}

// Rename does what its name suggests. Internally uses move.
func (s3fs *S3FileSystem) Rename(filePath string, newName string) error {
	if strings.ContainsAny(newName, "/\\") {
		return errors.New("newName for Rename() contains path separators: " + newName)
	}
	newPath := filepath.Join(filepath.Dir(filePath), newName)
	return s3fs.Move(filePath, newPath)
}

// Move "moves" a file. S3 does not have an atomic move / rename operation, so
// the function simply copies the object to destPath before deleting the old
// object at filePath.
func (s3fs *S3FileSystem) Move(filePath string, destPath string) error {
	if filePath[0] == '/' {
		filePath = filePath[1:]
	}
	err := s3fs.CopyFile(filePath, destPath, nil)
	if err != nil {
		return err
	}

	// S3 does not have an atomic move / rename operation. There are "Move"
	// operations in the .NET SDK and some other language SDKs which
	// essentially do exactly the same thing (copy file, delete old file).
	_, err = s3fs.s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s3fs.bucketName),
		Key:    aws.String(filePath),
	})
	return err
}

// Remove deletes an object from the S3 bucket.
func (s3fs *S3FileSystem) Remove(filePath string) error {
	// Directories that have to be deleted have to end with a forward slash
	// since the S3 API doesn't recognize it otherwise.
	// If a "directory" (S3 does not really have directories) is to be deleted,
	// we first have to delete its content.
	if filePath[len(filePath)-1] == '/' {
		if err := s3fs.deleteDirContent(filePath); err != nil {
			return err
		}
	}
	_, err := s3fs.s3Client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s3fs.bucketName),
		Key:    aws.String(filePath),
	})
	return err
}

func (s3fs *S3FileSystem) deleteDirContent(dirPath string) error {
	var files []fs.File
	err := s3fs.ListDirInfo(dirPath, func(f fs.File, fi fs.FileInfo) error {
		if fi.IsDir {
			if err := s3fs.deleteDirContent(f.Path()); err != nil {
				return err
			}
		}
		files = append(files, f)
		return nil
	}, nil)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := s3fs.Remove(f.Path()); err != nil {
			return err
		}
	}
	return nil
}
