package onedrivefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ungerik/go-fs"
)

const (
	// maxSimpleUpload is the size up to which a file is uploaded with
	// one request. Graph accepts up to 250 MB, but a larger file is
	// uploaded with an upload session, so that after throttling only
	// a fragment is sent again and not the whole file.
	maxSimpleUpload = 4 << 20

	// uploadFragmentSize is the size of the fragments of upload sessions,
	// 10 MiB. Graph requires multiples of 320 KiB and accepts up to 60 MiB.
	uploadFragmentSize = 32 * 320 << 10

	// cancelSessionTimeout limits the request that cancels
	// the upload session of a failed upload.
	cancelSessionTimeout = 30 * time.Second
)

// octetStream is the header of the requests that upload file content.
// It is only read.
var octetStream = http.Header{"Content-Type": {"application/octet-stream"}}

// WriteAll uploads data as the file, overwriting an existing one.
// Data larger than 4 MiB is uploaded with an upload session.
// A folder at filePath is an fs.ErrIsDirectory.
func (f *fileSystem) WriteAll(ctx context.Context, filePath string, data []byte, perm fs.Permissions) error {
	if err := f.check(ctx); err != nil {
		return err
	}
	if filePath == "" {
		return fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return fs.NewErrIsDirectory(f.RootDir())
	}
	if len(data) <= f.maxSimple {
		return f.uploadSimple(ctx, p, data)
	}
	return f.uploadSession(ctx, p, int64(len(data)), bytes.NewReader(data))
}

// OpenWriter returns a writer that uploads the file, see uploadWriter.
// The file is created or replaced when the writer is closed,
// a folder at filePath is an fs.ErrIsDirectory then.
// The writer implements fs.WriteAborter.
func (f *fileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	if err := f.checkClosed(); err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fs.ErrEmptyPath
	}
	p := cleanPath(filePath)
	if p == "/" {
		return nil, fs.NewErrIsDirectory(f.RootDir())
	}
	return &uploadWriter{f: f, path: p}, nil
}

// uploadSimple writes data as the file at the clean path p with one
// request, replacing an existing file. Graph creates missing parent
// folders. data must not be larger than the limit of the request.
func (f *fileSystem) uploadSimple(ctx context.Context, p string, data []byte) error {
	resp, err := f.do(ctx, http.MethodPut, f.itemURL(p, "/content")+"?@microsoft.graph.conflictBehavior=replace", octetStream, data)
	if err != nil {
		return f.wrapUploadErr(ctx, p, err)
	}
	return resp.Body.Close()
}

// uploadSession writes the size bytes of r as the file at the clean
// path p with an upload session, replacing an existing file.
// The session is canceled if the upload fails.
func (f *fileSystem) uploadSession(ctx context.Context, p string, size int64, r io.ReaderAt) (err error) {
	// A folder in the way fails before the data is sent
	if item, err := f.item(ctx, p); err == nil && item.isDir() {
		return fs.NewErrIsDirectory(f.file(p))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	body := map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"}}
	if _, err := f.doJSON(ctx, http.MethodPost, f.itemURL(p, "/createUploadSession"), nil, body, &session); err != nil {
		return f.wrapUploadErr(ctx, p, err)
	}
	if session.UploadURL == "" {
		return fmt.Errorf("onedrivefs: the upload session for %s has no upload URL", f.file(p))
	}
	committed := false
	defer func() {
		if !committed {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelSessionTimeout)
			defer cancel()
			if resp, cancelErr := f.do(ctx, http.MethodDelete, session.UploadURL, nil, nil); cancelErr == nil {
				_ = resp.Body.Close()
			}
		}
	}()
	fragment := make([]byte, min(int64(f.fragmentSize), size))
	for offset := int64(0); offset < size; {
		n := min(int64(len(fragment)), size-offset)
		if k, err := r.ReadAt(fragment[:n], offset); int64(k) < n {
			return fmt.Errorf("onedrivefs: reading the data of the upload of %s: %w", f.file(p), err)
		}
		header := octetStream.Clone()
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, size))
		resp, err := f.do(ctx, http.MethodPut, session.UploadURL, header, fragment[:n])
		if err != nil {
			return f.wrapUploadErr(ctx, p, err)
		}
		_ = resp.Body.Close()
		offset += n
		if offset == size && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("onedrivefs: the upload session of %s answered the last fragment with %s", f.file(p), resp.Status)
		}
	}
	committed = true
	return nil
}

