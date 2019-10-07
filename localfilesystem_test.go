package fs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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

func Test_LocalFileSystem_DirAndName(t *testing.T) {
	root := Local.Separator()

	dir, name := Local.DirAndName(root)
	assert.Equal(t, root, dir)
	assert.Equal(t, "", name)

	dir, name = Local.DirAndName(root + "FileInRoot")
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)

	dir, name = Local.DirAndName(root + "FileInRoot" + Local.Separator())
	assert.Equal(t, root, dir)
	assert.Equal(t, "FileInRoot", name)
}
