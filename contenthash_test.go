package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentHashFuncFrom is a regression test: ContentHashFuncFrom used
// one hash.Hash for every call, so the second call returned the hash of
// both inputs, and concurrent calls raced on the hash state.
func TestContentHashFuncFrom(t *testing.T) {
	hashFunc := ContentHashFuncFrom(sha256.New)
	sum := sha256.Sum256([]byte("abc"))
	want := hex.EncodeToString(sum[:])

	for range 2 {
		got, err := hashFunc(t.Context(), strings.NewReader("abc"))
		require.NoError(t, err)
		require.Equal(t, want, got, "same input, same hash")
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := hashFunc(t.Context(), strings.NewReader("abc"))
			if err != nil || got != want {
				t.Errorf("concurrent hash = %q, %v; want %q", got, err, want)
			}
		})
	}
	wg.Wait()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := hashFunc(ctx, strings.NewReader("abc"))
	require.ErrorIs(t, err, context.Canceled)
}

func TestFileContentHash(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	got, err := FileContentHash(t.Context(), NewMemFile("a.txt", []byte("abc")), ContentHashFuncFrom(sha256.New))
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(sum[:]), got)

	_, err = FileContentHash(t.Context(), NewMemFile("dir/", nil), ContentHashFuncFrom(sha256.New))
	require.ErrorIs(t, err, ErrIsDirectory{})
}
