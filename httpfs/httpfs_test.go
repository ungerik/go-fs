package httpfs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ungerik/go-fs"
)

func TestStat(t *testing.T) {
	info := FileSystemTLS.Info("domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png")
	assert.True(t, info.Exists)
	assert.False(t, info.IsDir)
	assert.Greater(t, info.Size, int64(0), "file size greater zero")
	assert.NotZero(t, info.ModTime, "has modified time")

	info2 := fs.File("https://domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png").Info()
	assert.Equal(t, info, info2)
}

func TestReadAll(t *testing.T) {
	data, err := FileSystemTLS.ReadAll("domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png")
	assert.NoError(t, err)
	assert.Greater(t, len(data), 0, "file size greater zero")

	data2, err := fs.File("https://domonda.com/wp-content/uploads/2019/10/domonda-red@2x.png").ReadAll()
	assert.NoError(t, err)
	assert.Equal(t, data, data2)
}
