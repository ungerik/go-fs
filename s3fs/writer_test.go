package s3fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fs "github.com/ungerik/go-fs"
)

// fakeUpload records what an uploadWriter uploads.
type fakeUpload struct {
	calls     atomic.Int32
	committed atomic.Value // []byte of the last upload that read io.EOF
	err       error        // returned instead of reading the body
	readErr   chan error   // receives the error that ended reading
}

func newFakeUpload() *fakeUpload {
	return &fakeUpload{readErr: make(chan error, 2)}
}

func (u *fakeUpload) upload(ctx context.Context, body io.Reader) error {
	u.calls.Add(1)
	if u.err != nil {
		return u.err
	}
	data, err := io.ReadAll(body)
	u.readErr <- err
	if err != nil {
		return err
	}
	u.committed.Store(data)
	return nil
}

func (u *fakeUpload) data() []byte {
	data, _ := u.committed.Load().([]byte)
	return data
}

func isOpen() error { return nil }

func TestUploadWriter(t *testing.T) {
	t.Run("Close", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		_, err := w.Write([]byte("hello "))
		require.NoError(t, err)
		_, err = w.Write([]byte("world"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		assert.Equal(t, "hello world", string(u.data()))
		assert.Equal(t, int32(1), u.calls.Load())

		assert.NoError(t, w.Close(), "Close after Close")
		assert.NoError(t, w.Abort(), "Abort after Close")
		_, err = w.Write([]byte("x"))
		assert.ErrorIs(t, err, os.ErrClosed, "Write after Close")
		assert.Equal(t, "hello world", string(u.data()))
	})

	t.Run("CloseWithoutWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		require.NoError(t, w.Close())
		assert.Equal(t, int32(1), u.calls.Load(), "Close without Write must upload an empty file")
		assert.NotNil(t, u.committed.Load())
		assert.Empty(t, u.data())
	})

	t.Run("AbortWithoutWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		require.NoError(t, w.Abort())
		require.NoError(t, w.Close(), "Close after Abort")
		assert.Zero(t, u.calls.Load(), "Abort without Write must not upload")
	})

	t.Run("AbortAfterWrite", func(t *testing.T) {
		u := newFakeUpload()
		w := newUploadWriter(u.upload, isOpen)
		_, err := w.Write([]byte("aborted data"))
		require.NoError(t, err)
		require.NoError(t, w.Abort())
		assert.ErrorIs(t, <-u.readErr, errAborted, "the upload must read the abort error")
		assert.Nil(t, u.committed.Load(), "Abort must not commit")
		require.NoError(t, w.Close(), "Close after Abort")
		_, err = w.Write([]byte("x"))
		assert.ErrorIs(t, err, os.ErrClosed, "Write after Abort")
		assert.Nil(t, u.committed.Load(), "Close after Abort must not commit")
	})

	t.Run("UploadError", func(t *testing.T) {
		u := newFakeUpload()
		u.err = errors.New("upload failed")
		w := newUploadWriter(u.upload, isOpen)
		var err error
		// The failed upload doesn't read, the error reaches the next Writes
		for range 3 {
			if _, err = w.Write([]byte("data")); err != nil {
				break
			}
		}
		assert.ErrorIs(t, err, u.err, "Write after the upload failed")
		assert.ErrorIs(t, w.Close(), u.err, "Close returns the upload error")
	})

	t.Run("FileSystemClosed", func(t *testing.T) {
		u := newFakeUpload()
		var closed atomic.Bool
		checkOpen := func() error {
			if closed.Load() {
				return fs.ErrFileSystemClosed
			}
			return nil
		}
		w := newUploadWriter(u.upload, checkOpen)
		_, err := w.Write([]byte("data"))
		require.NoError(t, err)
		closed.Store(true)
		assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
		assert.ErrorIs(t, <-u.readErr, errAborted, "Close after the file system was closed must abort")
		assert.Nil(t, u.committed.Load())

		w = newUploadWriter(u.upload, checkOpen)
		_, err = w.Write([]byte("data"))
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "first Write after the file system was closed")
		assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
	})
}

