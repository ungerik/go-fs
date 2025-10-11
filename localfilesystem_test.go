package fs

import (
	"context"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalFileSystem(t *testing.T) {
	testDir := MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, testDir.RemoveRecursive(), "testDir.RemoveRecursive() should not return an error")
	})

	RunFileSystemTests(
		context.Background(),
		t,
		Local,               // fs
		"local file system", // name
		"file://",           // prefix
		testDir.LocalPath(), // testDir
	)
}

func Test_LocalFileSystem_MakeAllDirs(t *testing.T) {
	const testDir = "TestDir"
	File(testDir).RemoveRecursive()
	defer File(testDir).RemoveRecursive()

	localFileSystem := LocalFileSystem{
		DefaultCreatePermissions:    UserAndGroupReadWrite,
		DefaultCreateDirPermissions: UserAndGroupReadWrite,
	}

	err := localFileSystem.MakeDir(testDir, []Permissions{AllReadWrite})
	assert.NoError(t, err)
}

func Test_LocalFileSystem_SplitDirAndName(t *testing.T) {
	root := Local.Separator()

	dir, name := Local.SplitDirAndName(root)
	assert.Equal(t, root, dir)
	assert.Equal(t, "", name)

	dir, name = Local.SplitDirAndName(root + "FileInRoot")
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)

	dir, name = Local.SplitDirAndName(root + "FileInRoot" + Local.Separator())
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)
}

func Test_expandTilde(t *testing.T) {
	currentUser, err := user.Current()
	require.NoError(t, err, "user.Current() should not error")
	require.NotNil(t, currentUser, "currentUser should not be nil")
	require.NotEmpty(t, currentUser.HomeDir, "HomeDir should not be empty")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde only",
			input:    "~",
			expected: currentUser.HomeDir,
		},
		{
			name:     "tilde with path",
			input:    "~/Documents",
			expected: filepath.Join(currentUser.HomeDir, "Documents"),
		},
		{
			name:     "tilde with nested path",
			input:    "~/Documents/test/file.txt",
			expected: filepath.Join(currentUser.HomeDir, "Documents/test/file.txt"),
		},
		{
			name:     "no tilde at start",
			input:    "/usr/local/bin",
			expected: "/usr/local/bin",
		},
		{
			name:     "tilde in middle",
			input:    "/path/~/test",
			expected: "/path/~/test",
		},
		{
			name:     "relative path without tilde",
			input:    "relative/path",
			expected: "relative/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandTilde(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
