package fs

import (
	"io"
	"path/filepath"
	"strings"
	"time"
)

var (
	All     = make([]FileSystem, 0)
	Default FileSystem
)

func Choose(url string) FileSystem {
	for _, fs := range All {
		if strings.HasPrefix(url, fs.Prefix()) {
			return fs
		}
	}
	return Default
}

func Info(url string) File {
	return Choose(url).Info(url)
}

func Create(url string) (File, error) {
	return Choose(url).Create(url)
}

func CreateDir(url string) (File, error) {
	return Choose(url).CreateDir(url)
}

type FileSystem interface {
	Prefix() string
	Info(url string) File
	Create(url string) (File, error)
	CreateDir(url string) (File, error)
}

type File interface {
	URL() string
	Path() string
	Name() string
	Ext() string

	Exists() bool
	IsDir() bool
	Size() int64

	// see pipeline pattern http://blog.golang.org/pipelines
	ListDir(done <-chan struct{}) (<-chan File, <-chan error)
	ListDirMatch(pattern string, done <-chan struct{}) (<-chan File, <-chan error)

	ModTime() time.Time

	Readable() (user, group, all bool)
	SetReadable(user, group, all bool) error

	Writable() (user, group, all bool)
	SetWritable(user, group, all bool) error

	Executable() (user, group, all bool)
	SetExecutable(user, group, all bool) error

	User() string
	SetUser(user string) error

	Group() string
	SetGroup(user string) error

	OpenReader() (io.ReadCloser, error)
	OpenWriter() (io.WriteCloser, error)
	OpenReadWriter() (io.ReadWriteCloser, error)
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
