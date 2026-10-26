package s3fs

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
)

const (
	// S3Mock instead of MinIO: MinIO deleted its minio/minio Docker Hub
	// repository (and the quay.io one), so the image can no longer be
	// pulled. S3Mock is Adobe's Apache-2.0 S3 test double; it keeps its
	// objects in a temporary directory inside the container and accepts
	// any credentials. Pinned like in github.com/domonda/go-docdb.
	s3MockImage    = "adobe/s3mock:5.2.3"
	s3MockPort     = "9090/tcp"
	testRegion     = "us-east-1"
	testAccessKey  = "test"
	testSecretKey  = "test"
	testBucketName = "testbucket"
	testDataDir    = "testdata"

	// s3MockReadyTimeout limits the wait for S3Mock to boot,
	// it usually needs a few seconds
	s3MockReadyTimeout = 90 * time.Second
)

var (
	dockerS3Available bool
	testS3Endpoint    string
	// s3MockContainerName is unique for every test run, so that
	// test runs in other worktrees don't stop the container.
	s3MockContainerName = dockertest.ContainerName("s3fs-test-s3mock")
)

func TestMain(m *testing.M) {
	// Check if Docker is available
	if !dockertest.Available() {
		log.Println("Docker not available, skipping Docker-based S3Mock tests")
		os.Exit(m.Run())
	}

	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	dockerS3Available = setupS3MockServer(ctx)
	if !dockerS3Available {
		dockertest.SetupFailed("S3Mock")
	}

	// Run tests
	exitCode := m.Run()

	if dockerS3Available {
		log.Println("Stopping and removing Docker S3Mock test server...")
		removeContainer(s3MockContainerName)
		log.Println("Docker container cleanup complete")
	}

	os.Exit(exitCode)
}

// removeContainer stops and removes a container together with its
// anonymous volumes, ignoring errors for a container that doesn't exist.
func removeContainer(name string) {
	_ = exec.Command("docker", "rm", "--force", "--volumes", name).Run()
}

func setupS3MockServer(ctx context.Context) bool {
	containerName := s3MockContainerName

	// Docker picks a free host port, the default port 9090 is often
	// taken by the S3Mock of another project on a development machine
	log.Println("Starting Docker S3Mock container...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", containerName,
		"-p", "127.0.0.1::"+s3MockPort,
		// The objects are stored below /tmp, in memory they don't
		// depend on free space on the Docker disk and are never left behind
		"--tmpfs", "/tmp:rw,mode=1777,size=1g",
		"-e", "COM_ADOBE_TESTING_S3MOCK_STORE_REGION="+testRegion,
		s3MockImage,
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker S3Mock container: %v\nOutput: %s", err, output)
		removeContainer(containerName)
		return false
	}
	log.Printf("Started Docker container: %s", containerName)

	output, err = exec.CommandContext(ctx, "docker", "port", containerName, s3MockPort).Output()
	if err != nil {
		log.Printf("Failed to get the host port of the S3Mock container: %v", err)
		removeContainer(containerName)
		return false
	}
	// The first line is the host address, like "127.0.0.1:55001"
	hostAddr, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	testS3Endpoint = "http://" + hostAddr

	// S3Mock is a Spring Boot application that needs several seconds
	// to boot, creating the test bucket doubles as readiness check.
	// A container that exited, like S3Mock without disk space for its
	// temporary directory, fails the setup at once with its logs instead
	// of after all attempts, which took minutes with the SDK retries.
	log.Println("Waiting for S3Mock server to be ready...")
	ctx, cancel := context.WithTimeout(ctx, s3MockReadyTimeout)
	defer cancel()
	client := createS3Client(func(o *s3.Options) { o.RetryMaxAttempts = 1 })
	var testErr error
	for ctx.Err() == nil {
		if !containerRunning(ctx, containerName) {
			if ctx.Err() != nil {
				break // docker inspect timed out
			}
			log.Printf("The S3Mock container exited, its logs:\n%s", containerLogs(containerName))
			removeContainer(containerName)
			return false
		}

		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(testBucketName),
		})
		if err == nil {
			log.Printf("S3Mock server is ready and bucket '%s' created", testBucketName)
			return true
		}

		// Check if bucket already exists (which is also OK)
		_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{
			Bucket: aws.String(testBucketName),
		})
		if headErr == nil {
			log.Printf("S3Mock server is ready (bucket already exists)")
			return true
		}

		testErr = err
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}

	log.Printf("S3Mock server not ready after %s: %v\nContainer logs:\n%s", s3MockReadyTimeout, testErr, containerLogs(containerName))
	removeContainer(containerName)
	return false
}

// containerRunning reports whether the container is running,
// false if it exited or docker inspect failed.
func containerRunning(ctx context.Context, name string) bool {
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// containerLogs returns the output of the container for the log
// of a failed setup, or the error of docker logs.
func containerLogs(name string) string {
	output, err := exec.Command("docker", "logs", name).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("docker logs %s: %v\n%s", name, err, output)
	}
	return string(output)
}

// createS3Client returns a client for the S3Mock server,
// optFns change its options.
func createS3Client(optFns ...func(*s3.Options)) *s3.Client {
	return s3.New(s3.Options{
		Region: testRegion,
		// S3Mock accepts any credentials,
		// the SDK needs some to sign the requests
		Credentials: credentials.NewStaticCredentialsProvider(
			testAccessKey,
			testSecretKey,
			"",
		),
		BaseEndpoint: aws.String(testS3Endpoint),
		UsePathStyle: true, // S3Mock serves buckets as path elements
		// S3Mock doesn't return the part checksums of a multipart upload
		// that the default CRC32 checksums require for completing it
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}, optFns...)
}

