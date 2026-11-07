package gitfs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs/gitfs/internal/object"
)

func TestBlobCache(t *testing.T) {
	// The cache holds at most maxSize bytes and evicts
	// the least recently used blobs first
	c := newBlobCache(10)
	c.put("a", []byte("aaaa"))
	c.put("b", []byte("bbbb"))
	_, ok := c.get("a") // a is now used more recently than b
	assert.True(t, ok)
	c.put("c", []byte("cccc"))

	_, ok = c.get("b")
	assert.False(t, ok, "b is the least recently used blob")
	for _, oid := range []object.OID{"a", "c"} {
		_, ok := c.get(oid)
		assert.True(t, ok, oid)
	}
	assert.Equal(t, int64(8), c.size)

	// Putting a cached blob again doesn't count it twice
	c.put("a", []byte("aaaa"))
	assert.Equal(t, int64(8), c.size)
	assert.Equal(t, 2, c.lru.Len())

	// A blob larger than the cache is not cached and evicts nothing
	c.put("big", make([]byte, 11))
	_, ok = c.get("big")
	assert.False(t, ok)
	assert.Equal(t, int64(8), c.size)

	// After close the cache is empty and stays empty
	c.close()
	c.put("d", []byte("d"))
	_, ok = c.get("d")
	assert.False(t, ok)
	assert.Zero(t, c.size)
}
