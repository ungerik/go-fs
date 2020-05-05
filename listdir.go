package fs

import (
	"context"
	"errors"
)

// ListDirMaxImpl implements the ListDirMax method functionality
// by calling listDir.
// FileSystem implementations can use this function to implement ListDirMax,
// if a own, specialized implementation doesn't make sense.
func ListDirMaxImpl(ctx context.Context, max int, listDir func(ctx context.Context, callback func(File) error) error) (files []File, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	errAbort := errors.New("errAbort") // used as an internal flag, won't be returned
	err = listDir(ctx, func(file File) error {
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
// optimzed form of doing that.
func ListDirInfoRecursiveImpl(ctx context.Context, fs FileSystem, dirPath string, callback func(File, FileInfo) error, patterns []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fs.ListDirInfo(
		ctx,
		dirPath,
		func(file File, info FileInfo) error {
			if info.IsDir() {
				err := file.ListDirInfoRecursiveContext(ctx, callback, patterns...)
				// Don't mind files that have been deleted while iterating
				return RemoveErrDoesNotExist(err)
			}
			match, err := fs.MatchAnyPattern(info.Name(), patterns)
			if match {
				err = callback(file, info)
			}
			return err
		},
		nil,
	)
}