// wrapUploadErr converts the error of an upload to the clean path p:
// a folder at p is an fs.ErrIsDirectory and a file in place of a parent
// folder an fs.ErrIsNotDirectory, other errors are converted by wrapErr.
// The Graph API tells neither apart from other bad requests or conflicts,
// so the items are requested to tell.
func (f *fileSystem) wrapUploadErr(ctx context.Context, p string, err error) error {
	if s := statusOf(err); s == http.StatusBadRequest || s == http.StatusNotFound || s == http.StatusConflict {
		if item, statErr := f.item(ctx, p); statErr == nil && item.isDir() {
			return fs.NewErrIsDirectory(f.file(p))
		}
		if ancestorErr := f.ancestorErr(ctx, p, false); ancestorErr != nil {
			return ancestorErr
		}
	}
	return f.wrapErr(p, err)
}

// uploadWriter is the writer of OpenWriter.
//
// An upload session needs the size of the file before the first fragment.
// So the writer buffers up to the size of a simple upload in memory and
// uploads such a file with one request on Close. A larger file is written
// to a temporary file of os.TempDir, which Close uploads with an upload
// session and removes.
//
// It implements fs.WriteAborter: Abort discards the data and removes
// the temporary file, the file of the drive is not changed.
type uploadWriter struct {
	f      *fileSystem
	path   string // clean path of the file
	buf    []byte
	spool  *os.File // temporary file of a file larger than f.maxSimple
	size   int64    // bytes written to spool
	err    error    // first error, returned by Write and Close
	closed bool
}

// Write buffers p, or writes it to the temporary file once the file
// is larger than a simple upload.
func (w *uploadWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.spool == nil && len(w.buf)+len(p) <= w.f.maxSimple {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	if w.spool == nil {
		spool, err := os.CreateTemp("", "onedrivefs-upload-*")
		if err != nil {
			w.err = fmt.Errorf("onedrivefs: creating the temporary file for the upload of %s: %w", w.f.file(w.path), err)
			return 0, w.err
		}
		w.spool = spool
		buf := w.buf
		w.buf = nil
		if _, err := w.spool.Write(buf); err != nil {
			w.err = fmt.Errorf("onedrivefs: writing the temporary file for the upload of %s: %w", w.f.file(w.path), err)
			return 0, w.err
		}
		w.size = int64(len(buf))
	}
	n, err := w.spool.Write(p)
	w.size += int64(n)
	if err != nil {
		w.err = fmt.Errorf("onedrivefs: writing the temporary file for the upload of %s: %w", w.f.file(w.path), err)
		return n, w.err
	}
	return n, nil
}

// Close uploads the data and removes the temporary file.
// Close is idempotent, it returns nil after Close or Abort.
func (w *uploadWriter) Close() (err error) {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() {
		err = errors.Join(err, w.removeSpool())
	}()
	if w.err != nil {
		return w.err
	}
	ctx := context.Background()
	if err := w.f.check(ctx); err != nil {
		return err
	}
	if w.spool == nil {
		buf := w.buf
		w.buf = nil
		return w.f.uploadSimple(ctx, w.path, buf)
	}
	return w.f.uploadSession(ctx, w.path, w.size, w.spool)
}

// Abort discards the written data without changing the file,
// implementing fs.WriteAborter.
func (w *uploadWriter) Abort() error {
	w.closed = true
	w.buf = nil
	return w.removeSpool()
}

// removeSpool closes and removes the temporary file, if there is one.
func (w *uploadWriter) removeSpool() error {
	if w.spool == nil {
		return nil
	}
	spool := w.spool
	w.spool = nil
	return errors.Join(spool.Close(), os.Remove(spool.Name()))
}

var _ fs.WriteAborter = new(uploadWriter)
