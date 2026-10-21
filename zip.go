package fs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// UnzipToMemFilesMaxSize is the maximum total uncompressed size
// of the files that UnzipToMemFiles reads into memory.
// It protects against zip bombs, archives that are small
// but decompress to more data than fits into memory.
// Use the zipfs package to read bigger archives.
const UnzipToMemFilesMaxSize = 1 << 30 // 1 GiB

// zipCopyBufferSize is the size of the buffer that Zip copies files with.
const zipCopyBufferSize = 32 * 1024

// Zip the passed files using flate.BestCompression.
//
// The entry of a MemFile is named after its FileName without a leading
// slash, which can be a slash separated relative path like "dir/file.txt",
// the entry of every other FileReader after its Name.
// So ZipMemFiles and Zip name the entries of MemFiles the same way and
// UnzipToMemFiles returns them with the same FileName.
// A name that is empty or escapes with ".." is an error.
// Directories are added as directory entries without content.
//
// The context is checked while copying the content of the files.
func Zip(ctx context.Context, files ...FileReader) ([]byte, error) {
	buf := bytes.Buffer{}
	zipWriter := zip.NewWriter(&buf)
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	copyBuf := make([]byte, zipCopyBufferSize)
	for _, file := range files {
		err := zipFileReader(ctx, zipWriter, file, copyBuf)
		if err != nil {
			return nil, err
		}
	}
	err := zipWriter.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close zip writer: %w", err)
	}
	return buf.Bytes(), nil
}

// ZipMemFiles zips the passed MemFiles using flate.BestCompression.
// The entries are named after the FileName of the MemFiles, see Zip.
func ZipMemFiles(files ...MemFile) ([]byte, error) {
	return Zip(context.Background(), AsFileReaders(files)...)
}

// zipFileReader adds file to zipWriter.
func zipFileReader(ctx context.Context, zipWriter *zip.Writer, file FileReader, copyBuf []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := zipEntryName(file)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", file.Name(), err)
	}
	header := &zip.FileHeader{
		Name:     name,
		Modified: info.ModTime(),
		Method:   zip.Deflate,
	}
	if info.IsDir() {
		header.Name += "/"
		header.Method = zip.Store
		_, err = zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("failed to create zip header for %s: %w", name, err)
		}
		return nil
	}
	w, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("failed to create zip header for %s: %w", name, err)
	}
	r, err := file.OpenReader()
	if err != nil {
		return fmt.Errorf("failed to open reader for %s: %w", name, err)
	}
	_, err = io.CopyBuffer(w, ctxCheckingReader{ctx: ctx, reader: r}, copyBuf)
	err = errors.Join(err, r.Close())
	if err != nil {
		return fmt.Errorf("failed to zip file %s: %w", name, err)
	}
	return nil
}

// ctxCheckingReader returns the error of its context
// from Read once the context is done.
type ctxCheckingReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r ctxCheckingReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// zipEntryName returns the name of the zip entry for file,
// see Zip.
func zipEntryName(file FileReader) (string, error) {
	var name string
	switch f := file.(type) {
	case MemFile:
		name = strings.TrimLeft(f.CleanPath(), "/")
	case *MemFile:
		name = strings.TrimLeft(f.CleanPath(), "/")
	default:
		name = file.Name()
	}
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("invalid zip entry name %q for %s", name, file)
	}
	return name, nil
}

// UnzipToMemFiles unzips the passed zipFile as MemFiles.
//
// The FileName of a MemFile is the name of its zip entry,
// which is a slash separated relative path like "dir/file.txt".
// Only regular files are returned, directory entries
// and symbolic links are skipped.
//
// An entry name that is absolute, escapes with "..", or contains a backslash
// is an error, because writing the MemFile to a path made of the
// directory and the FileName would end up outside of the directory,
// see archive/zip.ErrInsecurePath.
// Reading more than UnzipToMemFilesMaxSize bytes in total is an error.
func UnzipToMemFiles(ctx context.Context, zipFile FileReader) ([]MemFile, error) {
	return unzipToMemFiles(ctx, zipFile, UnzipToMemFilesMaxSize)
}

// unzipToMemFiles implements UnzipToMemFiles
// with maxSize as maximum total uncompressed size.
func unzipToMemFiles(ctx context.Context, zipFile FileReader, maxSize int64) ([]MemFile, error) {
	fileReader, err := zipFile.OpenReadSeeker()
	if err != nil {
		return nil, fmt.Errorf("failed to open zip file %s: %w", zipFile.Name(), err)
	}
	defer fileReader.Close()

	zipReader, err := zip.NewReader(fileReader, zipFile.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to create zip reader for %s: %w", zipFile.Name(), err)
	}

	var memFiles []MemFile
	remaining := maxSize
	for _, entry := range zipReader.File {
		if !entry.Mode().IsRegular() {
			continue
		}
		if !filepath.IsLocal(entry.Name) || strings.Contains(entry.Name, `\`) {
			return nil, fmt.Errorf("%w: zip entry %q of %s", zip.ErrInsecurePath, entry.Name, zipFile.Name())
		}
		if entry.UncompressedSize64 > uint64(remaining) { //#nosec G115 -- remaining is never negative
			return nil, errUnzipTooBig(zipFile, maxSize)
		}
		// Don't trust the uncompressed size of the header,
		// read one byte more than remaining to detect a bigger file
		memFile, err := unzipMemFile(ctx, entry, remaining+1)
		if err != nil {
			return nil, fmt.Errorf("failed to read zip file %s: %w", entry.Name, err)
		}
		remaining -= memFile.Size()
		if remaining < 0 {
			return nil, errUnzipTooBig(zipFile, maxSize)
		}
		memFiles = append(memFiles, memFile)
	}
	return memFiles, nil
}

func errUnzipTooBig(zipFile FileReader, maxSize int64) error {
	return fmt.Errorf("zip file %s has more than %d uncompressed bytes", zipFile.Name(), maxSize)
}

// unzipMemFile reads up to limit bytes of a zip entry as MemFile.
func unzipMemFile(ctx context.Context, entry *zip.File, limit int64) (MemFile, error) {
	r, err := entry.Open()
	if err != nil {
		return MemFile{}, err
	}
	data, err := ReadAll(ctx, io.LimitReader(r, limit))
	err = errors.Join(err, r.Close())
	if err != nil {
		return MemFile{}, err
	}
	return MemFile{FileName: entry.Name, FileData: data}, nil
}
