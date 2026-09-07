package dropboxfs

import (
	"sync"
	"time"

	fs "github.com/ungerik/go-fs"
)

// fileInfoCache is a cache with timeout for FileInfo data.
type fileInfoCache struct {
	mtx     sync.Mutex
	infos   map[string]fileInfoCacheEntry
	timeout time.Duration
}

type fileInfoCacheEntry struct {
	*fs.FileInfo
	time time.Time
}

// newFileInfoCache returns a new fileInfoCache with timeout,
// or nil if timeout is zero. It is valid to call the methods
// of fileInfoCache for a nil pointer.
func newFileInfoCache(timeout time.Duration) *fileInfoCache {
	if timeout == 0 {
		return nil
	}
	return &fileInfoCache{
		infos:   make(map[string]fileInfoCacheEntry),
		timeout: timeout,
	}
}

// Put puts or updates a FileInfo for a path.
func (cache *fileInfoCache) Put(path string, info *fs.FileInfo) {
	if cache == nil {
		return
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	cache.infos[path] = fileInfoCacheEntry{
		FileInfo: info,
		time:     time.Now(),
	}
}

// Get returns the FileInfo for a path or nil and false
// if there is no FileInfo for the path or the FileInfo
// has timed out.
func (cache *fileInfoCache) Get(path string) (info *fs.FileInfo, ok bool) {
	if cache == nil {
		return nil, false
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	entry, ok := cache.infos[path]
	if !ok {
		return nil, false
	}
	if entry.time.Add(cache.timeout).Before(time.Now()) {
		delete(cache.infos, path)
		return nil, false
	}
	return entry.FileInfo, true
}

// Clear removes all cached FileInfos.
func (cache *fileInfoCache) Clear() {
	if cache == nil {
		return
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	clear(cache.infos)
}

// Delete deletes the FileInfo with path if was cached.
func (cache *fileInfoCache) Delete(path string) {
	if cache == nil {
		return
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	delete(cache.infos, path)
}
