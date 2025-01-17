package fs

import (
	"context"
	"fmt"
	"io"

	"github.com/ungerik/go-fs/fsimpl"
)

type FileReader interface {
	// String returns the name and meta information for the FileReader.
	String() string

	// Name returns the name of the file
	Name() string

	// Ext returns the extension of file name including the point, or an empty string.
	Ext() string

	// LocalPath returns the cleaned local file-system path of the file backing the FileReader,
	// or an empty string if the FileReader is not backed by a local file.
	LocalPath() string

	// Size returns the size of the file
	Size() int64

	// Exists returns if file or data for the implementation exists
	Exists() bool

	// CheckExists return an ErrDoesNotExist error
	// if the file does not exist.
	CheckExists() error

	// IsDir returns true if the file is a directory.
	IsDir() bool

	// CheckIsDir return an ErrDoesNotExist error
	// if the file does not exist, ErrEmptyPath
	// if the file path is empty, or ErrIsNotDirectory
	// if a file exists, but is not a directory,
	// or nil if the file is a directory.
	CheckIsDir() error

	// ContentHash returns the DefaultContentHash for the file.
	ContentHash() (string, error)

	// ContentHashContext returns the DefaultContentHash for the file.
	ContentHashContext(ctx context.Context) (string, error)

	// ReadAll reads and returns all bytes of the file
	ReadAll() (data []byte, err error)

	// ReadAllContext reads and returns all bytes of the file
	ReadAllContext(context.Context) (data []byte, err error)

	// ReadAllContentHash reads and returns all bytes of the file
	// together with the DefaultContentHash.
	ReadAllContentHash(context.Context) (data []byte, hash string, err error)

	// ReadAllString reads the complete file and returns the content as string.
	ReadAllString() (string, error)

	// ReadAllStringContext reads the complete file and returns the content as string.
	ReadAllStringContext(context.Context) (string, error)

	// WriteTo implements the io.WriterTo interface
	WriteTo(writer io.Writer) (n int64, err error)

	// OpenReader opens the file and returns a ReadCloser that has to be closed after reading
	OpenReader() (ReadCloser, error)

	// OpenReadSeeker opens the file and returns a ReadSeekCloser.
	// Use OpenReader if seeking is not necessary because implementations
	// may need additional buffering to support seeking or not support it at all.
	OpenReadSeeker() (ReadSeekCloser, error)

	// ReadJSON reads and unmarshalles the JSON content of the file to output.
	ReadJSON(ctx context.Context, output any) error

	// ReadXML reads and unmarshalles the XML content of the file to output.
	ReadXML(ctx context.Context, output any) error

	// GobEncode reads and gob encodes the file name and content,
	// implementing encoding/gob.GobEncoder.
	GobEncode() ([]byte, error)
}

// AsFileReaders converts the passed files to []FileReader.
// If the passed files are already of type []FileReader
// then they are returned as is.
func AsFileReaders[F FileReader](files []F) []FileReader {
	if len(files) == 0 {
		return nil
	}
	fileReaders, ok := any(files).([]FileReader)
	if ok {
		return fileReaders
	}
	fileReaders = make([]FileReader, len(files))
	for i, memFile := range files {
		fileReaders[i] = memFile
	}
	return fileReaders
}

// FileReaderNameIndex returns the slice index of the first FileReader
// with the passed filename returned from its Name method
// or -1 in case of no match.
func FileReaderNameIndex(fileReades []FileReader, filename string) int {
	for i, f := range fileReades {
		if f.Name() == filename {
			return i
		}
	}
	return -1
}

// FileReaderLocalPathIndex returns the slice index of the first FileReader
// with the passed localPath returned from its LocalPath method
// or -1 in case of no match.
func FileReaderLocalPathIndex(fileReades []FileReader, localPath string) int {
	for i, f := range fileReades {
		if f.LocalPath() == localPath {
			return i
		}
	}
	return -1
}

// FileReaderContentHashIndex returns the slice index of the first FileReader
// with the passed hash returned from its ContentHashContext method
// or -1 in case of no match.
func FileReaderContentHashIndex(ctx context.Context, fileReades []FileReader, hash string) (int, error) {
	for i, f := range fileReades {
		fFash, err := f.ContentHashContext(ctx)
		if err != nil {
			return -1, err
		}
		if fFash == hash {
			return i, nil
		}
	}
	return -1, nil
}

// FileReaderNotExistsIndex returns the slice index of the first FileReader
// where the Exists method returned false
// or -1 in case of no match.
func FileReaderNotExistsIndex(fileReades []FileReader, filename string) int {
	for i, f := range fileReades {
		if !f.Exists() {
			return i
		}
	}
	return -1
}

// FileReaderWithName returns a new FileReader that wraps the passed fileReader,
// but the Name() method returns the passed name instead of name of the wrapped fileReader.
func FileReaderWithName(fileReader FileReader, name string) FileReader {
	return &fileReaderWithName{FileReader: fileReader, name: name}
}

type fileReaderWithName struct {
	FileReader
	name string
}

// String implements the fmt.Stringer interface.
func (f *fileReaderWithName) String() string {
	return fmt.Sprintf("%s -> %s", f.name, f.FileReader.String())
}

func (f *fileReaderWithName) Name() string {
	return f.name
}

func (f *fileReaderWithName) Ext() string {
	return fsimpl.Ext(f.name, "")
}
