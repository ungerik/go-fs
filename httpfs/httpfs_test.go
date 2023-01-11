package httpfs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs"
)

func TestStat(t *testing.T) {
	osInfo, err := FileSystemTLS.Stat("raw.githubusercontent.com/ungerik/go-fs/master/README.md")
	assert.NoError(t, err)
	assert.False(t, osInfo.IsDir())
	assert.Greater(t, osInfo.Size(), int64(0), "file size greater zero")
	assert.NotZero(t, osInfo.ModTime(), "has modified time")

	info := fs.File("https://raw.githubusercontent.com/ungerik/go-fs/master/README.md").Info()
	assert.Equal(t, fs.NewFileInfo(osInfo, false), info)
}

func TestReadAll(t *testing.T) {
	data, err := FileSystemTLS.ReadAll(context.Background(), "raw.githubusercontent.com/ungerik/go-fs/master/README.md")
	assert.NoError(t, err)
	assert.Greater(t, len(data), 0, "file size greater zero")

	data2, err := fs.File("https://raw.githubusercontent.com/ungerik/go-fs/master/README.md").ReadAll(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, data, data2)
}
