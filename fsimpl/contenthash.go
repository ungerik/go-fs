package fsimpl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const hashBlockSize = 4 * 1024 * 1024 // 4MB

// DropboxContentHash returns a Dropbox compatible 64 hex character content hash
// by reading from an io.Reader until io.EOF or the ctx gets cancelled.
// See https://www.dropbox.com/developers/reference/content-hash
func DropboxContentHash(ctx context.Context, reader io.Reader) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	buf := make([]byte, hashBlockSize)
	resultHash := sha256.New()
	numReadBytes, err := reader.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if numReadBytes > 0 {
		bufHash := sha256.Sum256(buf[:numReadBytes])
		resultHash.Write(bufHash[:])
	}
	for numReadBytes == hashBlockSize && err == nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		numReadBytes, err = reader.Read(buf)
		if err != nil && err != io.EOF {
			return "", err
		}
		if numReadBytes > 0 {
			bufHash := sha256.Sum256(buf[:numReadBytes])
			resultHash.Write(bufHash[:])
		}
	}
	return hex.EncodeToString(resultHash.Sum(nil)), nil
}
