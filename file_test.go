package fs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ungerik/go-fs/fsimpl"
)

func TestInvalidFile(t *testing.T) {
	require.False(t, InvalidFile.IsDir(), "InvalidFile does not exist")

	require.Equal(t, InvalidFile, InvalidFile.Dir(), "dir of InvalidFile is still an InvalidFile")
	dir, name := InvalidFile.DirAndName()
	require.Equal(t, InvalidFile, dir, "dir of InvalidFile is still an InvalidFile")
	require.Equal(t, "", name, "name of InvalidFile is empty string")

	require.Equal(t, InvalidFileSystem(""), InvalidFile.FileSystem(), "InvalidFile has an InvalidFileSystem")

	_, err := InvalidFile.OpenReader()
	require.Equal(t, ErrEmptyPath, err, "can't open InvalidFile")
}

func TestFileMakeAllDirs(t *testing.T) {
	checkDir := func(dir File) {
		if !dir.Exists() {
			t.Fatalf("dir does not exist: %s", dir)
		}
		if !dir.IsDir() {
			t.Fatalf("not a directory: %s", dir)
		}
	}

	baseDir := TempDir()

	err := baseDir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}

	file := baseDir.Join(fsimpl.RandomString())
	err = file.Touch()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Remove()

	err = file.MakeAllDirs()
	if !errors.As(err, new(ErrIsNotDirectory)) {
		t.Fatalf("should be ErrIsNotDirectory but is: %s", err)
	}

	pathParts := make([]string, 5)
	for i := range pathParts {
		pathParts[i] = fsimpl.RandomString()
	}

	dir := baseDir.Join(pathParts...)

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}
	err = dir.Dir().Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = baseDir.Join(pathParts[0]).RemoveRecursive()
	if err != nil {
		t.Fatal(err)
	}
}

func Test_FileJoin(t *testing.T) {
	exptectedPaths := []string{
		"/1/2/3/4/5",
		"/1/2/3/4",
		"/1/2/3",
		"/1/2",
		"/1",
		"/",
		"/",
	}

	f := File("/").Join("1", "2", "3", "4", "5")

	for _, exp := range exptectedPaths {
		assert.Equal(t, exp, f.LocalPath())
		// Up one directory
		f = f.Dir()
	}
}

func TestFile_Ext(t *testing.T) {
	tests := []struct {
		file File
		want string
	}{
		{file: "image.png", want: ".png"},
		{file: "image.66.png", want: ".png"},
		{file: "image", want: ""},
		{file: JoinCleanFilePath("dir.with.ext", "file"), want: ""},
		{file: JoinCleanFilePath("dir.with.ext", "file.ext"), want: ".ext"},
	}
	for _, tt := range tests {
		t.Run(string(tt.file), func(t *testing.T) {
			if got := tt.file.Ext(); got != tt.want {
				t.Errorf("File.Ext() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_TrimExt(t *testing.T) {
	tests := []struct {
		file File
		want File
	}{
		{file: "image.png", want: "image"},
		{file: "image.66.png", want: "image.66"},
		{file: "image", want: "image"},
		{file: JoinCleanFilePath("dir.with.ext", "file"), want: JoinCleanFilePath("dir.with.ext", "file")},
		{file: JoinCleanFilePath("dir.with.ext", "file.ext"), want: JoinCleanFilePath("dir.with.ext", "file")},
	}
	for _, tt := range tests {
		t.Run(string(tt.file), func(t *testing.T) {
			if got := tt.file.TrimExt(); got != tt.want {
				t.Errorf("File.TrimExt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFile_Watch(t *testing.T) {
	Local.WatchEventLogger = LoggerFunc(func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	Local.WatchErrorLogger = LoggerFunc(func(format string, args ...any) {
		t.Errorf(format, args...)
	})
	const sleepDurationForCallback = time.Millisecond * 10
	var (
		dir       = File(t.TempDir())
		gotFiles  []File
		gotEvents []Event
	)
	cancel, err := dir.Watch(func(file File, event Event) {
		gotFiles = append(gotFiles, file)
		gotEvents = append(gotEvents, event)
	})
	require.NoError(t, err, "dir.Watch")

	newFile := dir.Join("newFile")
	err = newFile.Touch()
	require.NoError(t, err, "newFile.Touch")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	renamedFile, err := newFile.Rename("renamedFile")
	require.NoError(t, err, "newFile.Rename")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	err = renamedFile.Remove()
	require.NoError(t, err, "renamedFile.Remove")

	time.Sleep(sleepDurationForCallback) // Give goroutines time for callback

	assert.Equal(t, []File{newFile, renamedFile, newFile, renamedFile}, gotFiles)
	assert.Equal(t, []Event{eventCreate, eventCreate, eventRename, eventRemove}, gotEvents)

	err = cancel()
	assert.NoError(t, err, "cancel watch")
}

func TestFile_ListDirInfoRecursiveContext(t *testing.T) {
	type args struct {
		ctx      context.Context
		callback func(*FileInfo) error
		patterns []string
	}
	tests := []struct {
		name    string
		file    File
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.file.ListDirInfoRecursiveContext(tt.args.ctx, tt.args.callback, tt.args.patterns...); (err != nil) != tt.wantErr {
				t.Errorf("File.ListDirInfoRecursiveContext() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFile_String(t *testing.T) {
	path := filepath.Join("dir", "file.ext")
	require.Equal(t, path+" (local file system)", File(path).String())
}
