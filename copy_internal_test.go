package fs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// modeInfo is an os.FileInfo that only has a file mode.
type modeInfo os.FileMode

func (m modeInfo) Name() string       { return "file" }
func (m modeInfo) Size() int64        { return 0 }
func (m modeInfo) Mode() os.FileMode  { return os.FileMode(m) }
func (m modeInfo) ModTime() time.Time { return time.Time{} }
func (m modeInfo) IsDir() bool        { return os.FileMode(m).IsDir() }
func (m modeInfo) Sys() any           { return nil }

// TestIsLocalSpecialFile is a regression test: a copy refused to replace
// every local file that os.Lstat doesn't describe as regular, but on Windows
// os.Lstat sets os.ModeIrregular for every reparse point that is no symbolic
// link, like the placeholders of OneDrive files, so CopyRecursive and Move
// failed over them. Only a device, a named pipe and a socket, which the copy
// would write into, must be refused, an irregular file is written like
// a regular one.
func TestIsLocalSpecialFile(t *testing.T) {
	tests := []struct {
		mode    os.FileMode
		special bool
	}{
		{0o644, false},
		{os.ModeIrregular | 0o644, false},
		{os.ModeNamedPipe | 0o600, true},
		{os.ModeSocket | 0o755, true},
		{os.ModeDevice | 0o660, true},
		{os.ModeDevice | os.ModeCharDevice | 0o666, true},
		{os.ModeCharDevice, true},
	}
	for _, tt := range tests {
		require.Equal(t, tt.special, isLocalSpecialFile(modeInfo(tt.mode)), "mode %s", tt.mode)
	}
}

// TestIsSpecialFile checks that isSpecialFile takes the file mode of the
// local path for a local file and IsRegular of the FileInfo for other file
// systems, which have no file mode of their own.
func TestIsSpecialFile(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("data"), 0o600))
	// IsRegular false like for a reparse point on Windows: the mode tells
	require.False(t, isSpecialFile(File(localPath), &FileInfo{IsRegular: false}), "a regular local file")

	mem, err := RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	file := mem.RootDir().Join("file.txt")
	require.True(t, isSpecialFile(file, &FileInfo{IsRegular: false}), "no regular file of another file system")
	require.False(t, isSpecialFile(file, &FileInfo{IsRegular: true}), "a regular file of another file system")
}