// TestOpenWriterStreaming checks that OpenWriter streams data larger than
// the multipart threshold in parts and that Abort of a multipart upload
// in progress leaves neither the object nor its uploaded parts behind.
func TestOpenWriterStreaming(t *testing.T) {
	const mib = 1 << 20
	f, client := newTestFileSystem(t, &Options{
		MultipartUploadThreshold: 5 * mib,
		PartSize:                 5 * mib,
	})
	ctx := t.Context()
	dir := testDataDir + "/streaming"
	// The file system is closed at the end of the test
	t.Cleanup(func() { cleanupTestDir(context.Background(), t, client, dir) })

	data := largeData(12 * mib)
	w, err := f.OpenWriter("/"+dir+"/large.bin", 0)
	require.NoError(t, err)
	_, isAborter := w.(fs.WriteAborter)
	require.True(t, isAborter, "the writer must implement fs.WriteAborter")
	// Write in small chunks like io.Copy does
	for chunk := range slices.Chunk(data, 32*1024) {
		_, err = w.Write(chunk)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	read, err := f.ReadAll(ctx, "/"+dir+"/large.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "streamed content")
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(dir + "/large.bin"),
	})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(strings.Trim(aws.ToString(head.ETag), `"`), "-3"), "streamed in 3 parts: ETag %s", aws.ToString(head.ETag))

	// A multipart upload replaces an existing object on Close
	require.NoError(t, f.WriteAll(ctx, "/"+dir+"/replaced.bin", []byte("existing"), 0))
	w, err = f.OpenWriter("/"+dir+"/replaced.bin", 0)
	require.NoError(t, err)
	for chunk := range slices.Chunk(data, 32*1024) {
		_, err = w.Write(chunk)
		require.NoError(t, err)
	}
	existing, err := f.ReadAll(ctx, "/"+dir+"/replaced.bin")
	require.NoError(t, err)
	assert.Equal(t, "existing", string(existing), "the object must not change before Close")
	require.NoError(t, w.Close())
	read, err = f.ReadAll(ctx, "/"+dir+"/replaced.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "Close must replace the existing object")
	head, err = client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(testBucketName),
		Key:    aws.String(dir + "/replaced.bin"),
	})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(strings.Trim(aws.ToString(head.ETag), `"`), "-3"), "replaced in 3 parts: ETag %s", aws.ToString(head.ETag))

	// Abort after the first parts were uploaded. An existing object can't
	// be checked here, because AbortMultipartUpload of S3Mock deletes the
	// object with the key of the upload, AWS keeps it.
	w, err = f.OpenWriter("/"+dir+"/aborted.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(data)
	require.NoError(t, err)
	require.NoError(t, w.(fs.WriteAborter).Abort())
	require.NoError(t, w.Close(), "Close after Abort")
	_, err = f.Stat("/" + dir + "/aborted.bin")
	assert.ErrorIs(t, err, os.ErrNotExist, "Abort must not create the object")
	uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(testBucketName),
		Prefix: aws.String(dir + "/"),
	})
	require.NoError(t, err)
	assert.Empty(t, uploads.Uploads, "Abort must abort the multipart upload")

	// Abort before the multipart threshold was reached
	require.NoError(t, f.WriteAll(ctx, "/"+dir+"/existing.bin", []byte("existing"), 0))
	w, err = f.OpenWriter("/"+dir+"/existing.bin", 0)
	require.NoError(t, err)
	_, err = w.Write(data[:mib])
	require.NoError(t, err)
	require.NoError(t, w.(fs.WriteAborter).Abort())
	existing, err = f.ReadAll(ctx, "/"+dir+"/existing.bin")
	require.NoError(t, err)
	assert.Equal(t, "existing", string(existing), "Abort must not change an existing object")

	// A small file is uploaded with a single request on Close
	w, err = f.OpenWriter("/"+dir+"/small.txt", 0)
	require.NoError(t, err)
	_, err = io.WriteString(w, "small")
	require.NoError(t, err)
	_, err = f.Stat("/" + dir + "/small.txt")
	assert.ErrorIs(t, err, os.ErrNotExist, "the object must not exist before Close")
	require.NoError(t, w.Close())
	small, err := f.ReadAll(ctx, "/"+dir+"/small.txt")
	require.NoError(t, err)
	assert.Equal(t, "small", string(small))

	// Close of a writer after the file system was closed
	w, err = f.OpenWriter("/"+dir+"/after-close.txt", 0)
	require.NoError(t, err)
	_, err = io.WriteString(w, "data")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
}
