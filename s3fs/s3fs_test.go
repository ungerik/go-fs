package s3fs_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
	"github.com/ungerik/go-fs/s3fs"
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
		m.Run()
		return
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
	log.Println("Waiting for S3Mock server to be ready...")
	const maxRetries = 60
	var testErr error
	for i := range maxRetries {
		client := createS3Client(ctx)

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
		if i < maxRetries-1 {
			time.Sleep(time.Second)
		}
	}

	log.Printf("Failed to connect to S3Mock server after %d retries: %v", maxRetries, testErr)
	removeContainer(containerName)
	return false
}

func createS3Client(ctx context.Context) *s3.Client {
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
	})
}

// Test_fileSystem tests the S3 filesystem implementation using S3Mock
func Test_fileSystem(t *testing.T) {
	if !dockerS3Available {
		t.Skip("Docker S3Mock server not available")
	}
	ctx := t.Context()

	// Create S3 client and filesystem
	client := createS3Client(ctx)
	s3fs := s3fs.NewAndRegister(client, testBucketName, false)

	// Ensure filesystem is registered
	require.NotNil(t, s3fs, "S3 filesystem should be created")

	// Clean up test directory before tests
	cleanupTestDir(ctx, t, client, testDataDir)

	// Get expected prefix
	expectedPrefix := fmt.Sprintf("s3://%s", testBucketName)

	// Run comprehensive filesystem tests
	fstest.RunConformance(t, s3fs, fstest.Config{
		Name:    fmt.Sprintf("S3 file system for bucket: %s", testBucketName),
		Prefix:  expectedPrefix,
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

// Test_fileSystem_MultipartUploadDownload tests multipart upload/download for large files
func Test_fileSystem_MultipartUploadDownload(t *testing.T) {
	if !dockerS3Available {
		t.Skip("Docker S3Mock server not available")
	}

	ctx := t.Context()

	// Create S3 client and filesystem
	client := createS3Client(ctx)
	s3fs := s3fs.NewAndRegister(client, testBucketName, false)
	defer s3fs.Close()

	// Create a large test file (11 MB - exceeds multipart download threshold of 10 MB)
	largeFileSize := 11 * 1024 * 1024 // 11 MB
	largeData := make([]byte, largeFileSize)
	// Fill with pattern to verify data integrity
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	testFilePath := s3fs.CleanPath(testDataDir, "large-test-file.bin")

	// Test multipart upload via WriteAll
	t.Run("MultipartUpload", func(t *testing.T) {
		err := s3fs.(fs.WriteAllFileSystem).WriteAll(ctx, testFilePath, largeData, 0)
		require.NoError(t, err, "WriteAll should succeed for large file")

		// Verify file exists
		info, err := s3fs.Stat(testFilePath)
		require.NoError(t, err, "Stat should work on uploaded file")
		require.Equal(t, int64(largeFileSize), info.Size, "File size should match")
	})

	// Test multipart download via ReadAll
	t.Run("MultipartDownload", func(t *testing.T) {
		readData, err := s3fs.(fs.ReadAllFileSystem).ReadAll(ctx, testFilePath)
		require.NoError(t, err, "ReadAll should succeed for large file")
		require.Equal(t, len(largeData), len(readData), "Read data size should match")
		require.Equal(t, largeData, readData, "Read data should match written data")
	})

	// Test multipart download via OpenReader
	t.Run("MultipartOpenReader", func(t *testing.T) {
		reader, err := s3fs.OpenReader(testFilePath)
		require.NoError(t, err, "OpenReader should succeed for large file")
		defer reader.Close()

		readData, err := io.ReadAll(reader)
		require.NoError(t, err, "Reading from reader should succeed")
		require.Equal(t, len(largeData), len(readData), "Read data size should match")
		require.Equal(t, largeData, readData, "Read data should match written data")
	})

	// Clean up
	err := s3fs.(fs.WriteFileSystem).Remove(testFilePath)
	require.NoError(t, err, "Cleanup should succeed")
}
