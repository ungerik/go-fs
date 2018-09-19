package fs

import "io"

type FileReader interface {
	// Name returns the name of the file
	Name() string

	// Size returns the size of the file
	Size() int64

	// ContentHash returns a Dropbox compatible content hash for the file.
	// See https://www.dropbox.com/developers/reference/content-hash
	ContentHash() (string, error)

	// ReadAll reads and returns all bytes of the file
	ReadAll() (data []byte, err error)

	// ReadAllContentHash reads and returns all bytes of the file
	// together with a Dropbox compatible content hash.
	// See https://www.dropbox.com/developers/reference/content-hash
	ReadAllContentHash() (data []byte, hash string, err error)

	// ReadAllString reads the complete file and returns the content as string.
	ReadAllString() (string, error)

	// OpenReader opens the file and returns a io.ReadCloser that has be closed after reading
	OpenReader() (io.ReadCloser, error)

	// OpenReadSeeker opens the file and returns a ReadSeekCloser.
	// Use OpenReader if seeking is not necessary because implementations
	// may need additional buffering to support seeking or not support it at all.
	OpenReadSeeker() (ReadSeekCloser, error)
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

func (f *fileReaderWithName) Name() string {
	return f.name
}
