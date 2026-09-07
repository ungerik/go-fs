package fs

import (
	"context"
	"testing"
)

func Test_MakeTempDir(t *testing.T) {
	tempDir, err := MakeTempDir()
	if err != nil {
		t.Fatalf("Error: %s", err)
	}

	defer tempDir.RemoveRecursive(context.Background())

	// fmt.Fprintln(os.Stderr, tempDir.Path())

	if !tempDir.IsDir() {
		t.Fatalf("Temp dir does not exist: '%s'", tempDir)
	}
}
