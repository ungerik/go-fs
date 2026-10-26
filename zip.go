package fs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ungerik/go-fs/internal/zipname"
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
// The entry of a MemFile is named after its cleaned FileName without
// a leading slash, which can be a slash separated relative path like
// "dir/file.txt", the entry of every other FileReader after its Name.
// So ZipMemFiles and Zip name the entries of MemFiles the same way and
// UnzipToMemFiles returns them with the same FileName.
//
// A name that is not valid in a zip archive is an error wrapping
// os.ErrInvalid, so UnzipToMemFiles can unzip every archive of Zip
// on the same operating system:
//   - an empty name, a name that escapes with "..", and the name of
//     another FileReader than a MemFile that is not a clean, slash
//     separated, relative path (see io/fs.ValidPath), like "/abs.txt"
//   - a name with a backslash, which UnzipToMemFiles rejects
//   - a name that is not valid UTF-8, which zip readers decode as CP-437
//   - a name with a control character like NUL, a line or paragraph
//     separator, or a bidirectional embedding, override or isolate,
//     which can disguise the extension of a name
//   - a name with a colon, which marks a drive letter on Windows
//   - a name longer than the 65535 bytes of a zip header
//
// Names that only Windows can't extract are not checked, like names
// ending in a dot or a space, names with one of the characters
// < > " | ? * and names that differ only in case. Device names like
// CON or NUL are only rejected on Windows, like by UnzipToMemFiles,
// which rejects an archive with such a name on Windows.
//
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
		err := addZipEntry(ctx, zipWriter, file, copyBuf)
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
// The entries are named after the FileName of the MemFiles.
// A FileName that is empty or not valid in a zip archive
// is an error wrapping os.ErrInvalid, see Zip.
func ZipMemFiles(files ...MemFile) ([]byte, error) {
	return Zip(context.Background(), AsFileReaders(files)...)
}

// addZipEntry adds file as entry to zipWriter,
// copyBuf is the buffer for copying its content.
func addZipEntry(ctx context.Context, zipWriter *zip.Writer, file FileReader, copyBuf []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", file.Name(), err)
	}
	var name string
	switch f := file.(type) {
	case MemFile:
		name = strings.TrimLeft(f.CleanPath(), "/")
	case *MemFile:
		name = strings.TrimLeft(f.CleanPath(), "/")
	default:
		name = file.Name()
	}
	// Error messages quote at most 200 characters of a name
	if name == "" {
		return fmt.Errorf("can't zip %.200q: %w: zip entry name is empty", errSubject(file), os.ErrInvalid)
	}
	err = zipname.Check(name, info.IsDir())
	if err != nil {
		return fmt.Errorf("can't zip %.200q: %w", errSubject(file), err)
	}
	if name == "." || !iofs.ValidPath(name) || !isLocalZipName(name) {
		return fmt.Errorf("can't zip %.200q: %w: zip entry name %.200q is not a clean, local, slash separated path", errSubject(file), os.ErrInvalid, name)
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
	err = copyBuffer(ctx, w, r, copyBuf)
	err = errors.Join(err, r.Close())
	if err != nil {
		return fmt.Errorf("failed to zip file %s: %w", name, err)
	}
	return nil
}

// isLocalZipName reports if the zip entry name is a relative,
// slash separated path that stays within the directory
// it is extracted to, see archive/zip.ErrInsecurePath.
// UnzipToMemFiles uses this rule and Zip a stricter one,
// so that every archive of Zip can be unzipped.
func isLocalZipName(name string) bool {
	return filepath.IsLocal(name) && !strings.Contains(name, `\`)
}

// UnzipToMemFiles unzips the passed zipFile as MemFiles.
//
// The FileName of a MemFile is the name of its zip entry,
// which is a slash separated relative path like "dir/file.txt".
// A name without the UTF-8 flag of the zip format is taken from
// the Info-ZIP Unicode Path extra field of the entry if it has one,
// else a name that is not valid UTF-8 is converted to UTF-8 from CP-437,
// the encoding of names without the flag.
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
		// Error messages quote at most 200 characters of a name
		name := zipname.Decode(&entry.FileHeader)
		if !isLocalZipName(name) {
			return nil, fmt.Errorf("%w: zip entry %.200q of %s", zip.ErrInsecurePath, name, zipFile.Name())
		}
		if entry.UncompressedSize64 > uint64(remaining) { //#nosec G115 -- remaining is never negative
			return nil, errUnzipTooBig(zipFile, maxSize)
		}
		r, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to read zip file %.200q: %w", name, err)
		}
		// Don't trust the uncompressed size of the header,
		// read one byte more than remaining to detect a bigger file
		data, err := ReadAll(ctx, io.LimitReader(r, remaining+1))
		err = errors.Join(err, r.Close())
		if err != nil {
			return nil, fmt.Errorf("failed to read zip file %.200q: %w", name, err)
		}
		remaining -= int64(len(data))
		if remaining < 0 {
			return nil, errUnzipTooBig(zipFile, maxSize)
		}
		memFiles = append(memFiles, MemFile{FileName: name, FileData: data})
	}
	return memFiles, nil
}

func errUnzipTooBig(zipFile FileReader, maxSize int64) error {
	return fmt.Errorf("zip file %s has more than %d uncompressed bytes", zipFile.Name(), maxSize)
}