// newTestFileSystem returns a registered file system for the test bucket
// that is closed at the end of the test, and the S3 client it uses.
func newTestFileSystem(t *testing.T, opts *Options) (*fileSystem, *s3.Client) {
	t.Helper()
	if !dockerS3Available {
		t.Skip("Docker S3Mock server not available")
	}
	client := createS3Client()
	f, err := New(client, testBucketName, opts)
	require.NoError(t, err)
	require.NoError(t, fs.Register(f))
	t.Cleanup(func() { _ = f.Close() })
	return f.(*fileSystem), client
}

// putObject creates an object with the S3 client, bypassing the file system.
func putObject(t *testing.T, client *s3.Client, k, content string) {
	t.Helper()
	_, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(k),
		Body:   strings.NewReader(content),
	})
	require.NoError(t, err, "PutObject(%q)", k)
}

// largeData returns n bytes of a pattern that detects
// misplaced or duplicated parts.
func largeData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

// Test_fileSystem tests the S3 filesystem implementation using S3Mock
func Test_fileSystem(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()

	// Clean up test directory before tests
	cleanupTestDir(ctx, t, client, testDataDir)

	fstest.RunConformance(t, f, fstest.Config{
		Name:    "S3",
		Prefix:  "s3://" + testBucketName,
		TestDir: testDataDir,
	})

	// Clean up after tests
	cleanupTestDir(ctx, t, client, testDataDir)
}

func cleanupTestDir(ctx context.Context, t *testing.T, client *s3.Client, dirPath string) {
	t.Helper()

	// List all objects with the test directory prefix
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(testBucketName),
		Prefix: aws.String(dirPath),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			t.Logf("Warning: Failed to list objects for cleanup: %v", err)
			return
		}

		for _, obj := range page.Contents {
			if obj.Key != nil {
				_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
					Bucket: aws.String(testBucketName),
					Key:    obj.Key,
				})
				if err != nil {
					t.Logf("Warning: Failed to delete object %s: %v", *obj.Key, err)
				}
			}
		}
	}
}

// Test_fileSystem_MultipartUploadDownload tests multipart upload/download
// for files above the thresholds of the Options
func Test_fileSystem_MultipartUploadDownload(t *testing.T) {
	const mib = 1 << 20
	s3fs, client := newTestFileSystem(t, &Options{
		MultipartUploadThreshold:   5 * mib,
		MultipartDownloadThreshold: 5 * mib,
		PartSize:                   5 * mib,
	})
	ctx := t.Context()

	// 11 MiB are uploaded and downloaded in 3 parts
	data := largeData(11 * mib)
	testFilePath := s3fs.CleanPath(testDataDir, "large-test-file.bin")
	t.Cleanup(func() { _ = s3fs.Remove(testFilePath) })

	t.Run("MultipartUpload", func(t *testing.T) {
		err := s3fs.WriteAll(ctx, testFilePath, data, 0)
		require.NoError(t, err, "WriteAll should succeed for large file")

		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(testBucketName),
			Key:    aws.String(key(testFilePath)),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(len(data)), aws.ToInt64(head.ContentLength), "object size")
		// The ETag of a multipart upload ends with the number of parts
		assert.True(t, strings.HasSuffix(strings.Trim(aws.ToString(head.ETag), `"`), "-3"), "multipart ETag %s", aws.ToString(head.ETag))
	})

	t.Run("MultipartDownload", func(t *testing.T) {
		readData, err := s3fs.ReadAll(ctx, testFilePath)
		require.NoError(t, err, "ReadAll should succeed for large file")
		require.Equal(t, len(data), len(readData), "Read data size should match")
		require.True(t, bytes.Equal(data, readData), "Read data should match written data")
	})

	t.Run("OpenReader", func(t *testing.T) {
		reader, err := s3fs.OpenReader(testFilePath)
		require.NoError(t, err, "OpenReader should succeed for large file")
		defer reader.Close()

		readData, err := io.ReadAll(reader)
		require.NoError(t, err, "Reading from reader should succeed")
		require.True(t, bytes.Equal(data, readData), "Read data should match written data")
	})
}

// TestNew checks the constructors and the metadata of the file system
// without any request to S3.
func TestNew(t *testing.T) {
	client := s3.New(s3.Options{Region: testRegion})

	_, err := New(nil, "bucket", nil)
	assert.Error(t, err, "nil client")
	_, err = New(client, "", nil)
	assert.Error(t, err, "empty bucket name")
	_, err = New(client, "a/b", nil)
	assert.Error(t, err, "bucket name with a slash")
	_, err = New(client, "bucket", &Options{PartSize: -1})
	assert.Error(t, err, "negative PartSize")
	_, err = New(client, "bucket", &Options{Concurrency: -1})
	assert.Error(t, err, "negative Concurrency")
	_, err = New(client, "bucket", &Options{Prefix: "no-scheme"})
	assert.Error(t, err, "Prefix without ://")
	_, err = New(client, "bucket", &Options{Prefix: "s3://host/bucket/"})
	assert.Error(t, err, "Prefix with a trailing slash")
	_, err = New(client, "bucket", &Options{Prefix: "backup/s3://bucket"})
	assert.Error(t, err, "Prefix with a slash before ://, a local path when unregistered")

	f, err := New(client, "bucket", nil)
	require.NoError(t, err)
	assert.Equal(t, "s3://bucket", f.Prefix())
	assert.Equal(t, "bucket", f.ID())
	assert.Equal(t, "S3", f.Name())
	assert.Equal(t, "S3 s3://bucket", f.String())
	assert.Equal(t, fs.File("s3://bucket/"), f.RootDir())
	assert.False(t, fs.IsRegistered(f), "New must not register")
	readable, writable := f.ReadableWritable()
	assert.True(t, readable)
	assert.True(t, writable)
	impl := f.(*fileSystem)
	assert.Equal(t, int64(defaultMultipartUploadThreshold), impl.uploadThreshold)
	assert.Equal(t, int64(defaultMultipartDownloadThreshold), impl.downloadThreshold)
	assert.Equal(t, defaultConcurrency, impl.concurrency)

	readOnly, err := New(client, "bucket", &Options{ReadOnly: true, Concurrency: 2})
	require.NoError(t, err)
	_, writable = readOnly.ReadableWritable()
	assert.False(t, writable)
	assert.Equal(t, 2, readOnly.(*fileSystem).concurrency)

	custom, err := New(client, "bucket", &Options{Prefix: "s3://user:secret@minio.example.com/bucket"})
	require.NoError(t, err)
	assert.Equal(t, "s3://user:secret@minio.example.com/bucket", custom.Prefix())
	assert.Equal(t, custom.Prefix(), custom.ID(), "the ID of a custom prefix is the prefix")
	assert.NotContains(t, custom.String(), "secret", "String must redact the password")
}

