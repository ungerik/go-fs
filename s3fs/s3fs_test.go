package s3fs_test

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

// Problems with directories:
// The API in general is a bit incosistent here. S3 requires trailing slashes
// when creating, changing, deleting directories, but treats them just like any
// other object and therefore doesn't return their keys with a trailing slash.
// This means that some tests require us to add the trailing slash.
//
// We could actually just define directories like all the other objects, but
// that would make it more difficult to determine which objects are
// "directories" and which aren't.
var directories = []string{"test", "test1", "test/test2"}
var files = []string{"test.txt", "test/test.txt", "test/test2/text.txt"}

var testData = []byte("TEST 123")

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

func TestRoot(t *testing.T) {
	assert.Equal(t, fmt.Sprintf("%s%s/", s3fs.Prefix, bucketName), string(s3.Root()))
}

func TestID(t *testing.T) {
	id, err := s3.ID()
	if err != nil {
		t.Error(err)
	}
	assert.Equal(t, bucketName, id)
}

func TestPrefix(t *testing.T) {
	assert.Equal(t, fmt.Sprintf("%s%s", s3fs.Prefix, bucketName), s3.Prefix())
}

func TestFile(t *testing.T) {
	testCases := []struct {
		filePath string
		vals     []string
	}{
		{"test/doc.pdf", []string{"/test/doc.pdf", "doc.pdf", fmt.Sprintf("%s%s/test/doc.pdf", s3fs.Prefix, bucketName)}},
		{"/", []string{"/", "", fmt.Sprintf("%s%s/", s3fs.Prefix, bucketName)}},
	}

	for _, tc := range testCases {
		f := s3.File(tc.filePath)
		assert.NotNil(t, f)
		assert.NotEmpty(t, f)
		assert.Equal(t, tc.vals[0], f.Path())
		assert.Equal(t, tc.vals[1], f.Name())
		assert.Equal(t, tc.vals[2], f.URL())
	}
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

func TestMakeDir(t *testing.T) {
	for _, d := range directories {
		assert.NoError(t, s3.MakeDir(d, nil))
		assert.True(t, s3.Stat(d+"/").Exists)
	}
}

func TestWriteAll(t *testing.T) {
	for _, f := range files {
		assert.NoError(t, s3.WriteAll(f, testData, nil))
		text, err := s3.File(f).ReadAllString()
		assert.NoError(t, err)
		assert.Equal(t, string(testData), text)
	}
}

func TestStat(t *testing.T) {
	for _, f := range files {
		assert.NotEmpty(t, s3.Stat(f))
	}
}

func TestStatNotExists(t *testing.T) {
	i := s3.Stat("DOESNOTEXIST/")
	assert.Equal(t, false, i.Exists)
}

func TestIsHidden(t *testing.T) {
	testCases := []struct {
		path   string
		hidden bool
	}{
		{".test.txt", true},
		{"test.txt", false},
		{fmt.Sprintf("%s%s/.test.txt", s3fs.Prefix, bucketName), true},
		{fmt.Sprintf("%s%s/test.txt", s3fs.Prefix, bucketName), false},
		{"/.test", true},
	}
	for _, tc := range testCases {
		assert.Equal(t, tc.hidden, s3.IsHidden(tc.path))
	}
}

func TestListDirInfo(t *testing.T) {
	expectedOutput := []string{"test.txt", "test", "test1"}

	result := []string{}
	assert.NoError(t, s3.ListDirInfo("/", func(file fs.File, _ fs.FileInfo) error {
		result = append(result, file.Path())
		return nil
	}, nil))

	assert.EqualValues(t, expectedOutput, result)
}

func TestListDirInfoRecursive(t *testing.T) {
	expectedOutput := append(directories, files...)
	assert.NoError(t, s3.ListDirInfoRecursive("/", func(file fs.File, _ fs.FileInfo) error {
		assert.Contains(t, expectedOutput, file.Path())
		return nil
	}, nil))
}

func TestListDirMax(t *testing.T) {
	files, err := s3.ListDirMax("/", 2, nil)
	assert.NoError(t, err)
	assert.Condition(t, assert.Comparison(func() bool {
		return len(files) <= 2
	}), "Too many object keys returned")
}

func TestTouch(t *testing.T) {
	path := "test1/touchtest"
	files = append(files, path)
	assert.NoError(t, s3.Touch(path, nil))
	assert.True(t, s3.File(path).Exists())
}

func TestTruncate(t *testing.T) {
	assert.NoError(t, s3.Truncate(files[0], 2))
	data, err := s3.ReadAll(files[0])
	assert.NoError(t, err)
	assert.Len(t, data, 2)
}

func TestCopyFile(t *testing.T) {
	parts := strings.Split(files[1], ".")
	targetPath := parts[0] + "_copy." + parts[1]
	assert.NoError(t, s3.CopyFile(files[1], targetPath, nil))
	assert.True(t, s3.Stat(targetPath).Exists)
	dataInCopy, err := s3.ReadAll(targetPath)
	assert.NoError(t, err)
	assert.Equal(t, testData, dataInCopy)
	files = append(files, targetPath)
}

func TestRename(t *testing.T) {
	originalData, err := s3.ReadAll(files[1])
	assert.NoError(t, err)

	parts := strings.Split(files[1], ".")
	renamedFile := path.Base(parts[0] + "_renamed." + parts[1])
	assert.NoError(t, s3.Rename(files[1], renamedFile))

	// Renamed file has to exist
	renamedPath := path.Join(path.Dir(files[1]), renamedFile)
	assert.True(t, s3.Stat(renamedPath).Exists)

	// Content has to be equal
	dataOfRenamedFile, err := s3.ReadAll(renamedPath)
	assert.NoError(t, err)
	assert.Equal(t, originalData, dataOfRenamedFile)

	// Original file cannot exist
	assert.False(t, s3.Stat(files[1]).Exists)

	files = append(files, renamedPath)
}

func TestMove(t *testing.T) {
	originalData, err := s3.ReadAll(files[2])
	source := files[2]
	dest := path.Base(files[2]) // Simply move to root directory
	assert.NoError(t, err)
	assert.NoError(t, s3.Move(source, dest))
	assert.False(t, s3.Stat(source).Exists)
	assert.True(t, s3.Stat(dest).Exists)

	dataAfterMove, err := s3.ReadAll(dest)
	assert.NoError(t, err)
	assert.Equal(t, originalData, dataAfterMove)

	files = append(files, dest)
}

// Also test if using fs.File's RemoveRecursive method works with S3.
func TestRemoveRecursive(t *testing.T) {
	assert.NoError(t, s3.File(directories[0]).RemoveRecursive())
	// This directory actually had contents before so if it doesn't exist
	// all of its contents were also deleted.
	assert.False(t, s3.Stat(directories[0]).Exists)
}

func TestRemove(t *testing.T) {
	for _, d := range directories {
		// The directory keys do not have a trailing slash, but trailing slashes
		// are required for S3 to delete directories. See the description at the
		// very top.
		assert.NoError(t, s3.Remove(d+s3fs.Separator))
	}
	for _, f := range files {
		assert.NoError(t, s3.Remove(f))
	}
}
