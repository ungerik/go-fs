package fs

import (
	"crypto/rand"
	"fmt"
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
// The returned File does not exist yet, it's just a path.
func TempFile(ext ...string) File {
	return TempDir().Join(fsimpl.RandomString() + strings.Join(ext, ""))
}

// MakeTempDir makes and returns a new randomly named sub directory in TempDir().
// Example:
//
//	tempDir, err := fs.MakeTempDir()
//	if err != nil {
//	    return err
//	}
//	defer tempDir.RemoveRecursive()
//	doThingsWith(tempDir)
func MakeTempDir() (File, error) {
	name, err := tempDirName()
	if err != nil {
		return "", err
	}
	dir := TempDir().Join(name)
	err = dir.MakeDir()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// MustMakeTempDir makes and returns a new randomly named sub directory in TempDir().
// It panics on errors.
// Example:
//
//	tempDir := fs.MustMakeTempDir()
//	defer tempDir.RemoveRecursive()
//	doThingsWith(tempDir)
func MustMakeTempDir() File {
	dir, err := MakeTempDir()
	if err != nil {
		panic(err)
	}
	return dir
}

func tempDirName() (string, error) {
	var randomBytes [4]byte
	_, err := rand.Read(randomBytes[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%X", time.Now().Format("20060102-150405"), randomBytes), nil
}

// TempFileCopy copies the provided source file
// to the temp directory of the operating system
// using a random filename with the extension of the source file.
func TempFileCopy(source FileReader) (File, error) {
	data, err := source.ReadAll()
	if err != nil {
		return InvalidFile, err
	}
	f := TempFile(path.Ext(source.Name()))
	return f, f.WriteAll(data)
}