// TestNewAndRegister checks that a second file system for a bucket with
// the same name can't take over the prefix of the first one, which the
// v0 constructor silently did, and that Options.Prefix tells them apart.
func TestNewAndRegister(t *testing.T) {
	client := s3.New(s3.Options{Region: testRegion})
	const bucket = "s3fs-register-test-bucket"

	require.NoError(t, NewAndRegister(client, bucket, nil))
	first := fs.GetFileSystemByPrefixOrNil(Prefix + bucket)
	require.NotNil(t, first)
	t.Cleanup(func() { _ = first.Close() })

	err := NewAndRegister(client, bucket, &Options{ReadOnly: true})
	assert.ErrorIs(t, err, fs.ErrAlreadyRegistered, "a second file system for the same prefix")
	assert.Same(t, first, fs.GetFileSystemByPrefixOrNil(Prefix+bucket), "the first file system must stay registered")

	otherPrefix := "s3://minio.example.com/" + bucket
	require.NoError(t, NewAndRegister(client, bucket, &Options{Prefix: otherPrefix, ReadOnly: true}))
	other := fs.GetFileSystemByPrefixOrNil(otherPrefix)
	require.NotNil(t, other)
	t.Cleanup(func() { _ = other.Close() })
	assert.Same(t, other, fs.File(otherPrefix+"/x.txt").FileSystem())
	assert.Same(t, first, fs.File(Prefix+bucket+"/x.txt").FileSystem())

	assert.Error(t, NewAndRegister(nil, bucket, nil), "nil client")
}

// TestString_RedactsPassword checks the password redaction of String, also
// for an Options.Prefix that url.Parse rejects, like one with an invalid
// percent escape, an invalid port or a control character, which was returned
// unchanged with the password, and for a password of digits or an empty one
// with an unencoded '/' or '?', which url.Parse takes for a port.
func TestString_RedactsPassword(t *testing.T) {
	client := s3.New(s3.Options{Region: testRegion})
	for prefix, want := range map[string]string{
		"s3://user:secret@minio.example.com/bucket":      "s3://user:xxxxx@minio.example.com/bucket",
		"s3://user:secret@minio.example.com/100%":        "s3://user:xxxxx@minio.example.com/100%",
		"s3://user:secret@minio.example.com:port/bucket": "s3://user:xxxxx@minio.example.com:port/bucket",
		"s3://user:secret@minio.example.com/bucket\x7f":  "s3://user:xxxxx@minio.example.com/bucket\x7f",
		// The password ends at the last '@', it may contain '@' and '/'
		"s3://user:se@cr/et@minio.example.com/b%zz":  "s3://user:xxxxx@minio.example.com/b%zz",
		"s3://user@minio.example.com/b%zz":           "s3://user@minio.example.com/b%zz",
		"s3://minio.example.com/b%zz":                "s3://minio.example.com/b%zz",
		"s3://bucket":                                "s3://bucket",
		"s3://user:/secret@minio.example.com/bucket": "s3://user:xxxxx@minio.example.com/bucket",
		"s3://user:123?secret@minio.example.com":     "s3://user:xxxxx@minio.example.com",
	} {
		f, err := New(client, "bucket", &Options{Prefix: prefix})
		require.NoError(t, err, prefix)
		assert.Equal(t, "S3 "+want, f.String(), "String of the prefix %q", prefix)
	}
}

// TestNew_InvalidPrefixRedacted is a regression test: the error
// for an invalid Options.Prefix contained its password.
func TestNew_InvalidPrefixRedacted(t *testing.T) {
	client := s3.New(s3.Options{Region: testRegion})
	for _, prefix := range []string{
		"user:secret@minio.example.com/bucket",
		"s3://user:secret@minio.example.com/bucket/",
		"s3://user:sec/ret@minio.example.com/bucket/",
	} {
		_, err := New(client, "bucket", &Options{Prefix: prefix})
		require.Error(t, err, prefix)
		assert.NotContains(t, err.Error(), "sec", "the error for %q must not contain the password", prefix)
		assert.NotContains(t, err.Error(), "ret", "the error for %q must not contain the password", prefix)
	}
}

// httptestClient returns an S3 client without retries for a test server
// with handler that stands in for S3, so that a test runs offline.
func httptestClient(t *testing.T, handler http.HandlerFunc) *s3.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return s3.New(s3.Options{
		Region:                     testRegion,
		Credentials:                credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
		BaseEndpoint:               aws.String(server.URL),
		UsePathStyle:               true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		RetryMaxAttempts:           1,
	})
}

