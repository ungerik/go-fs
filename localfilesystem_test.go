package fs

import (
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
		t.Context(),
		t,
		Local,               // fs
		"local file system", // name
		"file://",           // prefix
		testDir.LocalPath(), // testDir
	)
}

func Test_LocalFileSystem_MoveSameSrcDest(t *testing.T) {
	tmp := MustMakeTempDir()
	t.Cleanup(func() { _ = tmp.RemoveRecursive() })

	file := tmp.Join("a.txt")
	require.NoError(t, file.WriteAll([]byte("hello")))

	// File: Move(src, src) is a no-op, matching os.Rename.
	srcPath := file.LocalPath()
	require.NoError(t, Local.Move(srcPath, srcPath), "Move(file, file) must be a no-op")
	got, err := file.ReadAllString()
	require.NoError(t, err)
	assert.Equal(t, "hello", got, "file content preserved")

	// Same after cleaning (./a.txt should normalize to a.txt).
	noisy := filepath.Join(filepath.Dir(srcPath), ".", filepath.Base(srcPath))
	require.NoError(t, Local.Move(srcPath, noisy), "Move with redundant path components is still a no-op")
	require.True(t, file.Exists(), "file still present after no-op move with redundant path")

	// Directory: Move(dir, dir) is also a no-op (matches os.Rename, even
	// though our usual Move(dir, dir-that-exists) semantics would otherwise
	// append a base name).
	dir := tmp.Join("subdir")
	require.NoError(t, dir.MakeDir())
	dirPath := dir.LocalPath()
	require.NoError(t, Local.Move(dirPath, dirPath), "Move(dir, dir) must be a no-op")
	assert.True(t, dir.IsDir(), "directory still present after no-op move")
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

func Test_expandTilde_HonorsEnv(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv(homeEnvVar(), fakeHome)

	require.Equal(t, fakeHome, expandTilde("~"),
		"expandTilde(~) must use %s before falling back to user.Current", homeEnvVar())
	require.Equal(t, filepath.Join(fakeHome, "foo"), expandTilde("~/foo"))
	require.Equal(t, filepath.Join(fakeHome, "Documents/test/file.txt"),
		expandTilde("~/Documents/test/file.txt"))
}
