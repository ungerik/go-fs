package s3fs_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

const (
	minioContainerName = "s3fs-test-minio"
	testS3Port         = "9000"
	testS3ConsolePort  = "9001"
	testAccessKey      = "minioadmin"
	testSecretKey      = "minioadmin"
	testBucketName     = "testbucket"
	testDataDir        = "testdata"
)

var (
	dockerMinioAvailable bool
	testS3Endpoint       string
)

func TestMain(m *testing.M) {
	// Check if Docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		log.Println("Docker not available, skipping Docker-based MinIO S3 tests")
		m.Run()
		return
	}

	ctx := context.Background()

	// Setup MinIO server
	dockerMinioAvailable = setupMinioServer(ctx)
	if dockerMinioAvailable {
		testS3Endpoint = fmt.Sprintf("http://127.0.0.1:%s", testS3Port)
	}

	// Run tests
	exitCode := m.Run()

	// Cleanup
	if dockerMinioAvailable {
		log.Println("Stopping and removing Docker MinIO test server...")
		exec.Command("docker", "stop", minioContainerName).Run()
		exec.Command("docker", "rm", minioContainerName).Run()
		log.Println("Docker container cleanup complete")
	}

	os.Exit(exitCode)
}

func setupMinioServer(ctx context.Context) bool {
	containerName := minioContainerName

	// Stop and remove any existing container with the same name
	exec.Command("docker", "stop", containerName).Run()
	exec.Command("docker", "rm", containerName).Run()

	// Start MinIO container
	log.Println("Starting Docker MinIO container...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", containerName,
		"-p", fmt.Sprintf("%s:9000", testS3Port),
		"-p", fmt.Sprintf("%s:9001", testS3ConsolePort),
		"-e", fmt.Sprintf("MINIO_ROOT_USER=%s", testAccessKey),
		"-e", fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", testSecretKey),
		"minio/minio:latest",
		"server", "/data", "--console-address", ":9001",
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker MinIO container: %v\nOutput: %s", err, output)
		return false
	}
	log.Printf("Started Docker container: %s", containerName)

	// Wait for MinIO to be ready
	log.Println("Waiting for MinIO server to be ready...")
	time.Sleep(3 * time.Second)

	// Test connection and create bucket
	maxRetries := 10
	var testErr error
	for i := range maxRetries {
		client := createS3Client(ctx)

		// Try to create bucket
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(testBucketName),
		})
		if err == nil {
			log.Printf("MinIO server is ready and bucket '%s' created", testBucketName)
			return true
		}

		// Check if bucket already exists (which is also OK)
		_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{
			Bucket: aws.String(testBucketName),
		})
		if headErr == nil {
			log.Printf("MinIO server is ready (bucket already exists)")
			return true
		}

		testErr = err
		if i < maxRetries-1 {
			log.Printf("Retry %d/%d: waiting for MinIO server...", i+1, maxRetries)
			time.Sleep(1 * time.Second)
		}
	}

	log.Printf("Failed to connect to MinIO server after retries: %v", testErr)
	exec.Command("docker", "stop", containerName).Run()
	exec.Command("docker", "rm", containerName).Run()
	return false
}

func createS3Client(ctx context.Context) *s3.Client {
	endpoint := testS3Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%s", testS3Port)
	}
	return s3.New(s3.Options{
		Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			testAccessKey,
			testSecretKey,
			"",
		),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true, // MinIO requires path-style URLs
	})
}

// Test_fileSystem tests the S3 filesystem implementation using MinIO
func Test_fileSystem(t *testing.T) {
	if !dockerMinioAvailable {
		t.Skip("Docker MinIO server not available")
	}

	ctx := context.Background()

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
	fs.RunFileSystemTests(
		ctx,
		t,
		s3fs,
		fmt.Sprintf("S3 file system for bucket: s.bucketName"), // name - matches Name() method
		expectedPrefix,                                           // prefix
		testDataDir,                                              // testDir
	)

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
	if !dockerMinioAvailable {
		t.Skip("Docker MinIO server not available")
	}

	ctx := context.Background()

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

	testFilePath := s3fs.JoinCleanPath(testDataDir, "large-test-file.bin")

	// Test multipart upload via WriteAll
	t.Run("MultipartUpload", func(t *testing.T) {
		err := s3fs.(fs.WriteAllFileSystem).WriteAll(ctx, testFilePath, largeData, nil)
		require.NoError(t, err, "WriteAll should succeed for large file")

		// Verify file exists
		info, err := s3fs.Stat(testFilePath)
		require.NoError(t, err, "Stat should work on uploaded file")
		require.Equal(t, int64(largeFileSize), info.Size(), "File size should match")
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
	err := s3fs.Remove(testFilePath)
	require.NoError(t, err, "Cleanup should succeed")
}
