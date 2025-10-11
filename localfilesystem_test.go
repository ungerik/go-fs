package fs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
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
