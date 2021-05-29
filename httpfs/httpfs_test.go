package httpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs"
)

func TestStat(t *testing.T) {
	osInfo, err := FileSystemTLS.Stat("domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png")
	assert.NoError(t, err)
	assert.False(t, osInfo.IsDir())
	assert.Greater(t, osInfo.Size(), int64(0), "file size greater zero")
	assert.NotZero(t, osInfo.ModTime(), "has modified time")

	info := fs.File("https://domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png").Info()
	assert.Equal(t, fs.NewFileInfo(osInfo, false), info)
}

func TestReadAll(t *testing.T) {
	data, err := FileSystemTLS.ReadAll("domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png")
	assert.NoError(t, err)
	assert.Greater(t, len(data), 0, "file size greater zero")

	data2, err := fs.File("https://domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png").ReadAll()
	assert.NoError(t, err)
	assert.Equal(t, data, data2)
}
