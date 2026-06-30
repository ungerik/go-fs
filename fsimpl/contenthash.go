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
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// io.ReadFull fills the whole block even if the underlying reader
		// returns the data in smaller chunks without io.EOF, which is legal
		// per io.Reader. Using reader.Read directly would split blocks at the
		// wrong boundaries for such readers and produce a wrong hash.
		numReadBytes, err := io.ReadFull(reader, buf)
		if numReadBytes > 0 {
			bufHash := sha256.Sum256(buf[:numReadBytes])
			resultHash.Write(bufHash[:])
		}
		switch err {
		case nil:
			// Full block read, continue with the next one
		case io.EOF, io.ErrUnexpectedEOF:
			// Reader exhausted (io.EOF on a block boundary, or
			// io.ErrUnexpectedEOF for the last partial block)
			return hex.EncodeToString(resultHash.Sum(nil)), nil
		default:
			return "", err
		}
	}
}
