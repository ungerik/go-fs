package dropboxfs

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// newTestCache returns a cache with a clock that the test advances.
func newTestCache(timeout time.Duration, maxEntries int) (*fileInfoCache, *time.Time) {
	cache := newFileInfoCache(timeout)
	cache.maxEntries = maxEntries
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	return cache, &now
}

func TestFileInfoCache_Disabled(t *testing.T) {
	cache := newFileInfoCache(0)
	assert.Nil(t, cache)
	cache.Put("/a", &fs.FileInfo{Name: "a"})
	_, ok := cache.Get("/a")
	assert.False(t, ok)
	cache.Delete("/a")
}

func TestFileInfoCache_CaseInsensitive(t *testing.T) {
	cache, _ := newTestCache(time.Minute, 10)
	cache.Put("/Dir/File.txt", &fs.FileInfo{Name: "File.txt"})
	info, ok := cache.Get("/dir/FILE.TXT")
	require.True(t, ok)
	assert.Equal(t, "File.txt", info.Name)

	cache.Put("/a", nil)
	_, ok = cache.Get("/a")
	assert.False(t, ok, "nil is not cached")
}

func TestFileInfoCache_Expiry(t *testing.T) {
	cache, now := newTestCache(time.Minute, 10)
	cache.Put("/old", &fs.FileInfo{Name: "old"})
	*now = now.Add(30 * time.Second)
	cache.Put("/new", &fs.FileInfo{Name: "new"})
	*now = now.Add(31 * time.Second)

	_, ok := cache.Get("/old")
	assert.False(t, ok, "expired")
	_, ok = cache.Get("/new")
	assert.True(t, ok)

	// Expired entries are evicted by Put without a Get
	cache.Put("/expires", &fs.FileInfo{})
	*now = now.Add(2 * time.Minute)
	cache.Put("/trigger", &fs.FileInfo{})
	assert.Len(t, cache.infos, 1, "expired entries are evicted on Put")
}

// TestFileInfoCache_Bounded is the regression test for the unbounded
// cache, which kept every listed entry until it was read again.
func TestFileInfoCache_Bounded(t *testing.T) {
	cache, _ := newTestCache(time.Hour, 100)
	for i := range 1000 {
		cache.Put("/file"+strconv.Itoa(i), &fs.FileInfo{})
		require.LessOrEqual(t, len(cache.infos), 100)
	}
	_, ok := cache.Get("/file999")
	assert.True(t, ok, "the latest entry is kept")
}

func TestFileInfoCache_Delete(t *testing.T) {
	cache, _ := newTestCache(time.Minute, 10)
	for _, p := range []string{"/dir", "/dir/a", "/dir/sub/b", "/dirx", "/other"} {
		cache.Put(p, &fs.FileInfo{})
	}
	cache.Delete("/DIR")
	for p, want := range map[string]bool{"/dir": false, "/dir/a": false, "/dir/sub/b": false, "/dirx": true, "/other": true} {
		_, ok := cache.Get(p)
		assert.Equal(t, want, ok, p)
	}
}

func TestFileInfoCache_Copies(t *testing.T) {
	cache, _ := newTestCache(time.Minute, 10)
	info := &fs.FileInfo{Name: "a"}
	cache.Put("/a", info)
	info.Name = "changed after Put"
	got, _ := cache.Get("/a")
	assert.Equal(t, "a", got.Name)
	got.Name = "changed after Get"
	got, _ = cache.Get("/a")
	assert.Equal(t, "a", got.Name)
}
