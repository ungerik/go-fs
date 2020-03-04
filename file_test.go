package fs

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs/fsimpl"
)

func TestInvalidFile(t *testing.T) {
	assert.False(t, InvalidFile.IsDir(), "InvalidFile does not exist")

	assert.Equal(t, InvalidFile, InvalidFile.Dir(), "dir of InvalidFile is still an InvalidFile")
	dir, name := InvalidFile.DirAndName()
	assert.Equal(t, InvalidFile, dir, "dir of InvalidFile is still an InvalidFile")
	assert.Equal(t, "", name, "name of InvalidFile is empty string")

	assert.Equal(t, InvalidFileSystem{}, InvalidFile.FileSystem(), "InvalidFile has an InvalidFileSystem")

	_, err := InvalidFile.OpenReader()
	assert.Equal(t, ErrInvalidFileSystem, err, "can't open InvalidFile")
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
	if !errors.Is(err, new(ErrIsNotDirectory)) {
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
