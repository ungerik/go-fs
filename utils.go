package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
)

// FileURLs returns the URLs of the passed files
func FileURLs(files []File) []string {
	fileURLs := make([]string, len(files))
	for i, file := range files {
		fileURLs[i] = file.URL()
	}
	return fileURLs
}

// FilePaths returns the FileSystem specific paths the passed files
func FilePaths(files []File) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path()
	}
	return paths
}

// FileNames returns the names of the passed files
func FileNames[T FileReader](files []T) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name()
	}
	return names
}

// FilesAsFileReaders converts a slice of File to a slice of FileReader
func FilesAsFileReaders(files []File) []FileReader {
	fileReaders := make([]FileReader, len(files))
	for i, file := range files {
		fileReaders[i] = file
	}
	return fileReaders
}

// FilesFromStrings returns Files for the given fileURIs.
func FilesFromStrings(fileURIs []string) []File {
	files := make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = File(fileURIs[i])
	}
	return files
}

// FileReadersFromStrings returns FileReaders for the given fileURIs.
func FileReadersFromStrings(fileURIs []string) []FileReader {
	fileReaders := make([]FileReader, len(fileURIs))
	for i := range fileURIs {
		fileReaders[i] = File(fileURIs[i])
	}
	return fileReaders
}

// FileInfoToFileCallback converts a File callback function
// into a FileInfo callback function that is calling
// the passed fileCallback with the FileInfo.File.
func FileInfoToFileCallback(fileCallback func(File) error) func(*FileInfo) error {
	return func(info *FileInfo) error {
		return fileCallback(info.File)
	}
}

// ReadHeader reads up to maxNumBytes from the beginning of the passed FileReader.
// If fr is a MemFile then a slice of its FileData is returned
// without copying it.
func ReadHeader(fr FileReader, maxNumBytes int) ([]byte, error) {
	if maxNumBytes <= 0 {
		return nil, nil
	}

	// Fast path for MemFile
	if memFile, ok := fr.(MemFile); ok {
		if len(memFile.FileData) <= maxNumBytes {
			return memFile.FileData, nil
		}
		return memFile.FileData[:maxNumBytes], nil
	}

	// Generic case
	r, err := fr.OpenReader()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	data := make([]byte, maxNumBytes)
	n, err := r.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		return data[:n], err
	}
	return data[:n], nil
}

// ReadHeaderString reads up to maxNumBytes as string from the beginning of the passed FileReader.
// If fr is a MemFile then a slice of its FileData will be used as fast path.
func ReadHeaderString(fr FileReader, maxNumBytes int) (string, error) {
	data, err := ReadHeader(fr, maxNumBytes)
	return string(data), err
}

// ReadAllContext reads all data from r until EOF is reached,
// another error is returned, or the context got canceled.
// It is identical to io.ReadAll except that
// it can be canceled via a context.
func ReadAllContext(ctx context.Context, r io.Reader) ([]byte, error) {
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
				return b, nil
			}
			return b, err
		}
	}
	return b, ctx.Err()
}

// WriteAllContext writes all data wo the to w
// with a cancelable context.
func WriteAllContext(ctx context.Context, w io.Writer, data []byte) error {
	const chunkSize = 4 * 1024 * 1024 // 4MB
	return writeAllContext(ctx, w, data, chunkSize)
}

func writeAllContext(ctx context.Context, w io.Writer, data []byte, chunkSize int) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := w.Write(data[:min(chunkSize, len(data))])
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// ExecutableFile returns a File for the executable that started the current process.
// It wraps os.ExecutableFile, see https://golang.org/pkg/os/#ExecutableFile
func ExecutableFile() File {
	exe, err := os.Executable()
	if err != nil {
		return InvalidFile
	}
	return File(exe)
}

func SourceFile() File {
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		return InvalidFile
	}
	return File(file)
}
