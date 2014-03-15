package fs

import (
// "fmt"
)

type ErrIsDirectory struct {
	File
}

func (err ErrIsDirectory) Error() string {
	return "file is a directory: " + err.File.URL()
}

type ErrIsNotDirectory struct {
	File
}

func (err ErrIsNotDirectory) Error() string {
	return "file is not a directory: " + err.File.URL()
}
