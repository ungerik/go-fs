package fs

import (
	"fmt"
	"io"
)

const copyBufferSize = 1024 * 1024

func copy(src, dest File, patterns []string, buf *[]byte) error {
	if !src.IsDir() {
		// Just copy one file
		if dest.IsDir() {
			dest = dest.Relative(src.Name())
		} else {
			err := dest.Dir().MakeDir()
			if err != nil {
				return err
			}
		}

		r, err := src.OpenReader()
		if err != nil {
			return err
		}
		defer r.Close()

		w, err := dest.OpenWriter(src.Permissions())
		if err != nil {
			return err
		}
		defer w.Close()

		if *buf == nil {
			*buf = make([]byte, copyBufferSize)
		}
		_, err = io.CopyBuffer(w, r, *buf)
		return err
	}

	if dest.Exists() && !dest.IsDir() {
		return fmt.Errorf("Can't copy a directory (%s) over a file (%s)", src.URL(), dest.URL())
	}

	// No error if dest is already a dir
	err := dest.MakeDir()
	if err != nil {
		return err
	}

	// Copy directories recursive
	return src.ListDir(func(file File) error {
		return copy(file, dest.Relative(file.Name()), patterns, buf)
	}, patterns...)
}

// Copy copies even between files of different file systems
func Copy(src, dest File, patterns ...string) error {
	var buf []byte
	return copy(src, dest, patterns, &buf)
}

// CopyPath copies even between files of different file systems
func CopyPath(src, dest string, patterns ...string) error {
	var buf []byte
	return copy(GetFile(src), GetFile(dest), patterns, &buf)
}
