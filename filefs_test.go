package fs

import (
	"testing"
	"testing/fstest"
)

func TestFileFS(t *testing.T) {
	err := fstest.TestFS(
		File(".").AsFS(),
		"filefs_test.go",
		"filefs.go",
		"go.mod",
		"go.sum",
		"LICENSE",
		"README.md",
	)
	if err != nil {
		t.Fatal(err)
	}
}
