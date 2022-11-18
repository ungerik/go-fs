package fs

import (
	"context"
	"io"
)

// FilesToURLs returns the URLs of a slice of Files.
func FilesToURLs(files []File) []string {
	fileURLs := make([]string, len(files))
	for i, file := range files {
		fileURLs[i] = file.URL()
	}
	return fileURLs
}

// FilesToPaths returns the FileSystem specific paths of a slice of Files.
func FilesToPaths(files []File) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path()
	}
	return paths
}

// FilesToNames returns a string slice with the names pars from the files
func FilesToNames(files []File) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name()
	}
	return names
}

// FilesToFileReaders converts a slice of File to a slice of FileReader
func FilesToFileReaders(files []File) []FileReader {
	fileReaders := make([]FileReader, len(files))
	for i, file := range files {
		fileReaders[i] = file
	}
	return fileReaders
}

// StringsToFiles returns Files for the given fileURIs.
func StringsToFiles(fileURIs []string) []File {
	files := make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = File(fileURIs[i])
	}
	return files
}

// StringsToFileReaders returns FileReaders for the given fileURIs.
func StringsToFileReaders(fileURIs []string) []FileReader {
	fileReaders := make([]FileReader, len(fileURIs))
	for i := range fileURIs {
		fileReaders[i] = File(fileURIs[i])
	}
	return fileReaders
}

type FileCallback func(File) error

func (f FileCallback) FileInfoCallback(file File, info FileInfo) error {
	return f(file)
}

// ReadAllContext reads all data from r until EOF is reached,
// another error is returned, or the context got canceled.
// It is identical to io.ReadAll
// except that it can be canceled via a context.
func ReadAllContext(ctx context.Context, r io.Reader) ([]byte, error) {
	// Implementation
	b := make([]byte, 0, 512)
	for ctx.Err() == nil {
		if len(b) == cap(b) {
			// Add more capacity (let append pick how much).
			b = append(b, 0)[:len(b)]
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return b, err
		}
	}
	return nil, ctx.Err()
}
