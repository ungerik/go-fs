package s3fs_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

var s3 *s3fs.S3FileSystem
var bucketName string

type defaultPathTestCase struct {
	input string
	want  string
}

// NOTE: For most of these you need to have AWS credentials defined somewhere.
// Read more at: https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/configuring-sdk.html

func TestMain(m *testing.M) {
	bucketName = os.Getenv("S3_BUCKET_NAME")
	if bucketName == "" {
		panic(errors.New("You need to set S3_BUCKET_NAME environment variable"))
	}
	s3 = createS3FSInstance()

	retCode := m.Run()
	os.Exit(retCode)
}

func createS3FSInstance() *s3fs.S3FileSystem {
	r, err := s3fs.RegionFromString(os.Getenv("S3_REGION"))
	if err != nil {
		panic(err)
	}
	return s3fs.New(os.Getenv("S3_BUCKET_NAME"), r, time.Second*10)
}

func TestNew(t *testing.T) {
	fs := createS3FSInstance()
	assert.NotNil(t, fs)
}

func TestClose(t *testing.T) {
	if err := s3.Close(); err != nil {
		t.Error(err)
	}

	assert.NotContains(t, fs.Registry, s3.Prefix())
	// We need to create the instance again for other tests
	s3 = createS3FSInstance()
}

func TestFile(t *testing.T) {
	f := s3.File("test/doc.pdf")
	assert.NotNil(t, f)
	assert.NotEmpty(t, f)
	assert.Equal(t, "/test/doc.pdf", f.Path())
	assert.Equal(t, "doc.pdf", f.Name())
	assert.Equal(t, fmt.Sprintf("s3://%s/test/doc.pdf", bucketName), f.URL())
}

func TestJoinCleanFile(t *testing.T) {
	testCases := []struct {
		input string
		want  fs.File
	}{
		{fmt.Sprintf("s3://%s/test.pdf", bucketName), fs.File(fmt.Sprintf("s3://%s/test.pdf", bucketName))},
		{"test.pdf", fs.File(fmt.Sprintf("s3://%s/test.pdf", bucketName))},
		{"/test.pdf", fs.File(fmt.Sprintf("s3://%s/test.pdf", bucketName))},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.want, s3.JoinCleanFile(tc.input))
	}
}

func TestURL(t *testing.T) {
	assert.Equal(t, fmt.Sprintf("s3://%s/test/test.pdf", bucketName), s3.URL("/test/test.pdf"))
}

func TestJoinCleanPath(t *testing.T) {
	testCases := []defaultPathTestCase{
		{fmt.Sprintf("s3://%s/test.pdf", bucketName), "/test.pdf"},
		{"test.pdf", "/test.pdf"},
		{"/test.pdf", "/test.pdf"},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.want, s3.JoinCleanPath(tc.input))
	}
}

func TestSplitPath(t *testing.T) {
	testCases := []struct {
		input string
		want  []string
	}{
		{fmt.Sprintf("s3://%s/test/test.pdf", bucketName), []string{"test", "test.pdf"}},
		{"/test/test.pdf", []string{"test", "test.pdf"}},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.want, s3.SplitPath(tc.input))
	}
}

func TestDirAndName(t *testing.T) {
	testCases := []struct {
		input string
		want  [2]string
	}{
		{fmt.Sprintf("s3://%s/test/test.pdf", bucketName), [2]string{fmt.Sprintf("s3://%s/test", bucketName), "test.pdf"}},
		{"/test/test.pdf", [2]string{"/test", "test.pdf"}},
	}
	for _, tc := range testCases {
		dir, name := s3.DirAndName(tc.input)
		res := [2]string{dir, name}
		assert.Equal(t, tc.want, res)
	}
}

func TestIsAbsPath(t *testing.T) {
	assert.True(t, s3.IsAbsPath("/test/test.pdf"))
	assert.False(t, s3.IsAbsPath("test.df"))
}

func TestAbsPath(t *testing.T) {
	testCases := []defaultPathTestCase{
		{fmt.Sprintf("s3://%s/test/test.pdf", bucketName), fmt.Sprintf("s3://%s/test/test.pdf", bucketName)},
		{"test.pdf", "/test.pdf"},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.want, s3.AbsPath(tc.input))
	}
}

func TestRoot(t *testing.T) {
	expectedFile := s3.File("/")
	actualFile := s3.Root()
	assert.Equal(t, expectedFile, actualFile)
}

func TestStat(t *testing.T) {
	i := s3.Stat("pdf_input/doc.pdf")
	assert.NotEmpty(t, i)
}

func TestStatNotExists(t *testing.T) {
	i := s3.Stat("DOESNOTEXIST/")
	assert.Equal(t, false, i.Exists)
}

func TestListDir(t *testing.T) {
	var fileInfos []fs.FileInfo
	err := s3.ListDirInfo("pdf_input/", func(f fs.File, fi fs.FileInfo) error {
		fileInfos = append(fileInfos, fi)
		t.Log(string(f))
		return nil
	}, nil)
	assert.NoError(t, err)
	assert.NotEmpty(t, fileInfos)
}
