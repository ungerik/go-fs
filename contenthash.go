package fs

import (
	"context"
	"encoding/hex"
	"hash"
	"io"

	"github.com/ungerik/go-fs/fsimpl"
)

// ContentHashFunc is used to return the string representation of a content hash
// by reading from an io.Reader until io.EOF or the context is cancelled.
type ContentHashFunc func(ctx context.Context, reader io.Reader) (string, error)

// DefaultContentHash configures the default content hash function
// used by methods like File.ContentHash and FileReader.ContentHash.
var DefaultContentHash ContentHashFunc = fsimpl.DropboxContentHash

// FileContentHash returns the hashFunc result for fileReader
func FileContentHash(ctx context.Context, fileReader FileReader, hashFunc ContentHashFunc) (string, error) {
	reader, err := fileReader.OpenReader()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return hashFunc(ctx, reader)
}

// ContentHashFuncFrom returns a ContentHashFunc that uses a standard hash.Hash
// implementation to return the hash sum as hex encoded string,
// for example ContentHashFuncFrom(sha256.New).
//
// Every call of the returned ContentHashFunc creates a new hash.Hash
// with newHash, so the function can be used concurrently.
func ContentHashFuncFrom(newHash func() hash.Hash) ContentHashFunc {
	const readBlockSize = 4 * 1024 * 1024 // 4MB

	return func(ctx context.Context, reader io.Reader) (string, error) {
		h := newHash()
		buf := make([]byte, readBlockSize)
		for {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			n, err := reader.Read(buf)
			if err != nil && err != io.EOF {
				return "", err
			}
			if n > 0 {
				_, err = h.Write(buf[:n])
				if err != nil {
					return "", err
				}
			}
			if err == io.EOF {
				return hex.EncodeToString(h.Sum(nil)), nil
			}
		}
	}
}
