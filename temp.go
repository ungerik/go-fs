package fs

import (
	"context"
	"os"
	"path"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// TempDir returns the temp directory of the operating system
func TempDir() File {
	return File(os.TempDir())
}

// TempFile returns a randomly named File with an optional extension
// in the temp directory of the operating system.
//
// The returned File does not exist yet, it's just a path, so two
// callers can in theory get the same path. Use CreateTempFile to
// atomically create a temporary file.
func TempFile(ext ...string) File {
	return TempDir().Join(fsimpl.RandomString() + strings.Join(ext, ""))
}

// CreateTempFile atomically creates a new empty file with a random name
// and an optional extension in the temp directory of the operating system,
// like os.CreateTemp, and returns it.
func CreateTempFile(ext ...string) (File, error) {
	f, err := os.CreateTemp("", "*"+strings.Join(ext, ""))
	if err != nil {
		return "", err
	}
	return File(f.Name()), f.Close()
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
	return File(dir), nil
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
// to the temp directory of the operating system
// using a random filename with the extension of the source file.
func TempFileCopy(ctx context.Context, source FileReader) (File, error) {
	data, err := source.ReadAll(ctx)
	if err != nil {
		return InvalidFile, err
	}
	f := TempFile(path.Ext(source.Name()))
	return f, f.WriteAll(ctx, data)
}
