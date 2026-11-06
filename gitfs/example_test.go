package gitfs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/gitfs"
)

// ExampleNew prints the README of the latest commit of the default
// branch of the go-fs repository. On github.com this costs three
// requests: two for New and one for the file.
//
// The example has no output comment because it needs the internet.
func ExampleNew() {
	ctx := context.Background()

	repo, err := gitfs.New(ctx, "https://github.com/ungerik/go-fs.git", "", nil)
	if err != nil {
		panic(err)
	}
	defer repo.Close()
	if err := fs.Register(repo); err != nil {
		panic(err)
	}

	readme, err := repo.RootDir().Join("README.md").ReadAllString(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(readme)
}

// ExampleNew_tag lists the Go files of a release of go-fs,
// using the URI of a directory.
func ExampleNew_tag() {
	ctx := context.Background()

	repo, err := gitfs.New(ctx, "https://github.com/ungerik/go-fs.git", "v1.0.0-rc.6", nil)
	if err != nil {
		panic(err)
	}
	defer repo.Close()
	if err := fs.Register(repo); err != nil {
		panic(err)
	}

	dir := fs.File("git+https://github.com/ungerik/go-fs.git@v1.0.0-rc.6/fsimpl")
	err = dir.ListDir(ctx, func(file fs.File) error {
		fmt.Println(file.Name(), file.Size())
		return nil
	}, "*.go")
	if err != nil {
		panic(err)
	}
}
