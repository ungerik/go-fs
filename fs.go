package fs

import (
	"errors"
	"io"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"
)

var (
	Registry []FileSystem
	Default  FileSystem
)

type WatchEvent struct {
	//todo
	Err error
}

func Choose(url string) FileSystem {
	for _, fs := range Registry {
		if strings.HasPrefix(url, fs.Prefix()) {
			return fs
		}
	}
	return Default
}

func Get(url string) File {
	return Choose(url).Get(url)
}

func Create(url string) (File, error) {
	return Choose(url).Create(url)
}

func CreateDir(url string) (File, error) {
	return Choose(url).CreateDir(url)
}

type FileSystem interface {
	Prefix() string
	Get(url string) File
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

	Watch() <-chan WatchEvent

	ListDir(callback func(File) error, patterns ...string) error

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
	OpenAppendWriter() (io.WriteCloser, error)
	OpenReadWriter() (io.ReadWriteCloser, error)
}

var endListDir = errors.New("endListDir")

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

func ReadFile(url string) ([]byte, error) {
	reader, err := Get(url).OpenReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ioutil.ReadAll(reader)
}

func WriteFile(url string, data []byte) error {
	writer, err := Get(url).OpenWriter()
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.Write(data)
	return err
}

func AppendFile(url string, data []byte) error {
	writer, err := Get(url).OpenAppendWriter()
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.Write(data)
	return err
}
