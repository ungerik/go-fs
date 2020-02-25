package fsimpl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const hashBlockSize = 4 * 1024 * 1024

// DropboxContentHash returns a Dropbox compatible 64 hex character content hash by reading from an io.Reader until io.EOF.
// See https://www.dropbox.com/developers/reference/content-hash
func DropboxContentHash(reader io.Reader) (string, error) {
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

type ContentHasher interface {
	Hash(reader io.Reader) (string, error)
}

type ContentHasherFunc func(reader io.Reader) (string, error)

func (f ContentHasherFunc) Hash(reader io.Reader) (string, error) {
	return f(reader)
}

var ContentHash = ContentHasherFunc(DropboxContentHash)

// ContentHashBytes returns a Dropbox compatible 64 hex character content hash for a byte slice.
// See https://www.dropbox.com/developers/reference/content-hash
func ContentHashBytes(buf []byte) string {
	// bytes.Reader.Read only ever returns io.EOF
	// which is not treatet as error by ContentHash
	// so we can ignore all returned errors
	hash, _ := ContentHash(bytes.NewReader(buf))
	return hash
}
