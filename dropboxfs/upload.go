package dropboxfs

import (
	"bytes"
	"context"
	"os"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/ungerik/go-fs"
)

const (
	// maxUploadSize is the limit of the data of one upload request
	// of the Dropbox API. Larger files are uploaded with an upload session.
	maxUploadSize = 150 << 20

	// uploadChunkSize is the size of the chunks of upload sessions and the
	// size up to which a writer buffers a file before it starts an upload
	// session. Dropbox recommends multiples of 4 MiB.
	uploadChunkSize = 32 << 20
)

// overwriteMode returns the write mode that replaces an existing file.
func overwriteMode() *files.WriteMode {
	return &files.WriteMode{Tagged: dropbox.Tagged{Tag: files.WriteModeOverwrite}}
}

// upload writes data as the file at the API path p, replacing an
// existing file, with one upload request, or with an upload session
// if data is larger than the limit of an upload request.
func (dbfs *fileSystem) upload(ctx context.Context, p string, data []byte) (*files.FileMetadata, error) {
	if len(data) <= dbfs.maxUpload {
		return dbfs.uploadOne(ctx, p, data)
	}
	s := uploadSession{dbfs: dbfs, path: p}
	for len(data) > dbfs.chunkSize {
		if err := s.append(ctx, data[:dbfs.chunkSize]); err != nil {
			return nil, err
		}
		data = data[dbfs.chunkSize:]
	}
	return s.finish(ctx, data)
}

// uploadOne writes data as the file at the API path p with one upload
// request, replacing an existing file. data must not be larger than
// the limit of an upload request.
func (dbfs *fileSystem) uploadOne(ctx context.Context, p string, data []byte) (*files.FileMetadata, error) {
	arg := files.NewUploadArg(p)
	arg.Mode = overwriteMode()
	arg.Mute = dbfs.mute
	return dbfs.client.UploadContext(ctx, arg, bytes.NewReader(data))
}

// uploaded updates the cache after an upload to the API path p
// of filePath and returns its error converted by wrapErr.
func (dbfs *fileSystem) uploaded(p, filePath string, meta *files.FileMetadata, err error) error {
	if err != nil {
		dbfs.cache.Delete(p)
		return dbfs.wrapErr(filePath, err)
	}
	dbfs.cache.Put(p, dbfs.metadataToFileInfo(meta))
	return nil
}

// uploadSession uploads a file in chunks with an upload session,
// which is started by the first chunk and committed by finish.
type uploadSession struct {
	dbfs   *fileSystem
	path   string // API path of the file
	id     string // empty before the first chunk
	offset uint64 // number of bytes uploaded so far
}

// append uploads the next chunk of the file.
func (s *uploadSession) append(ctx context.Context, chunk []byte) error {
	if err := s.dbfs.check(ctx); err != nil {
		return err
	}
	if s.id == "" {
		result, err := s.dbfs.client.UploadSessionStartContext(ctx, files.NewUploadSessionStartArg(), bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		s.id = result.SessionId
	} else {
		arg := files.NewUploadSessionAppendArg(files.NewUploadSessionCursor(s.id, s.offset))
		err := s.dbfs.client.UploadSessionAppendV2Context(ctx, arg, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
	}
	s.offset += uint64(len(chunk))
	return nil
}

// finish uploads the last chunk, which may be empty, and commits the file,
// replacing an existing one. At least one chunk must have been appended.
func (s *uploadSession) finish(ctx context.Context, chunk []byte) (*files.FileMetadata, error) {
	if err := s.dbfs.check(ctx); err != nil {
		return nil, err
	}
	commit := files.NewCommitInfo(s.path)
	commit.Mode = overwriteMode()
	commit.Mute = s.dbfs.mute
	arg := files.NewUploadSessionFinishArg(files.NewUploadSessionCursor(s.id, s.offset), commit)
	return s.dbfs.client.UploadSessionFinishContext(ctx, arg, bytes.NewReader(chunk))
}

// uploadWriter is the writer of OpenWriter.
//
// It buffers up to one chunk and uploads a file that fits into a chunk
// with one request on Close. A larger file is uploaded with an upload
// session chunk by chunk while it is written, and the session is
// committed on Close, so at most one chunk is held in memory.
//
// It implements fs.WriteAborter: Abort discards the data. An upload
// session that was already started is never committed and expires
// on the Dropbox servers, the file is not changed.
type uploadWriter struct {
	dbfs     *fileSystem
	filePath string
	session  uploadSession
	buf      []byte
	err      error // first upload error, returned by Write and Close
	closed   bool
}

// Write buffers p and uploads every completed chunk
// except the last one, which is uploaded by Close.
func (w *uploadWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	n := len(p)
	for len(p) > 0 {
		if len(w.buf) == w.dbfs.chunkSize {
			err := w.session.append(context.Background(), w.buf)
			if err != nil {
				w.err = w.dbfs.wrapErr(w.filePath, err)
				return n - len(p), w.err
			}
			w.buf = w.buf[:0]
		}
		k := min(w.dbfs.chunkSize-len(w.buf), len(p))
		w.buf = append(w.buf, p[:k]...)
		p = p[k:]
	}
	return n, nil
}

// Close uploads the buffered data and commits the file.
// Close is idempotent, it returns nil after Close or Abort.
func (w *uploadWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	buf := w.buf
	w.buf = nil
	if w.err != nil {
		return w.err
	}
	ctx := context.Background()
	if err := w.dbfs.check(ctx); err != nil {
		return err
	}
	var (
		meta *files.FileMetadata
		err  error
	)
	if w.session.id == "" {
		meta, err = w.dbfs.uploadOne(ctx, w.session.path, buf)
	} else {
		meta, err = w.session.finish(ctx, buf)
	}
	return w.dbfs.uploaded(w.session.path, w.filePath, meta, err)
}

// Abort discards the written data without changing the file,
// implementing fs.WriteAborter.
func (w *uploadWriter) Abort() error {
	w.closed = true
	w.buf = nil
	return nil
}

var _ fs.WriteAborter = new(uploadWriter)
