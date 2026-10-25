package fs

import (
	"context"
	"io"
	"os"
	"runtime"
)

// FileInfoToFileCallback converts a File callback function
// into a FileInfo callback function that is calling
// the passed fileCallback with the FileInfo.File.
func FileInfoToFileCallback(fileCallback func(File) error) func(*FileInfo) error {
	return func(info *FileInfo) error {
		return fileCallback(info.File)
	}
}

// ReadHeader reads up to maxNumBytes from the beginning of the passed FileReader.
// Fewer bytes are only returned for a file that is shorter than maxNumBytes.
// If fr is a MemFile then a slice of its FileData is returned
// without copying it.
// ctx is checked before the file is opened and between the reads like by
// ReadAll, a canceled ctx returns the bytes read so far with ctx.Err().
func ReadHeader(ctx context.Context, fr FileReader, maxNumBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

	// ReadAll reads until the limit or the end of the file and checks ctx
	// before every Read, a single Read may return fewer bytes than available
	return ReadAll(ctx, io.LimitReader(r, int64(maxNumBytes)))
}

// ReadHeaderString reads up to maxNumBytes as string from the beginning
// of the passed FileReader, see ReadHeader.
// If fr is a MemFile then a slice of its FileData will be used as fast path.
func ReadHeaderString(ctx context.Context, fr FileReader, maxNumBytes int) (string, error) {
	data, err := ReadHeader(ctx, fr, maxNumBytes)
	return string(data), err
}

// ReadAll reads all data from r until EOF is reached,
// another error is returned, or the context got canceled.
// It is identical to io.ReadAll except that
// it can be canceled via a context.
func ReadAll(ctx context.Context, r io.Reader) ([]byte, error) {
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

// WriteAll writes all of data to w
// with a cancelable context.
func WriteAll(ctx context.Context, w io.Writer, data []byte) error {
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

// SourceFile returns the file of the caller of the current function.
func SourceFile() File {
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		return InvalidFile
	}
	return File(file)
}
