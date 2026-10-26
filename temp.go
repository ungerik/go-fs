package fs

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// TempDir returns the temp directory of the operating system.
// A $TMPDIR that begins with a literal "~", which the environment didn't
// expand, is a directory below the current one like for [os.CreateTemp],
// not the home directory.
func TempDir() File {
	return localFile(os.TempDir())
}

// TempFile returns a randomly named File with the extension ext,
// which can be empty, in the temp directory of the operating system.
// InvalidFile is returned if ext contains a slash or the path separator
// of the operating system, because the File would not be in the temp
// directory, like os.CreateTemp rejects such a pattern.
//
// The returned File does not exist yet, it's just a path, so two
// callers can in theory get the same path. Use CreateTempFile to
// atomically create a temporary file.
func TempFile(ext string) File {
	if strings.ContainsAny(ext, "/"+Separator) {
		return InvalidFile
	}
	return TempDir().Join(fsimpl.RandomString() + ext)
}

// CreateTempFile atomically creates a new empty file with a random name
// and the extension ext, which can be empty, in the temp directory
// of the operating system, like os.CreateTemp, and returns it.
func CreateTempFile(ext string) (File, error) {
	f, err := os.CreateTemp("", "*"+ext)
	if err != nil {
		return InvalidFile, err
	}
	return localFile(f.Name()), f.Close()
}

// MakeTempDir makes and returns a new randomly named sub directory in TempDir()
// using os.MkdirTemp, so the directory is guaranteed to be new.
// Example:
//
//	tempDir, err := fs.MakeTempDir()
//	if err != nil {
//	    return err
//	}
//	defer tempDir.RemoveRecursive(ctx)
//	doThingsWith(tempDir)
func MakeTempDir() (File, error) {
	dir, err := os.MkdirTemp("", time.Now().Format("20060102-150405")+"_*")
	if err != nil {
		return "", err
	}
	return localFile(dir), nil
}

// MustMakeTempDir makes and returns a new randomly named sub directory in TempDir().
// It panics on errors.
// Example:
//
//	tempDir := fs.MustMakeTempDir()
//	defer tempDir.RemoveRecursive(ctx)
//	doThingsWith(tempDir)
func MustMakeTempDir() File {
	dir, err := MakeTempDir()
	if err != nil {
		panic(err)
	}
	return dir
}

// TempFileCopy copies the provided source file
// to a new file in the temp directory of the operating system
// that is created atomically with CreateTempFile,
// using a random filename with the extension of the source file.
// If copying fails, then the temp file is removed
// and InvalidFile is returned with the error.
func TempFileCopy(ctx context.Context, source FileReader) (File, error) {
	f, err := CreateTempFile(path.Ext(source.Name()))
	if err != nil {
		return InvalidFile, err
	}
	err = CopyFile(ctx, source, f)
	if err != nil {
		return InvalidFile, errors.Join(err, f.Remove())
	}
	return f, nil
}
