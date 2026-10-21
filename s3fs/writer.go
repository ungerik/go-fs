package s3fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"

	fs "github.com/ungerik/go-fs"
)

var _ fs.WriteAborter = new(uploadWriter)

// errAborted is the error that the upload of an aborted writer reads.
var errAborted = errors.New("s3fs: upload aborted")

// uploadWriter streams the written data through a pipe into an upload
// that the first Write starts, so the writer doesn't buffer the
// whole file in memory. Close ends the data and returns the result
// of the upload, Abort makes the upload fail without committing anything.
type uploadWriter struct {
	// upload uploads the data of body and commits it
	// only if body ends with io.EOF.
	upload func(ctx context.Context, body io.Reader) error
	// checkOpen returns an error after the file system was closed.
	checkOpen func() error

	mtx    sync.Mutex
	closed bool               // after Close or Abort
	pipe   *io.PipeWriter     // nil until the first Write
	cancel context.CancelFunc // cancels the upload
	done   chan error         // receives the result of the upload
}

func newUploadWriter(upload func(ctx context.Context, body io.Reader) error, checkOpen func() error) *uploadWriter {
	return &uploadWriter{upload: upload, checkOpen: checkOpen}
}

// start starts the upload goroutine, w.mtx must be locked.
func (w *uploadWriter) start() {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	w.pipe, w.cancel, w.done = writer, cancel, make(chan error, 1)
	go func() {
		err := w.upload(ctx, reader)
		// A failed upload stops reading,
		// pending and later Writes get its error
		_ = reader.CloseWithError(err) // never fails
		w.done <- err
	}()
}

// Write writes p to the upload, starting it with the first call.
// It returns the error of a failed upload
// and os.ErrClosed after Close or Abort.
func (w *uploadWriter) Write(p []byte) (int, error) {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return 0, os.ErrClosed
	}
	if w.pipe == nil {
		if err := w.checkOpen(); err != nil {
			w.mtx.Unlock()
			return 0, err
		}
		w.start()
	}
	pipe := w.pipe
	w.mtx.Unlock()
	return pipe.Write(p)
}

// Close completes the upload and returns its error.
// Without any written data it uploads an empty file.
// Close after Close or Abort does nothing and returns nil.
func (w *uploadWriter) Close() error {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return nil
	}
	w.closed = true
	pipe := w.pipe
	w.mtx.Unlock()

	if err := w.checkOpen(); err != nil {
		w.abort(pipe)
		return err
	}
	if pipe == nil {
		return w.upload(context.Background(), bytes.NewReader(nil))
	}
	_ = pipe.Close() // never fails
	err := <-w.done
	w.cancel()
	return err
}

// Abort makes the upload fail, so no data is committed and an existing
// file is not changed, and waits until the upload stopped.
// It implements fs.WriteAborter. Close after Abort does nothing
// and returns nil, so does Abort after Close or Abort.
func (w *uploadWriter) Abort() error {
	w.mtx.Lock()
	if w.closed {
		w.mtx.Unlock()
		return nil
	}
	w.closed = true
	pipe := w.pipe
	w.mtx.Unlock()

	w.abort(pipe)
	return nil
}

// abort stops the upload of pipe, if it was started,
// and waits until it returned.
func (w *uploadWriter) abort(pipe *io.PipeWriter) {
	if pipe == nil {
		return // Nothing was uploaded
	}
	// The upload reads the error first and aborts a multipart upload,
	// then canceling interrupts the requests in flight
	_ = pipe.CloseWithError(errAborted) // never fails
	w.cancel()
	<-w.done
}
