package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"
)

type failContext struct {
	errAfter int
	err      error

	counter int
}

func (*failContext) Deadline() (deadline time.Time, ok bool) { return time.Time{}, false }
func (*failContext) Done() <-chan struct{}                   { return nil }
func (*failContext) Value(key any) any                       { return nil }

func (f *failContext) Err() error {
	f.counter++
	if f.counter > f.errAfter {
		return f.err
	}
	return nil
}

func Test_writeAllContext(t *testing.T) {
	type args struct {
		ctx       context.Context
		data      []byte
		chunkSize int
	}
	tests := []struct {
		name    string
		args    args
		wantW   string
		wantErr bool
	}{
		{name: " chunkSize2", args: args{ctx: t.Context(), data: []byte(""), chunkSize: 2}, wantW: "", wantErr: false},
		{name: "1 chunkSize2", args: args{ctx: t.Context(), data: []byte("1"), chunkSize: 2}, wantW: "1", wantErr: false},
		{name: "12 chunkSize2", args: args{ctx: t.Context(), data: []byte("12"), chunkSize: 2}, wantW: "12", wantErr: false},
		{name: "123 chunkSize2", args: args{ctx: t.Context(), data: []byte("123"), chunkSize: 2}, wantW: "123", wantErr: false},
		{name: "1234 chunkSize2", args: args{ctx: t.Context(), data: []byte("1234"), chunkSize: 2}, wantW: "1234", wantErr: false},
		{name: "12345 chunkSize2", args: args{ctx: t.Context(), data: []byte("12345"), chunkSize: 2}, wantW: "12345", wantErr: false},

		{name: " chunkSize3", args: args{ctx: t.Context(), data: []byte(""), chunkSize: 3}, wantW: "", wantErr: false},
		{name: "1 chunkSize3", args: args{ctx: t.Context(), data: []byte("1"), chunkSize: 3}, wantW: "1", wantErr: false},
		{name: "12 chunkSize3", args: args{ctx: t.Context(), data: []byte("12"), chunkSize: 3}, wantW: "12", wantErr: false},
		{name: "123 chunkSize3", args: args{ctx: t.Context(), data: []byte("123"), chunkSize: 3}, wantW: "123", wantErr: false},
		{name: "1234 chunkSize3", args: args{ctx: t.Context(), data: []byte("1234"), chunkSize: 3}, wantW: "1234", wantErr: false},
		{name: "12345 chunkSize3", args: args{ctx: t.Context(), data: []byte("12345"), chunkSize: 3}, wantW: "12345", wantErr: false},

		{name: "12345 chunkSize2 error", args: args{ctx: &failContext{errAfter: 1, err: errors.New("contextError")}, data: []byte("12345"), chunkSize: 2}, wantW: "12", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &bytes.Buffer{}
			if err := writeAllContext(tt.args.ctx, w, tt.args.data, tt.args.chunkSize); (err != nil) != tt.wantErr {
				t.Errorf("writeAllContext() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotW := w.String(); gotW != tt.wantW {
				t.Errorf("writeAllContext() = %v, want %v", gotW, tt.wantW)
			}
		})
	}
}

// oneByteFileReader is a FileReader whose reader
// returns only one byte per Read call.
type oneByteFileReader struct {
	MemFile
}

func (f oneByteFileReader) OpenReader() (ReadCloser, error) {
	return oneByteReadCloser{iotest.OneByteReader(bytes.NewReader(f.FileData))}, nil
}

type oneByteReadCloser struct{ io.Reader }

func (oneByteReadCloser) Close() error                 { return nil }
func (oneByteReadCloser) Stat() (iofs.FileInfo, error) { return nil, errors.New("not implemented") }

// TestReadHeader is a regression test: ReadHeader did a single Read,
// which may return fewer bytes than requested although more are available.
func TestReadHeader(t *testing.T) {
	ctx := t.Context()
	file := oneByteFileReader{NewMemFile("x.pdf", []byte("%PDF-1.7 hello"))}

	header, err := ReadHeader(ctx, file, 5)
	require.NoError(t, err)
	require.Equal(t, "%PDF-", string(header), "all requested bytes")

	header, err = ReadHeader(ctx, file, 100)
	require.NoError(t, err, "a file shorter than the header is no error")
	require.Equal(t, "%PDF-1.7 hello", string(header))

	str, err := ReadHeaderString(ctx, file, 4)
	require.NoError(t, err)
	require.Equal(t, "%PDF", str)

	header, err = ReadHeader(ctx, file, 0)
	require.NoError(t, err)
	require.Nil(t, header)

	// MemFile fast path
	header, err = ReadHeader(ctx, file.MemFile, 4)
	require.NoError(t, err)
	require.Equal(t, "%PDF", string(header))
	header, err = ReadHeader(ctx, file.MemFile, 100)
	require.NoError(t, err)
	require.Equal(t, "%PDF-1.7 hello", string(header))

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = ReadHeader(canceled, file, 5)
	require.ErrorIs(t, err, context.Canceled)
	_, err = ReadHeaderString(canceled, file.MemFile, 5)
	require.ErrorIs(t, err, context.Canceled, "also for the MemFile fast path")

	// Canceled after the check before opening and two one-byte reads
	errCanceled := errors.New("canceled while reading")
	header, err = ReadHeader(&failContext{errAfter: 3, err: errCanceled}, file, 5)
	require.ErrorIs(t, err, errCanceled)
	require.Equal(t, "%P", string(header), "the bytes read before the cancellation")
}

func TestExecutableFile(t *testing.T) {
	require.True(t, ExecutableFile().Exists(), "executable file for current process exists")
}

func TestSourceFile(t *testing.T) {
	require.True(t, SourceFile().Exists(), "source file for the call exists")
	require.Equal(t, "utils_test.go", SourceFile().Name(), "source file is that of the test")
}