// TestRemoveRootOffline checks that Remove and RemoveAll refuse the root
// with an error wrapping os.ErrInvalid, like the other file systems,
// without any request: with the empty key prefix RemoveAll deleted every
// object of the bucket. The test server answers every request with an error.
func TestRemoveRootOffline(t *testing.T) {
	var requests atomic.Int32
	client := httptestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotImplemented)
	})
	f, err := New(client, "bucket", nil)
	require.NoError(t, err)

	for _, root := range []string{"/", "//"} {
		err := f.(fs.RemoveAllFileSystem).RemoveAll(t.Context(), root)
		assert.ErrorIs(t, err, os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "RemoveAll(%q)", root)
		err = f.(fs.WriteFileSystem).Remove(root)
		assert.ErrorIs(t, err, os.ErrInvalid, "Remove(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "Remove(%q)", root)
	}
	assert.Zero(t, requests.Load(), "removing the root must not send a request")
}

// TestReadAllPreallocates checks that ReadAll of an object below the
// multipart download threshold reads into a buffer of the size that
// HeadObject reported, instead of growing one from 512 bytes.
func TestReadAllPreallocates(t *testing.T) {
	data := largeData(1<<20 + 17)
	client := httptestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"etag"`)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	})
	f, err := New(client, "bucket", nil)
	require.NoError(t, err)

	read, err := f.(fs.ReadAllFileSystem).ReadAll(t.Context(), "/data.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "content")
	assert.LessOrEqual(t, cap(read), len(data)+bytes.MinRead, "the buffer must be allocated for the size of the object")
}

// TestRemoveRoot checks on S3Mock that Remove and RemoveAll of the root
// fail with an error wrapping os.ErrInvalid and keep the objects.
// RemoveAll used to delete every object of the bucket.
func TestRemoveRoot(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	dir := testDataDir + "/remove-root"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), "/"+dir) })
	putObject(t, client, dir+"/keep.txt", "keep")

	for _, root := range []string{"/", "//"} {
		assert.ErrorIs(t, f.RemoveAll(ctx, root), os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorIs(t, f.Remove(root), os.ErrInvalid, "Remove(%q)", root)
	}
	data, err := f.ReadAll(ctx, "/"+dir+"/keep.txt")
	require.NoError(t, err, "removing the root must not delete any object")
	assert.Equal(t, "keep", string(data))
}

// TestWriteWithoutListPermission checks that credentials without the
// s3:ListBucket permission can still upload: the directory check of the
// writes gets 403 from ListObjectsV2 and passes. An httptest server stands
// in for S3 and denies listings, so the test runs offline.
func TestWriteWithoutListPermission(t *testing.T) {
	var mtx sync.Mutex
	objects := map[string]string{}
	client := httptestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Has("list-type"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
		case r.Method == http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mtx.Lock()
			objects[r.URL.Path] = string(data)
			mtx.Unlock()
			w.Header().Set("ETag", `"etag"`)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	})
	f, err := New(client, "bucket", nil)
	require.NoError(t, err)
	ctx := t.Context()

	require.NoError(t, f.(fs.WriteAllFileSystem).WriteAll(ctx, "/dir/a.txt", []byte("a"), 0), "WriteAll")
	w, err := f.(fs.WriteFileSystem).OpenWriter("/dir/b.txt", 0)
	require.NoError(t, err, "OpenWriter")
	_, err = io.WriteString(w, "b")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	mtx.Lock()
	assert.Equal(t, map[string]string{"/bucket/dir/a.txt": "a", "/bucket/dir/b.txt": "b"}, objects)
	mtx.Unlock()

	// Other errors of the check are not ignored
	_, err = f.Stat("/dir")
	assert.Error(t, err)
	assert.False(t, isAccessDenied(err))
}

// listBucketResult is the XML answer of a ListObjectsV2 request.
type listBucketResult struct {
	XMLName               xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name                  string
	Prefix                string
	KeyCount              int
	MaxKeys               string
	IsTruncated           bool
	ContinuationToken     string `xml:",omitempty"`
	NextContinuationToken string `xml:",omitempty"`
	Contents              []listedObject
}

// listedObject is an object of a listBucketResult.
type listedObject struct {
	Key          string
	LastModified string
	Size         int
}

// shortPageStore stands in for an S3-compatible store with the objects
// of keys that answers ListObjectsV2 with truncated pages of fewer keys
// than MaxKeys: the pages alternate between no key and one key, so the
// first page is always empty, and the last page is an empty one that
// isn't truncated. The continuation token is the number of the next page.
//
// With brokenToken the pages from number brokenFrom on are truncated
// without a usable continuation token: "missing" omits it, "repeated"
// sends the number of the page itself, so the next request gets the
// same page again, and "cycled" alternates between the empty pages
// brokenFrom and brokenFrom+2, which must be even, so the listing never
// ends, and "endless" answers every page from brokenFrom on with an
// empty truncated page and a new token. HeadObject finds no object, DeleteObject records the key, other
// requests fail.
type shortPageStore struct {
	keys        []string // sorted
	modified    time.Time
	brokenToken string
	brokenFrom  int

	mtx      sync.Mutex
	lists    int
	listKeys []string // "max-keys" of every listing, prefixed with "+" for a follow-up page
	deleted  []string
	writes   int
}

// maxListRequests limits the ListObjectsV2 requests of a shortPageStore,
// so that a listing that doesn't end fails instead of hanging the test.
// It is above the limit of findObject for a test with a few listings.
const maxListRequests = 4 * maxListingPages

func (st *shortPageStore) handler(w http.ResponseWriter, r *http.Request) {
	st.mtx.Lock()
	defer st.mtx.Unlock()
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && query.Has("list-type"):
		// Listing below
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusNotFound)
		return
	case r.Method == http.MethodDelete:
		st.deleted = append(st.deleted, strings.TrimPrefix(r.URL.Path, "/bucket/"))
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			st.writes++
		}
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	st.lists++
	if st.lists > maxListRequests {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}

	prefix := query.Get("prefix")
	var keys []string
	for _, k := range st.keys {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	token := query.Get("continuation-token")
	if token != "" {
		st.listKeys = append(st.listKeys, "+"+query.Get("max-keys"))
	} else {
		st.listKeys = append(st.listKeys, query.Get("max-keys"))
	}
	n := 0
	if token != "" {
		var err error
		if n, err = strconv.Atoi(token); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	result := listBucketResult{
		Name:              "bucket",
		Prefix:            prefix,
		MaxKeys:           query.Get("max-keys"),
		ContinuationToken: token,
		IsTruncated:       n < 2*len(keys),
	}
	// The odd pages n have the key with index n/2
	if n%2 == 1 && n/2 < len(keys) {
		result.Contents = []listedObject{{Key: keys[n/2], LastModified: st.modified.Format(time.RFC3339)}}
		result.KeyCount = 1
	}
	switch {
	case st.brokenToken == "endless" && n >= st.brokenFrom:
		result.Contents, result.KeyCount = nil, 0
		result.IsTruncated = true
		result.NextContinuationToken = strconv.Itoa(n + 2)
	case st.brokenToken == "cycled" && n >= st.brokenFrom:
		result.IsTruncated = true
		next := st.brokenFrom + 2
		if n == next {
			next = st.brokenFrom
		}
		result.NextContinuationToken = strconv.Itoa(next)
	case st.brokenToken != "" && n >= st.brokenFrom:
		result.IsTruncated = true
		if st.brokenToken == "repeated" {
			result.NextContinuationToken = strconv.Itoa(n)
		}
	case result.IsTruncated:
		result.NextContinuationToken = strconv.Itoa(n + 1)
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(result)
}

// TestShortTruncatedListingPages checks the directory checks of Stat,
// the writes and Remove against a store that answers ListObjectsV2 with
// truncated pages of fewer keys than MaxKeys, even none: deciding from
// the first page reported directories as missing, so writes created
// objects onto them, and let Remove delete the marker of a directory
// with content. A truncated page without a new continuation token is an
// error instead of an endless or incomplete listing, also a token of an
// earlier page, which used to repeat the listing until the store failed.
func TestShortTruncatedListingPages(t *testing.T) {
	modified := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	newStore := func(t *testing.T, brokenToken string, brokenFrom int) (*shortPageStore, *fileSystem) {
		t.Helper()
		st := &shortPageStore{
			keys: []string{
				"dir/",
				"dir/file.txt",
				"empty/",
				"implicit/sub/file.txt",
			},
			modified:    modified,
			brokenToken: brokenToken,
			brokenFrom:  brokenFrom,
		}
		f, err := New(httptestClient(t, st.handler), "bucket", nil)
		require.NoError(t, err)
		return st, f.(*fileSystem)
	}

	t.Run("Stat", func(t *testing.T) {
		_, f := newStore(t, "", 0)
		info, err := f.Stat("/dir")
		require.NoError(t, err)
		assert.True(t, info.IsDir, "marker directory")
		assert.True(t, info.Modified.Equal(modified), "Modified of the marker, got %s", info.Modified)
		info, err = f.Stat("/implicit")
		require.NoError(t, err)
		assert.True(t, info.IsDir, "implicit directory")
		assert.True(t, info.Modified.IsZero(), "an implicit directory has no Modified")
		info, err = f.Stat("/empty")
		require.NoError(t, err)
		assert.True(t, info.IsDir, "empty marker directory")
		_, err = f.Stat("/missing")
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("WriteOntoDirectory", func(t *testing.T) {
		st, f := newStore(t, "", 0)
		ctx := t.Context()
		assert.ErrorIs(t, f.WriteAll(ctx, "/implicit", []byte("x"), 0), fs.ErrIsDirectory{}, "WriteAll")
		_, err := f.OpenWriter("/dir", 0)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenWriter")
		assert.NoError(t, f.Touch("/implicit", 0), "Touch of a directory does nothing")
		st.mtx.Lock()
		defer st.mtx.Unlock()
		assert.Zero(t, st.writes, "no object must be written onto a directory")
	})

	t.Run("Remove", func(t *testing.T) {
		st, f := newStore(t, "", 0)
		assert.ErrorIs(t, f.Remove("/dir"), fs.ErrDirectoryNotEmpty{}, "marker directory with content")
		assert.ErrorIs(t, f.Remove("/implicit"), fs.ErrDirectoryNotEmpty{}, "implicit directory")
		st.mtx.Lock()
		assert.Empty(t, st.deleted, "the marker of a directory with content must not be deleted")
		st.mtx.Unlock()

		require.NoError(t, f.Remove("/empty"), "empty marker directory")
		st.mtx.Lock()
		assert.Equal(t, []string{"empty/"}, st.deleted)
		st.mtx.Unlock()
	})

	// The first page asks for the keys the check needs, the pages after a
	// truncated page without a match for the most keys a page returns
	t.Run("FollowUpPagesAskForMaxKeys", func(t *testing.T) {
		st, f := newStore(t, "", 0)
		_, err := f.Stat("/dir")
		require.NoError(t, err)
		st.mtx.Lock()
		defer st.mtx.Unlock()
		require.Equal(t, []string{"1", "+1000"}, st.listKeys, "max-keys of the listings of Stat")
	})

	// Empty truncated pages with new tokens without end
	t.Run("EndlessPages", func(t *testing.T) {
		st, f := newStore(t, "endless", 0)
		_, err := f.Stat("/dir")
		assert.ErrorIs(t, err, errListingTooLong, "Stat")
		assert.ErrorIs(t, f.Remove("/dir"), errListingTooLong, "Remove")
		st.mtx.Lock()
		defer st.mtx.Unlock()
		assert.Empty(t, st.deleted, "nothing must be deleted")
		assert.LessOrEqual(t, st.lists, 2*maxListingPages+1, "every listing stops at maxListingPages")
	})

	for _, brokenToken := range []string{"missing", "repeated", "cycled"} {
		// The first page is broken, so the directory checks can't decide
		t.Run("BrokenToken/"+brokenToken+"/FirstPage", func(t *testing.T) {
			st, f := newStore(t, brokenToken, 0)
			_, err := f.Stat("/dir")
			assert.ErrorIs(t, err, errTruncatedWithoutToken, "Stat")
			assert.ErrorIs(t, f.WriteAll(t.Context(), "/dir", []byte("x"), 0), errTruncatedWithoutToken, "WriteAll")
			assert.ErrorIs(t, f.Remove("/dir"), errTruncatedWithoutToken, "Remove")
			st.mtx.Lock()
			defer st.mtx.Unlock()
			assert.Empty(t, st.deleted, "nothing must be deleted")
			assert.Zero(t, st.writes, "nothing must be written")
		})

		// Page 1 has the marker, so Stat decides before the broken page 2,
		// but the not-empty check of Remove needs the key after the marker
		t.Run("BrokenToken/"+brokenToken+"/AfterMarker", func(t *testing.T) {
			st, f := newStore(t, brokenToken, 2)
			info, err := f.Stat("/dir")
			require.NoError(t, err)
			assert.True(t, info.IsDir)
			assert.ErrorIs(t, f.Remove("/dir"), errTruncatedWithoutToken, "Remove")
			st.mtx.Lock()
			defer st.mtx.Unlock()
			assert.Empty(t, st.deleted, "the marker must not be deleted")
		})
	}
}

// TestCopySource checks the escaping of the CopySource
// of CopyObject requests, S3 decodes a "+" as space.
func TestCopySource(t *testing.T) {
	f := &fileSystem{bucketName: "bucket"}
	for k, want := range map[string]string{
		"a+b.txt":       "bucket/a%2Bb.txt",
		"c d.txt":       "bucket/c%20d.txt",
		"100%.txt":      "bucket/100%25.txt",
		"dir/ü.txt":     "bucket/dir/%C3%BC.txt",
		"x&y=z.txt":     "bucket/x&y=z.txt",
		"q?.txt":        "bucket/q%3F.txt",
		"semi;comma,":   "bucket/semi%3Bcomma%2C",
		"a/b+c/d e.txt": "bucket/a/b%2Bc/d%20e.txt",
	} {
		assert.Equal(t, want, f.copySource(k), "copySource(%q)", k)
	}
}

// TestIsPathKey checks which listed keys have a path.
func TestIsPathKey(t *testing.T) {
	for k, want := range map[string]bool{
		"x.txt":      true,
		"a/b":        true,
		"":           false,
		"/":          false,
		"/x.txt":     false,
		"x/":         false,
		"a//b":       false,
		"a/b/":       false,
		"legacy.txt": true,
	} {
		assert.Equal(t, want, isPathKey(k), "isPathKey(%q)", k)
	}
}

// TestWriteAtBuffer checks the bounds of the download buffer.
func TestWriteAtBuffer(t *testing.T) {
	buf := make(writeAtBuffer, 6)
	n, err := buf.WriteAt([]byte("def"), 3)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	_, err = buf.WriteAt([]byte("abc"), 0)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(buf))
	_, err = buf.WriteAt([]byte("xy"), 5)
	assert.Error(t, err, "beyond the end")
	_, err = buf.WriteAt([]byte("x"), -1)
	assert.Error(t, err, "negative offset")
	_, err = buf.WriteAt(make([]byte, 7), 0)
	assert.Error(t, err, "larger than the buffer")
}

// TestSpecialKeyNames checks CopyFile and Touch with characters that
// need escaping in the CopySource, a "+" used to be decoded as space.
func TestSpecialKeyNames(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	dir := "/" + testDataDir + "/special-names"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), dir) })

	for _, name := range []string{"a+b.txt", "c d.txt", "100%.txt", "ü.txt", "x&y=z.txt", "q?.txt", "semi;comma,.txt", "at@colon:$.txt"} {
		src := dir + "/" + name
		dest := dir + "/copy-" + name
		// S3Mock refuses to copy an object onto itself without user metadata,
		// AWS accepts it with the REPLACE directive that Touch uses
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:   aws.String(testBucketName),
			Key:      aws.String(key(src)),
			Body:     strings.NewReader(name),
			Metadata: map[string]string{"name": "special"},
		})
		require.NoError(t, err, "PutObject(%q)", src)
		if assert.NoError(t, f.CopyFile(ctx, src, dest), "CopyFile(%q)", src) {
			data, err := f.ReadAll(ctx, dest)
			require.NoError(t, err, "ReadAll(%q)", dest)
			assert.Equal(t, name, string(data), "content of %q", dest)
		}
		assert.NoError(t, f.Touch(src, 0), "Touch(%q)", src)
	}
	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(key(dir + "/a b.txt")),
	})
	assert.True(t, isNotFound(err), "copying a+b.txt must not create a b.txt")
}

// TestTouchKeepsMetadata checks that Touch of an existing object keeps its
// Content-Type and user metadata, which a CopyObject with the REPLACE
// directive resets unless they are passed again.
func TestTouchKeepsMetadata(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	k := testDataDir + "/touch/meta.html"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), "/"+testDataDir+"/touch") })

	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(testBucketName),
		Key:                aws.String(k),
		Body:               strings.NewReader("<p>"),
		ContentType:        aws.String("text/html"),
		ContentDisposition: aws.String(`attachment; filename="meta.html"`),
		CacheControl:       aws.String("no-cache"),
		Metadata:           map[string]string{"owner": "erik"},
	})
	require.NoError(t, err)
	before, err := f.Stat("/" + k)
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond) // LastModified has a resolution of seconds
	require.NoError(t, f.Touch("/"+k, 0))

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(k),
	})
	require.NoError(t, err)
	assert.Equal(t, "text/html", aws.ToString(head.ContentType), "Content-Type")
	assert.Equal(t, `attachment; filename="meta.html"`, aws.ToString(head.ContentDisposition), "Content-Disposition")
	assert.Equal(t, "no-cache", aws.ToString(head.CacheControl), "Cache-Control")
	assert.Equal(t, map[string]string{"owner": "erik"}, head.Metadata, "user metadata")
	assert.True(t, aws.ToTime(head.LastModified).After(before.Modified), "Touch must update LastModified")
	data, err := f.ReadAll(ctx, "/"+k)
	require.NoError(t, err)
	assert.Equal(t, "<p>", string(data), "Touch must not change the content")

	// Touch of a missing object creates an empty one, of a directory does nothing
	require.NoError(t, f.Touch("/"+testDataDir+"/touch/new.txt", 0))
	info, err := f.Stat("/" + testDataDir + "/touch/new.txt")
	require.NoError(t, err)
	assert.Zero(t, info.Size)
	require.NoError(t, f.Touch("/"+testDataDir+"/touch", 0))
	info, err = f.Stat("/" + testDataDir + "/touch")
	require.NoError(t, err)
	assert.True(t, info.IsDir, "Touch of a directory must not create an object")
}

// TestListDirSkipsKeysWithoutPath checks that keys with empty path
// elements are not listed: with "d//x.txt" ListDir("/d") listed /d itself,
// so recursive walkers looped, and a leading slash key (written by go-fs v0)
// made the root list itself.
func TestListDirSkipsKeysWithoutPath(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	dir := testDataDir + "/no-path"
	t.Cleanup(func() {
		_ = f.RemoveAll(context.Background(), "/"+dir)
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(testBucketName),
			Key:    aws.String("/" + dir + "/legacy.txt"),
		})
	})
	putObject(t, client, dir+"/d//x.txt", "x")
	putObject(t, client, dir+"/d/y.txt", "y")
	putObject(t, client, "/"+dir+"/legacy.txt", "old") // v0 style key

	var listed []string
	err := f.ListDir(ctx, "/"+dir+"/d", nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.File.Path())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/" + dir + "/d/y.txt"}, listed, "ListDir must not list the directory itself")

	listed = nil
	err = f.ListDirRecursive(ctx, "/"+dir, nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.File.Path())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/" + dir + "/d/y.txt"}, listed, "ListDirRecursive must skip keys without path")

	err = f.ListDir(ctx, "/", nil, func(info *fs.FileInfo) error {
		assert.NotEqual(t, f.RootDir(), info.File, "the root must not list itself")
		assert.NotEmpty(t, info.Name)
		return nil
	})
	require.NoError(t, err)

	var walked []string
	err = f.RootDir().Join(dir).ListDirRecursive(ctx, func(file fs.File) error {
		walked = append(walked, file.Path())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/" + dir + "/d/y.txt"}, walked)
}

// migrateLeadingSlashKeys is the migration recipe of the README
// for the keys with a leading slash that go-fs v0 wrote.
func migrateLeadingSlashKeys(ctx context.Context, client *s3.Client, bucket string) error {
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("/"),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			oldKey := aws.ToString(obj.Key)
			newKey := strings.TrimLeft(oldKey, "/")
			// The copy source is URL-encoded, S3 decodes a "+" as space
			source := bucket + "/" + strings.ReplaceAll(url.PathEscape(oldKey), "+", "%2B")
			_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     aws.String(bucket),
				Key:        aws.String(newKey),
				CopySource: aws.String(source),
			})
			if err != nil {
				return fmt.Errorf("copying %q: %w", oldKey, err)
			}
		}
	}
	return nil
}

// TestMigrateLeadingSlashKeys checks the migration recipe of the README:
// objects that go-fs v0 wrote with a leading slash are invisible to v1
// until they are copied to keys without it.
func TestMigrateLeadingSlashKeys(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	dir := testDataDir + "/migrate"
	t.Cleanup(func() {
		_ = f.RemoveAll(context.Background(), "/"+dir)
		cleanupTestDir(context.Background(), t, client, "/"+dir)
	})
	putObject(t, client, "/"+dir+"/a+b c.txt", "v0 data")
	putObject(t, client, "/"+dir+"/sub/x.txt", "v0 sub")

	_, err := f.Stat("/" + dir + "/a+b c.txt")
	require.ErrorIs(t, err, os.ErrNotExist, "v0 keys are invisible before the migration")

	require.NoError(t, migrateLeadingSlashKeys(ctx, client, testBucketName))

	data, err := f.ReadAll(ctx, "/"+dir+"/a+b c.txt")
	require.NoError(t, err)
	assert.Equal(t, "v0 data", string(data))
	data, err = f.ReadAll(ctx, "/"+dir+"/sub/x.txt")
	require.NoError(t, err)
	assert.Equal(t, "v0 sub", string(data))
}

// TestDirectoryErrors checks the error contract for directories
// on the native methods: writing or copying onto a directory must fail
// with fs.ErrIsDirectory and leave it intact, listing a missing
// directory must report os.ErrNotExist.
func TestDirectoryErrors(t *testing.T) {
	f, client := newTestFileSystem(t, nil)
	ctx := t.Context()
	dir := "/" + testDataDir + "/dir-errors"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), dir) })
	putObject(t, client, key(dir)+"/implicit/file.txt", "content")
	require.NoError(t, f.MakeDir(dir+"/marker", 0))
	require.NoError(t, f.WriteAll(ctx, dir+"/src.txt", []byte("src"), 0))

	for _, d := range []string{dir + "/implicit", dir + "/marker", "/"} {
		assert.ErrorIs(t, f.WriteAll(ctx, d, []byte("x"), 0), fs.ErrIsDirectory{}, "WriteAll(%q)", d)
		_, err := f.OpenWriter(d, 0)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenWriter(%q)", d)
		assert.ErrorIs(t, f.CopyFile(ctx, dir+"/src.txt", d), fs.ErrIsDirectory{}, "CopyFile onto %q", d)
		assert.ErrorIs(t, f.CopyFile(ctx, d, dir+"/copy.txt"), fs.ErrIsDirectory{}, "CopyFile from %q", d)
		_, err = f.ReadAll(ctx, d)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "ReadAll(%q)", d)
		info, err := f.Stat(d)
		require.NoError(t, err)
		assert.True(t, info.IsDir, "%q must stay a directory", d)
	}
	assert.ErrorIs(t, f.Remove(dir+"/implicit"), fs.ErrDirectoryNotEmpty{}, "Remove of a non-empty directory")

	err := f.ListDir(ctx, dir+"/missing", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrNotExist, "ListDir of a missing directory")
	err = f.ListDirRecursive(ctx, dir+"/missing", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrNotExist, "ListDirRecursive of a missing directory")
	err = f.ListDir(ctx, dir+"/src.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "ListDir of a file")
	err = f.ListDir(ctx, dir+"/marker", nil, func(*fs.FileInfo) error { return errors.New("listed") })
	assert.NoError(t, err, "ListDir of an empty directory")
}

// TestCopyFileInParts checks the multipart copy that CopyFile and Touch use
// for objects above the 5 GiB that a single CopyObject request can copy,
// with the limits lowered to copy an 11 MiB object in 3 parts.
func TestCopyFileInParts(t *testing.T) {
	const mib = 1 << 20
	f, client := newTestFileSystem(t, nil)
	f.maxCopyObjectSize = mib
	f.copyPartSize = 5 * mib
	ctx := t.Context()
	dir := testDataDir + "/copy-parts"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), "/"+dir) })

	data := largeData(11 * mib)
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(testBucketName),
		Key:         aws.String(dir + "/src.bin"),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/x-test"),
		Metadata:    map[string]string{"owner": "erik"},
	})
	require.NoError(t, err)

	require.NoError(t, f.CopyFile(ctx, "/"+dir+"/src.bin", "/"+dir+"/dest.bin"))
	copied, err := f.ReadAll(ctx, "/"+dir+"/dest.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, copied), "copied content")
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(dir + "/dest.bin"),
	})
	require.NoError(t, err)
	assert.Equal(t, "application/x-test", aws.ToString(head.ContentType), "Content-Type of the copy")
	assert.Equal(t, map[string]string{"owner": "erik"}, head.Metadata, "metadata of the copy")
	assert.True(t, strings.HasSuffix(strings.Trim(aws.ToString(head.ETag), `"`), "-3"), "copied in 3 parts: ETag %s", aws.ToString(head.ETag))

	require.NoError(t, f.Touch("/"+dir+"/src.bin", 0), "Touch in parts")
	touched, err := f.ReadAll(ctx, "/"+dir+"/src.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, touched), "Touch must not change the content")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err = f.copyObjectInParts(canceled, client, dir+"/src.bin", dir+"/canceled.bin", head)
	assert.ErrorIs(t, err, context.Canceled)

	// A canceled context already fails CreateMultipartUpload, a failed part
	// copy fails after the upload was created: the ETag of the source
	// changed, so S3 answers UploadPartCopy with 412 Precondition Failed
	stale := *head
	stale.ETag = aws.String(`"00000000000000000000000000000000"`)
	err = f.copyObjectInParts(ctx, client, dir+"/src.bin", dir+"/stale.bin", &stale)
	assert.True(t, hasErrorCode(err, "PreconditionFailed"), "copy of a changed source: %v", err)
	_, err = f.Stat("/" + dir + "/stale.bin")
	assert.ErrorIs(t, err, os.ErrNotExist, "a failed copy must not create the object")
	uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(testBucketName),
		Prefix: aws.String(dir + "/"),
	})
	require.NoError(t, err)
	assert.Empty(t, uploads.Uploads, "a failed copy must abort its multipart upload")

	err = f.CopyFile(ctx, "/"+dir+"/missing.bin", "/"+dir+"/dest2.bin")
	assert.ErrorIs(t, err, os.ErrNotExist, "CopyFile of a missing object")
}

