package gitfs

import (
	"container/list"
	"sync"

	"github.com/ungerik/go-fs/gitfs/internal/object"
)

// blobCache keeps the content of the most recently used
// blobs up to a maximum number of bytes.
type blobCache struct {
	mtx     sync.Mutex
	maxSize int64
	size    int64
	closed  bool      // put drops the data after close
	lru     list.List // of *cacheEntry, most recently used first
	entries map[object.OID]*list.Element
}

type cacheEntry struct {
	oid  object.OID
	data []byte
}

func newBlobCache(maxSize int64) *blobCache {
	return &blobCache{maxSize: maxSize, entries: make(map[object.OID]*list.Element)}
}

// get returns the cached data of a blob, which must not be modified.
func (c *blobCache) get(oid object.OID) ([]byte, bool) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	element, ok := c.entries[oid]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(element)
	return element.Value.(*cacheEntry).data, true
}

// put adds the data of a blob, evicting the least recently used blobs
// to stay within the maximum size. Data larger than the maximum is not
// cached. The data must not be modified afterwards.
func (c *blobCache) put(oid object.OID, data []byte) {
	size := int64(len(data))
	if size > c.maxSize {
		return
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.closed {
		return
	}
	if element, ok := c.entries[oid]; ok {
		c.lru.MoveToFront(element)
		return
	}
	for c.size+size > c.maxSize {
		oldest := c.lru.Back()
		entry := c.lru.Remove(oldest).(*cacheEntry)
		delete(c.entries, entry.oid)
		c.size -= int64(len(entry.data))
	}
	c.entries[oid] = c.lru.PushFront(&cacheEntry{oid: oid, data: data})
	c.size += size
}

// close removes all cached blobs and drops the blobs of later calls
// of put, like those of fetches that finish after FileSystem.Close.
func (c *blobCache) close() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.closed = true
	c.lru.Init()
	clear(c.entries)
	c.size = 0
}
