package fs

import (
	"fmt"
	"io"
	"path"
	"path/filepath"
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

// MatchAnyPattern returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via path.Match
func MatchAnyPattern(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := path.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

// MatchAnyPatternLocal returns true if name matches any of patterns,
// or if len(patterns) == 0.
// The match per pattern is checked via filepath.Match
func MatchAnyPatternLocal(name string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	for _, pattern := range patterns {
		match, err := filepath.Match(pattern, name)
		if match || err != nil {
			return match, err
		}
	}
	return false, nil
}

// ListDirMaxImpl implements the ListDirMax method functionality
// by calling fs.ListDir.
// FileSystem implementations can use this function to implement ListDirMax,
// if a own, specialized implementation doesn't make sense.
func ListDirMaxImpl(fs FileSystem, filePath string, n int, patterns ...string) (files []File, err error) {
	if !fs.IsDir(filePath) {
		return nil, ErrIsNotDirectory{File(filePath)}
	}
	if n == -1 {
		files = make([]File, 0)
	} else {
		files = make([]File, 0, n)
	}
	err = fs.ListDir(filePath, func(file File) error {
		if n == -1 || len(files) < n {
			files = append(files, file)
		}
		return nil
	}, patterns...)
	if err != nil {
		return nil, err
	}
	return files, nil
}
