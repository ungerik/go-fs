package fs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Zip the passed files using flate.BestCompression.
func Zip(ctx context.Context, files ...FileReader) ([]byte, error) {
	buf := bytes.Buffer{}
	zipWriter := zip.NewWriter(&buf)
	zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var modified time.Time
		if f, ok := file.(interface{ Modified() time.Time }); ok {
			modified = f.Modified()
		}
		w, err := zipWriter.CreateHeader(
			&zip.FileHeader{
				Name:     file.Name(),
				Modified: modified,
				Method:   zip.Deflate,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create zip header for %s: %w", file.Name(), err)
		}
		r, err := file.OpenReader()
		if err != nil {
			return nil, fmt.Errorf("failed to open reader for %s: %w", file.Name(), err)
		}
		_, err = io.Copy(w, r)
		if err != nil {
			return nil, fmt.Errorf("failed to copy file %s: %w", file.Name(), err)
		}
		err = r.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to close reader for %s: %w", file.Name(), err)
		}
	}
	err := zipWriter.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close zip writer: %w", err)
	}
	return buf.Bytes(), nil
}

// UnzipToMemFiles unzips the passed zipFile as MemFiles.
func UnzipToMemFiles(ctx context.Context, zipFile FileReader) ([]MemFile, error) {
	fileReader, err := zipFile.OpenReadSeeker()
	if err != nil {
		return nil, fmt.Errorf("failed to open zip file %s: %w", zipFile.Name(), err)
	}
	defer fileReader.Close()

	zipReader, err := zip.NewReader(fileReader, zipFile.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to create zip reader for %s: %w", zipFile.Name(), err)
	}

	memFiles := make([]MemFile, len(zipReader.File))
	for i, zipFile := range zipReader.File {
		if strings.HasSuffix(zipFile.Name, "/") {
			continue
		}
		r, err := zipFile.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open zip file %s: %w", zipFile.Name, err)
		}
		memFiles[i], err = ReadAllMemFile(ctx, r, zipFile.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to read zip file %s: %w", zipFile.Name, err)
		}
		err = r.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to close zip file %s: %w", zipFile.Name, err)
		}
	}
	return memFiles, nil
}
