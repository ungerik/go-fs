package fs

import (
	"context"
	"errors"
)

// ListDirMaxImpl implements the ListDirMax method functionality by calling listDir.
// It returns the passed max number of files or an unlimited number if max is < 0.
// FileSystem implementations can use this function to implement ListDirMax,
// if an own, specialized implementation doesn't make sense.
func ListDirMaxImpl(ctx context.Context, max int, listDir func(ctx context.Context, callback func(File) error) error) (files []File, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	errAbort := errors.New("errAbort") // used as an internal flag, won't be returned
	err = listDir(ctx, func(file File) error {
		if max >= 0 && len(files) >= max {
			return errAbort
		}
		if files == nil {
			// Reserve space for files
			if max < 0 {
				files = make([]File, 0, 32)
			} else {
				files = make([]File, 0, max)
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil && !errors.Is(err, errAbort) {
		return nil, err
	}
	return files, nil
}

// // ListDirRecursiveImpl can be used by FileSystem implementations to
// // implement FileSystem.ListDirRecursive if it doesn't have an internal
// // optimzed form of doing that.
// func ListDirRecursiveImpl(fs FileSystem, dirPath string, callback func(File) error, patterns []string) error {
// 	return fs.ListDir(dirPath, func(f File) error {
// 		if f.IsDir() {
// 			err := f.ListDirRecursive(callback, patterns...)
// 			// Don't mind files that have been deleted while iterating
// 			return RemoveErrDoesNotExist(err)
// 		}
// 		match, err := fs.MatchAnyPattern(f.Name(), patterns)
// 		if match {
// 			err = callback(f)
// 		}
// 		return err
// 	}, nil)
// }

// ListDirInfoRecursiveImpl can be used by FileSystem implementations to
// implement FileSystem.ListDirRecursive if it doesn't have an internal
// optimzed implementation for it.
func ListDirInfoRecursiveImpl(ctx context.Context, fs FileSystem, dirPath string, callback func(FileInfo) error, patterns []string) error {
	return fs.ListDirInfo(
		ctx,
		dirPath,
		func(info FileInfo) error {
			if info.IsDir {
				err := info.File.ListDirInfoRecursiveContext(ctx, callback, patterns...)
				// Don't mind files that have been deleted while iterating
				return RemoveErrDoesNotExist(err)
			}
			match, err := fs.MatchAnyPattern(info.Name, patterns)
			if !match || err != nil {
				return err
			}
			return callback(info)
		},
		nil, // No patterns
	)
}
