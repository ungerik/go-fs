package fs

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/ungerik/go-fs/fsimpl"
)

func randFileCount() int {
	return 5 + int(rand.Float64()*10)
}

func randDirCount() int {
	return 2 + int(rand.Float64()*5)
}

func writeRandomFileContent(file File) error {
	size := 1 + int(rand.Float64()*1024*1024)
	buffer := make([]byte, size)
	rand.Read(buffer)
	return file.WriteAll(buffer)
}

func writeEmptyFile(file File) error {
	return file.WriteAllString("")
}

func deleteRandomFileInDir(dir File) error {
	var files []File
	err := dir.ListDirInfo(func(file File, info FileInfo) error {
		if !info.IsDir {
			files = append(files, file)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("no files")
	}
	i := int(rand.Float64() * float64(len(files)))
	return files[i].Remove()
}

func deleteRandomSubDir(dir File) error {
	var dirs []File
	err := dir.ListDirInfo(func(file File, info FileInfo) error {
		if info.IsDir {
			dirs = append(dirs, file)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return errors.New("no dirs")
	}
	i := int(rand.Float64() * float64(len(dirs)))
	return dirs[i].RemoveRecursive()
}

func writeRandomDirFiles(dir File, subDirDepth int) (err error) {
	numFiles := randFileCount()
	for i := 0; i < numFiles; i++ {
		file := dir.Join(fsimpl.RandomString() + ".bin")
		if i == 0 {
			// always write one empty file
			err = writeEmptyFile(file)
		} else {
			err = writeRandomFileContent(file)
		}
		if err != nil {
			return err
		}
	}

	if subDirDepth > 0 {
		numDirs := randDirCount()
		for i := 0; i < numDirs; i++ {
			subDir := dir.Join(fsimpl.RandomString())
			err = subDir.MakeDir()
			if err != nil {
				return err
			}
			// leave first directory empty
			if i > 0 {
				err = writeRandomDirFiles(subDir, subDirDepth-1)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func Test_IdenticalDirContents(t *testing.T) {
	testDir := TempDir().Join("testdir-" + fsimpl.RandomString())
	testDir.MakeDir()
	fmt.Println("testdir:", testDir.Path())

	a := testDir.Join("a")
	a.MakeDir()

	b := testDir.Join("b")
	b.MakeDir()

	recreateBasCopyOfA := func() error {
		err := b.RemoveRecursive()
		if err != nil {
			return err
		}
		b.MakeDir()
		return CopyRecursive(context.Background(), a, b)
	}

	// Empty directories should be identical:
	identical, err := IdenticalDirContents(context.Background(), a, b, false)
	if err != nil {
		t.Fatal(err)
	}
	if !identical {
		t.Fail()
	}
	identical, err = IdenticalDirContents(context.Background(), a, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if !identical {
		t.Fail()
	}

	err = writeRandomDirFiles(a, 3)
	if err != nil {
		t.Fatal(err)
	}

	err = recreateBasCopyOfA()
	if err != nil {
		t.Fatal(err)
	}

	identical, err = IdenticalDirContents(context.Background(), a, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if !identical {
		t.Fail()
	}

	err = deleteRandomFileInDir(b)
	if err != nil {
		t.Fatal(err)
	}

	identical, err = IdenticalDirContents(context.Background(), a, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if identical {
		t.Fail()
	}

	err = recreateBasCopyOfA()
	if err != nil {
		t.Fatal(err)
	}

	err = deleteRandomSubDir(b)
	if err != nil {
		t.Fatal(err)
	}

	identical, err = IdenticalDirContents(context.Background(), a, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if identical {
		t.Fail()
	}

	err = testDir.RemoveRecursive()
	if err != nil {
		t.Fatal(err)
	}
}
