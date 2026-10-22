package dropboxfs

import (
	"strings"
	"sync"
	"time"

	fs "github.com/ungerik/go-fs"
)

// cacheMaxEntries is the maximum number of FileInfos in a fileInfoCache.
const cacheMaxEntries = 10_000

// fileInfoCache is a cache with timeout for FileInfo data.
//
// The keys are the lower case paths, because Dropbox paths are
// case-insensitive. The cache is bounded: expired entries are evicted
// when entries are added, at most once per timeout, and if the cache
// holds maxEntries, the expired entries and if necessary a tenth of
// the other entries are evicted before an entry is added, so that
// the next entries are added without evicting again.
type fileInfoCache struct {
	mtx        sync.Mutex
	infos      map[string]fileInfoCacheEntry
	timeout    time.Duration
	maxEntries int
	nextSweep  time.Time
	now        func() time.Time // time.Now, replaced in tests
}

type fileInfoCacheEntry struct {
	info    fs.FileInfo
	expires time.Time
}

// newFileInfoCache returns a new fileInfoCache with timeout,
// or nil if timeout is not positive. It is valid to call the methods
// of fileInfoCache for a nil pointer.
func newFileInfoCache(timeout time.Duration) *fileInfoCache {
	if timeout <= 0 {
		return nil
	}
	return &fileInfoCache{
		infos:      make(map[string]fileInfoCacheEntry),
		timeout:    timeout,
		maxEntries: cacheMaxEntries,
		now:        time.Now,
	}
}

// cacheKey returns the key of a Dropbox API path.
func cacheKey(path string) string {
	return strings.ToLower(path)
}

// Put puts or updates a copy of the FileInfo for a path.
// A nil info is ignored.
func (cache *fileInfoCache) Put(path string, info *fs.FileInfo) {
	if cache == nil || info == nil {
		return
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	now := cache.now()
	if now.After(cache.nextSweep) || len(cache.infos) >= cache.maxEntries {
		cache.evictExpired(now)
		cache.nextSweep = now.Add(cache.timeout)
	}
	if len(cache.infos) >= cache.maxEntries {
		evict := len(cache.infos) - cache.maxEntries + 1 + cache.maxEntries/10
		for key := range cache.infos {
			if evict == 0 {
				break
			}
			delete(cache.infos, key)
			evict--
		}
	}
	cache.infos[cacheKey(path)] = fileInfoCacheEntry{
		info:    *info,
		expires: now.Add(cache.timeout),
	}
}

// evictExpired deletes the entries that expired before now.
func (cache *fileInfoCache) evictExpired(now time.Time) {
	for key, entry := range cache.infos {
		if now.After(entry.expires) {
			delete(cache.infos, key)
		}
	}
}

// Get returns a copy of the FileInfo for a path, or nil and false
// if there is no FileInfo for the path or the FileInfo
// has timed out.
func (cache *fileInfoCache) Get(path string) (info *fs.FileInfo, ok bool) {
	if cache == nil {
		return nil, false
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	key := cacheKey(path)
	entry, ok := cache.infos[key]
	if !ok {
		return nil, false
	}
	if cache.now().After(entry.expires) {
		delete(cache.infos, key)
		return nil, false
	}
	info = new(fs.FileInfo)
	*info = entry.info
	return info, true
}

// Delete deletes the cached FileInfos of path
// and of all paths below it.
func (cache *fileInfoCache) Delete(path string) {
	if cache == nil {
		return
	}
	cache.mtx.Lock()
	defer cache.mtx.Unlock()
	key := cacheKey(path)
	delete(cache.infos, key)
	prefix := key + "/"
	for k := range cache.infos {
		if strings.HasPrefix(k, prefix) {
			delete(cache.infos, k)
		}
	}
}
