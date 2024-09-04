package fs

import (
	"bytes"
	"context"
	"errors"
	"testing"
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
		{name: " chunkSize2", args: args{ctx: context.Background(), data: []byte(""), chunkSize: 2}, wantW: "", wantErr: false},
		{name: "1 chunkSize2", args: args{ctx: context.Background(), data: []byte("1"), chunkSize: 2}, wantW: "1", wantErr: false},
		{name: "12 chunkSize2", args: args{ctx: context.Background(), data: []byte("12"), chunkSize: 2}, wantW: "12", wantErr: false},
		{name: "123 chunkSize2", args: args{ctx: context.Background(), data: []byte("123"), chunkSize: 2}, wantW: "123", wantErr: false},
		{name: "1234 chunkSize2", args: args{ctx: context.Background(), data: []byte("1234"), chunkSize: 2}, wantW: "1234", wantErr: false},
		{name: "12345 chunkSize2", args: args{ctx: context.Background(), data: []byte("12345"), chunkSize: 2}, wantW: "12345", wantErr: false},

		{name: " chunkSize3", args: args{ctx: context.Background(), data: []byte(""), chunkSize: 3}, wantW: "", wantErr: false},
		{name: "1 chunkSize3", args: args{ctx: context.Background(), data: []byte("1"), chunkSize: 3}, wantW: "1", wantErr: false},
		{name: "12 chunkSize3", args: args{ctx: context.Background(), data: []byte("12"), chunkSize: 3}, wantW: "12", wantErr: false},
		{name: "123 chunkSize3", args: args{ctx: context.Background(), data: []byte("123"), chunkSize: 3}, wantW: "123", wantErr: false},
		{name: "1234 chunkSize3", args: args{ctx: context.Background(), data: []byte("1234"), chunkSize: 3}, wantW: "1234", wantErr: false},
		{name: "12345 chunkSize3", args: args{ctx: context.Background(), data: []byte("12345"), chunkSize: 3}, wantW: "12345", wantErr: false},

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

func TestExecutableFile(t *testing.T) {
	require.True(t, ExecutableFile().Exists(), "executable file for current process exists")
}

func TestSourceFile(t *testing.T) {
	require.True(t, SourceFile().Exists(), "source file for the call exists")
	require.Equal(t, "utils_test.go", SourceFile().Name(), "source file is that of the test")
}
