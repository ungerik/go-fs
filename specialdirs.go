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

// TempDir returns the temp directory of the operating system
func TempDir() File {
	return File(os.TempDir())
}

// TempFile returns a randomly named File with an optional extension
// in the temp directory of the operating system.
// The file does not exist yet, the returned File just contains the path.
func TempFile(ext ...string) File {
	return TempDir().Join(fsimpl.RandomString() + strings.Join(ext, ""))
}

// MakeTempDir makes and returns a new randomly named sub directory in TempDir().
// Example:
// tempDir, err := fs.MakeTempDir()
// if err != nil {
// 	return err
// }
// defer tempDir.RemoveRecursive()
// doThingsWith(tempDir)
func MakeTempDir() (File, error) {
	name, err := tempDirName()
	if err != nil {
		return "", err
	}
	tempDir := TempDir().Join(name)
	err = tempDir.MakeDir()
	if err != nil {
		return "", err
	}
	return tempDir, nil
}

func tempDirName() (string, error) {
	var randomBytes [4]byte
	_, err := rand.Read(randomBytes[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%X", time.Now().Format("20060102_150405_999999"), randomBytes), nil
}
