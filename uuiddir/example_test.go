package uuiddir_test

import (
	"fmt"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/uuiddir"
)

// Example stores a file under the nested directory path of a UUID,
// which bounds how many entries end up in a single directory.
func Example() {
	uuid := [16]byte{
		0xf0, 0x49, 0x8f, 0xad, 0x43, 0x7c, 0x49, 0x54,
		0xad, 0x82, 0x8e, 0xc2, 0xcc, 0x20, 0x26, 0x28,
	}

	memFS, err := fs.NewMemFileSystem("/")
	if err != nil {
		panic(err)
	}
	defer memFS.Close()
	baseDir := memFS.RootDir()

	uuidDir, err := uuiddir.Make(baseDir, uuid)
	if err != nil {
		panic(err)
	}
	fmt.Println(uuidDir.Path())

	// The path of a UUID directory can be parsed back into the UUID
	parsed, err := uuiddir.Parse(uuidDir)
	if err != nil {
		panic(err)
	}
	fmt.Println(uuiddir.FormatString(parsed))

	// Output:
	// /f0/498/fad/437c4954/ad828ec2cc202628
	// f0/498/fad/437c4954/ad828ec2cc202628
}
