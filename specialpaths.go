package fs

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/ungerik/go-fs/fsimpl"
)

// HomeDir returns the home directory of the current user.
func HomeDir() File {
	u, err := user.Current()
	if err != nil {
		return InvalidFile
	}
	return File(u.HomeDir)
}

// CurrentWorkingDir returns the current working directory of the process.
// In case of an erorr, Exists() of the result File will return false.
func CurrentWorkingDir() File {
	cwd, _ := os.Getwd()
	return File(cwd)
}

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
//   tempDir, err := fs.MakeTempDir()
//   if err != nil {
//       return err
//   }
//   defer tempDir.RemoveRecursive()
//   doThingsWith(tempDir)
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
//   tempDir := fs.MustMakeTempDir()
//   defer tempDir.RemoveRecursive()
//   doThingsWith(tempDir)
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

// Executable returns a File for the executable that started the current process.
// It wraps os.Executable, see https://golang.org/pkg/os/#Executable
func Executable() File {
	exe, err := os.Executable()
	if err != nil {
		return InvalidFile
	}
	return File(exe)
}
