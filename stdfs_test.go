package fs

import (
	"testing"
	"testing/fstest"
)

func TestStdFS(t *testing.T) {
	err := fstest.TestFS(
		File(".").StdFS(),
		"stdfs_test.go",
		"stdfs.go",
		"go.mod",
		"go.sum",
		"LICENSE",
		"README.md",
	)
	if err != nil {
		t.Fatal(err)
	}
}
