package fs

import (
	"context"
	"fmt"
	"io"
	"io/fs"

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

	// OpenReader opens the file and returns a io/fs.File that has be closed after reading
	OpenReader() (fs.File, error)

	// OpenReadSeeker opens the file and returns a ReadSeekCloser.
	// Use OpenReader if seeking is not necessary because implementations
	// may need additional buffering to support seeking or not support it at all.
	OpenReadSeeker() (ReadSeekCloser, error)

	// ReadJSON reads and unmarshalles the JSON content of the file to output.
	ReadJSON(ctx context.Context, output interface{}) error

	// ReadXML reads and unmarshalles the XML content of the file to output.
	ReadXML(ctx context.Context, output interface{}) error

	// GobEncode reads and gob encodes the file name and content,
	// implementing encoding/gob.GobEncoder.
	GobEncode() ([]byte, error)
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
