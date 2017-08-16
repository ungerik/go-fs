package fs

import "errors"

// ListDirMaxImpl implements the ListDirMax method functionality
// by calling listDir.
// FileSystem implementations can use this function to implement ListDirMax,
// if a own, specialized implementation doesn't make sense.
func ListDirMaxImpl(max int, listDir func(callback func(File) error) error) (files []File, err error) {
	errAbort := errors.New("errAbort") // used as an internal flag, won't be returned
	err = listDir(func(file File) error {
		if len(files) >= max {
			return errAbort
		}
		if files == nil {
			if max > 0 {
				files = make([]File, 0, max)
			} else {
				files = make([]File, 0, 32)
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil && err != errAbort {
		return nil, err
	}
	return files, nil
}

// ListDirRecursiveImpl can be used by FileSystem implementations to
// implement FileSystem.ListDirRecursive if it doesn't have an internal
// optimzed form of doing that.
func ListDirRecursiveImpl(fs FileSystem, dirPath string, callback func(File) error, patterns []string) error {
	return fs.ListDir(dirPath, func(f File) error {
		if f.IsDir() {
			err := f.ListDirRecursive(callback, patterns...)
			// Don't mind files that have been deleted while iterating
			if IsErrDoesNotExist(err) {
				err = nil
			}
			return err
		}
		match, err := fs.MatchAnyPattern(f.Name(), patterns)
		if match {
			err = callback(f)
		}
		return err
	}, nil)
}
