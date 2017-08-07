package fs

/*
import (
	"errors"
	"path/filepath"
)


const endListDir = ConstError("endListDir")

// see pipeline pattern http://blog.golang.org/pipelines
func ListDir(dir File, done <-chan struct{}, patterns ...string) (<-chan File, <-chan error) {
	files := make(chan File, 64)
	errs := make(chan error, 1)

	go func() {
		defer close(files)

		callback := func(file File) error {
			select {
			case files <- file:
				return nil
			case <-done:
				return endListDir
			}
		}

		err := dir.ListDir(callback, patterns...)
		if err != nil && err != endListDir {
			errs <- err
		}
	}()

	return files, errs
}

// see pipeline pattern http://blog.golang.org/pipelines
func Match(pattern string, done <-chan struct{}, inFiles <-chan File, inErrs <-chan error) (<-chan File, <-chan error) {
	outFiles := make(chan File)
	outErrs := make(chan error, 1)

	go func() {
		defer close(outFiles)
		for {
			select {
			case file := <-inFiles:
				match, err := filepath.Match(pattern, file.Name())
				if err != nil {
					outErrs <- err
					return
				}
				if match {
					outFiles <- file
				}
			case err := <-inErrs:
				outErrs <- err
				return
			case <-done:
				return
			}
		}
	}()

	return outFiles, outErrs
}
*/
