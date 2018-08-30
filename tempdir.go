package fs

import (
	"crypto/rand"
	"fmt"
	"time"

	"os"
)

// TempDir returns the temp directory of the operating system
func TempDir() File {
	return File(os.TempDir())
}

// MakeTempDir makes and returns a new randomly named sub directory in TempDir().
// Example:
// tempDir, err := MakeTempDir()
// if err != nil {
// 	return err
// }
// defer tempDir.RemoveDirContentsRecursive()
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
