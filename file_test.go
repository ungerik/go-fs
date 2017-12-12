package fs

import (
	"testing"

	uuid "github.com/satori/go.uuid"
)

func TestFile_MakeAllDirs(t *testing.T) {
	checkDir := func(dir File) {
		if !dir.Exists() {
			t.Fatalf("dir does not exist: %s", dir)
		}
		if !dir.IsDir() {
			t.Fatalf("not a directory: %s", dir)
		}
	}

	baseDir := TempDir()

	pathParts := make([]string, 5)
	for i := range pathParts {
		pathParts[i] = uuid.NewV4().String()
	}

	dir := baseDir.Relative(pathParts...)

	err := dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = dir.Remove()
	if err != nil {
		t.Fatal(err)
	}
	err = dir.Dir().Remove()
	if err != nil {
		t.Fatal(err)
	}

	err = dir.MakeAllDirs()
	if err != nil {
		t.Fatal(err)
	}
	checkDir(dir)

	err = baseDir.Relative(pathParts[0]).RemoveRecursive()
	if err != nil {
		t.Fatal(err)
	}
}
