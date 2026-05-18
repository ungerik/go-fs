package fs

import (
	"context"
	"reflect"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// homeEnvVar returns the environment variable name that [os.UserHomeDir]
// reads on the current platform: USERPROFILE on Windows, home on Plan 9,
// HOME everywhere else.
func homeEnvVar() string {
	switch runtime.GOOS {
	case "windows":
		return "USERPROFILE"
	case "plan9":
		return "home"
	default:
		return "HOME"
	}
}

func TestHomeDir(t *testing.T) {
	require.True(t, HomeDir().IsDir(), "home directory exists")
}

func TestHomeDir_HonorsEnv(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv(homeEnvVar(), fakeHome)
	require.Equal(t, File(fakeHome), HomeDir(),
		"HomeDir must read %s before falling back to user.Current", homeEnvVar())
}

func Test_listDirMaxImpl(t *testing.T) {
	ctx := t.Context()
	errCtx, cancel := context.WithCancel(t.Context())
	cancel()

	list := func(files ...File) func(ctx context.Context, callback func(File) error) error {
		return func(ctx context.Context, callback func(File) error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for _, file := range files {
				err := callback(file)
				if err != nil {
					return err
				}
			}
			return nil
		}
	}

	type args struct {
		ctx     context.Context
		max     int
		listDir func(ctx context.Context, callback func(File) error) error
	}
	tests := []struct {
		name      string
		args      args
		wantFiles []File
		wantErr   bool
	}{
		{name: "-1", args: args{ctx: ctx, max: -1, listDir: list("1", "2", "3")}, wantFiles: []File{"1", "2", "3"}},
		{name: "-1 no files", args: args{ctx: ctx, max: -1, listDir: list()}, wantFiles: nil},
		{name: "0", args: args{ctx: ctx, max: 0, listDir: list("1", "2", "3")}, wantFiles: nil},
		{name: "1", args: args{ctx: ctx, max: 1, listDir: list("1", "2", "3")}, wantFiles: []File{"1"}},
		{name: "2", args: args{ctx: ctx, max: 2, listDir: list("1", "2", "3")}, wantFiles: []File{"1", "2"}},

		{name: "context error", args: args{ctx: errCtx, max: -1, listDir: list("1", "2", "3")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFiles, err := listDirMaxImpl(tt.args.ctx, tt.args.max, tt.args.listDir)
			if (err != nil) != tt.wantErr {
				t.Errorf("ListDirMaxImpl() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotFiles, tt.wantFiles) {
				t.Errorf("ListDirMaxImpl() = %v, want %v", gotFiles, tt.wantFiles)
			}
		})
	}
}
