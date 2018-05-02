package fs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_LocalFileSystemMakeAllDirs(t *testing.T) {
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
