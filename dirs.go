package fs

import (
	"context"
	"errors"
	"os"
	"os/user"
)

// HomeDir returns the home directory of the current user.
//
// It first calls [os.UserHomeDir], which honors the $HOME environment
// variable on Unix and macOS, %USERPROFILE% on Windows, and $home on
// Plan 9. This lets tests, containers, and HOME=/other invocations
// override the resolved directory.
//
// If [os.UserHomeDir] returns an error (for example when the relevant
// environment variable is unset) HomeDir falls back to looking up the
// current user's home via [user.Current].
func HomeDir() File {
	if home, err := os.UserHomeDir(); err == nil {
		return File(home)
	}
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

// listDirMaxImpl implements the ListDirMax method functionality by calling listDir.
// It returns the passed max number of files or an unlimited number if max is < 0.
// FileSystem implementations can use this function to implement ListDirMax,
// if an own, specialized implementation doesn't make sense.
func listDirMaxImpl(ctx context.Context, max int, listDir func(ctx context.Context, callback func(File) error) error) (files []File, err error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if max == 0 {
		return nil, nil
	}
	done := errors.New("done") // used as an internal flag, won't be returned
	err = listDir(ctx, func(file File) error {
		if max >= 0 && len(files) >= max {
			return done
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
	if err != nil && !errors.Is(err, done) {
		return nil, err
	}
	return files, nil
}