// TestReadAllRanges checks that ReadAll downloads in ranges
// with the ETag of the object and fails cleanly for a missing one.
func TestReadAllRanges(t *testing.T) {
	const mib = 1 << 20
	f, _ := newTestFileSystem(t, &Options{MultipartDownloadThreshold: mib, PartSize: mib, Concurrency: 3})
	ctx := t.Context()
	p := "/" + testDataDir + "/ranges/data.bin"
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), "/"+testDataDir+"/ranges") })

	data := largeData(3*mib + 17)
	require.NoError(t, f.WriteAll(ctx, p, data, 0))
	read, err := f.ReadAll(ctx, p)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "content downloaded in ranges")

	_, err = f.ReadAll(ctx, "/"+testDataDir+"/ranges/missing.bin")
	assert.ErrorIs(t, err, os.ErrNotExist)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.ReadAll(canceled, p)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestValidateName verifies the names that a copy or move into the file
// system rejects with ValidateName before it writes anything.
func TestValidateName(t *testing.T) {
	f := &fileSystem{}
	for _, name := range []string{"a.txt", "Größe: what?*.txt", "trailing.", strings.Repeat("a", 1024)} {
		assert.NoError(t, f.ValidateName(name), "%.20q", name)
	}
	for _, name := range []string{"\xff.txt", `a\b`, "a\x01b", "a\nb", strings.Repeat("a", 1025)} {
		assert.ErrorIs(t, f.ValidateName(name), os.ErrInvalid, "%.20q", name)
	}
}
