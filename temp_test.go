package fs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTempDir(t *testing.T) {
	require.True(t, TempDir().IsDir(), "temp directory exists")
}
