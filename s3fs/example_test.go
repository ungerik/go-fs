package s3fs_test

import (
	"context"
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

// ExampleNewLoadDefaultConfig registers a bucket as file system using the
// AWS default credential chain, then reads an object through the File API.
//
// The example is compiled but not run, because it needs an S3 bucket.
func ExampleNewLoadDefaultConfig() {
	ctx := context.Background()

	s3FS, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", false)
	if err != nil {
		panic(err)
	}
	defer s3FS.Close()

	file := s3FS.RootDir().Join("reports", "2026-01.pdf")

	data, err := file.ReadAll(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(data))

	// Every backend uses the same File API
	err = s3FS.RootDir().Join("reports").ListDir(ctx,
		func(f fs.File) error {
			fmt.Println(f.Name())
			return nil
		},
		"*.pdf",
	)
	if err != nil {
		panic(err)
	}
}
