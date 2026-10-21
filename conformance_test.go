package fs_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

func TestLocalFileSystem_Conformance(t *testing.T) {
	testDir := fs.MustMakeTempDir()
	t.Cleanup(func() {
		assert.NoError(t, testDir.RemoveRecursive(context.Background()), "testDir.RemoveRecursive(context.Background()) should not return an error")
	})

	fstest.RunConformance(t, fs.Local, fstest.Config{
		Name:    "local file system",
		Prefix:  "file://",
		TestDir: testDir.LocalPath(),
	})
}

func TestMemFileSystem_Conformance(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts fs.MemFileSystemOptions
	}{
		{"separator /", fs.MemFileSystemOptions{Separator: "/"}},
		{`separator \`, fs.MemFileSystemOptions{Separator: `\`}},
		{"volume C:", fs.MemFileSystemOptions{Separator: `\`, Volume: "C:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memFS, err := fs.RegisteredTestMemFileSystem(&tc.opts)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, memFS.Close()) })

			testDir := memFS.CleanPath(tc.opts.Volume + tc.opts.Separator + "test")
			require.NoError(t, memFS.MakeDir(testDir, 0), "creating test directory")

			fstest.RunConformance(t, memFS, fstest.Config{
				Name:    "memory file system",
				Prefix:  memFS.Prefix(),
				TestDir: testDir,
			})
		})
	}
}
