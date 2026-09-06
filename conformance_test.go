package fs_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

func TestLocalFileSystem_Conformance(t *testing.T) {
	testDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, testDir.RemoveRecursive(), "testDir.RemoveRecursive() should not return an error")
	})

	fstest.RunConformance(t, fs.Local, fstest.Config{
		Name:    "local file system",
		Prefix:  "file://",
		TestDir: testDir.LocalPath(),
	})
}

func TestMemFileSystem_Conformance(t *testing.T) {
	for _, sep := range []string{`/`, `\`} {
		t.Run("separator "+sep, func(t *testing.T) {
			memFS, err := fs.NewMemFileSystem(sep)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, memFS.Close()) })

			testDir := sep + "test"
			require.NoError(t, memFS.MakeDir(testDir, nil), "creating test directory")

			fstest.RunConformance(t, memFS, fstest.Config{
				Name:    "memory file system",
				Prefix:  memFS.Prefix(),
				TestDir: testDir,
			})
		})
	}
}
