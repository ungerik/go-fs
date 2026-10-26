package fs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFile_TrimExtWindowsTrailingDot checks that TrimExt on Windows keeps
// a local name that would end with a dot or a space, which Windows removes:
// "report..zip" would become "report.", which is the file "report".
func TestFile_TrimExtWindowsTrailingDot(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"report..zip", "report .zip", "....zip"} {
		file := File(filepath.Join(dir, name))
		assert.Equal(t, file, file.TrimExt(), "TrimExt of %q", name)
	}
}
