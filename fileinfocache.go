package fs

import "time"

// FileInfoCache is a cache with timeout for FileInfo data.
type FileInfoCache struct {
	infos   map[string]fileInfoCacheEntry
	timeout time.Duration
}

type fileInfoCacheEntry struct {
	FileInfo
	time time.Time
}

// NewFileInfoCache returns a new FileInfoCache with timeout,
// or nil if timeout is zero. It is valid to call the methods
// of FileInfoCache for a nil pointer.
func NewFileInfoCache(timeout time.Duration) *FileInfoCache {
	if timeout == 0 {
		return nil
	}
	return &FileInfoCache{
		infos:   make(map[string]fileInfoCacheEntry),
		timeout: timeout,
	}
}

// Put puts or updates a FileInfo for a path.
func (cache *FileInfoCache) Put(path string, info FileInfo) {
	if cache == nil {
		return
	}
	cache.infos[path] = fileInfoCacheEntry{
		FileInfo: info,
		time:     time.Now(),
	}
}

// Get returns the FileInfo for a path or nil and false
// if there is no FileInfo for the path or the FileInfo
// has timed out.
func (cache *FileInfoCache) Get(path string) (info FileInfo, ok bool) {
	if cache == nil {
		return nil, false
	}
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

// Delete deletes the FileInfo with path if was cached.
func (cache *FileInfoCache) Delete(path string) {
	if cache == nil {
		return
	}
	delete(cache.infos, path)
}
