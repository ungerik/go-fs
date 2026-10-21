package s3fs_test

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/s3fs"
)

// ExampleNewLoadDefaultConfig creates a file system for a bucket using the
// AWS default credential chain, registers it, then reads an object through
// the File API. Files resolve to their file system with the registry,
// so the file system must be registered to use it via File values.
//
// The example is compiled but not run, because it needs an S3 bucket.
func ExampleNewLoadDefaultConfig() {
	ctx := context.Background()

	s3FS, err := s3fs.NewLoadDefaultConfig(ctx, "my-bucket", nil)
	if err != nil {
		panic(err)
	}
	err = fs.Register(s3FS)
	if err != nil {
		panic(err)
	}
	defer s3FS.Close() // Also unregisters it

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

// ExampleNewAndRegister registers a file system for a bucket of an
// S3-compatible service. Options.Prefix keeps its URIs apart from those
// of an AWS bucket with the same name.
//
// The example is compiled but not run, because it needs an S3 service.
func ExampleNewAndRegister() {
	ctx := context.Background()

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String("https://minio.example.com"),
		UsePathStyle: true,
	})
	err := s3fs.NewAndRegister(client, "backup", &s3fs.Options{
		Prefix:   "s3://minio.example.com/backup",
		ReadOnly: true,
	})
	if err != nil {
		panic(err)
	}
	file := fs.File("s3://minio.example.com/backup/db/dump.sql")
	defer file.FileSystem().Close() // Unregisters it

	size := file.Size()
	fmt.Println(size)

	err = fs.CopyFile(ctx, file, fs.File("/tmp/dump.sql"))
	if err != nil {
		panic(err)
	}
}
