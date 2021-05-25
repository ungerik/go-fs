// Package uuiddirs provides functions to split up a UUID
// into a series of sub-directories so that an unlimited number
// of UUIDs can be used as directories.
//
// Example:
//   The UUID f0498fad-437c-4954-ad82-8ec2cc202628 maps to the path
//   f0/498/fad/437c4954/ad828ec2cc202628
package uuiddir

import (
	"context"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	fs "github.com/ungerik/go-fs"
)

// Split a UUID into 5 hex strings.
// Example:
//   Splitting the UUID f0498fad-437c-4954-ad82-8ec2cc202628 returns
//   []string{"f0", "498", "fad", "437c4954", "ad828ec2cc202628"}
func Split(uuid [16]byte) []string {
	hexStr := hex.EncodeToString(uuid[:])
	return []string{
		hexStr[0:2],
		hexStr[2:5],
		hexStr[5:8],
		hexStr[8:16],
		hexStr[16:32],
	}
}

// Join returns a directory with the splitted UUID and pathParts joined to baseDir.
func Join(baseDir fs.File, uuid [16]byte, pathParts ...string) fs.File {
	return baseDir.Join(append(Split(uuid), pathParts...)...)
}

// Parse the path of uuidDir for a UUID
func Parse(uuidDir fs.File) (uuid [16]byte, err error) {
	uuidPath := strings.TrimSuffix(uuidDir.PathWithSlashes(), "/")
	if len(uuidPath) < 36 {
		return nilUUID, fmt.Errorf("path can't be parsed as UUID: %q", string(uuidDir))
	}
	return ParseString(uuidPath[len(uuidPath)-36:])
}

// FormatString returns the splitted UUID joined with slashes.
// It's the inverse to ParseString.
func FormatString(uuid [16]byte) string {
	return path.Join(Split(uuid)...)
}

// ParseString parses a 36 character string (like returned from FormatString) as UUID.
func ParseString(uuidPath string) (uuid [16]byte, err error) {
	if len(uuidPath) != 36 {
		return nilUUID, fmt.Errorf("path can't be parsed as UUID: %q", uuidPath)
	}
	uuidPath = strings.Replace(uuidPath, "/", "", 4)
	if len(uuidPath) != 32 {
		return nilUUID, fmt.Errorf("path can't be parsed as UUID: %q", uuidPath)
	}
	b, err := hex.DecodeString(uuidPath)
	if err != nil {
		return nilUUID, fmt.Errorf("path can't be parsed as UUID: %q", uuidPath)
	}
	copy(uuid[:], b)
	return uuid, validateUUID(uuid)
}

// Enum calls callback for every directory that represents an UUID under baseDir.
func Enum(ctx context.Context, baseDir fs.File, callback func(uuidDir fs.File, uuid [16]byte) error) error {
	return baseDir.ListDirContext(ctx, func(level0Dir fs.File) error {
		if !level0Dir.Exists() || level0Dir.IsHidden() {
			return nil
		}
		if !level0Dir.IsDir() {
			// fmt.Println("Directory expected but found file:", level0Dir)
			return nil
		}
		return level0Dir.ListDirContext(ctx, func(level1Dir fs.File) error {
			if !level1Dir.Exists() || level1Dir.IsHidden() {
				return nil
			}
			if !level1Dir.IsDir() {
				// fmt.Println("Directory expected but found file:", level1Dir)
				return nil
			}
			return level1Dir.ListDirContext(ctx, func(level2Dir fs.File) error {
				if !level2Dir.Exists() || level2Dir.IsHidden() {
					return nil
				}
				if !level2Dir.IsDir() {
					// fmt.Println("Directory expected but found file:", level2Dir)
					return nil
				}
				return level2Dir.ListDirContext(ctx, func(level3Dir fs.File) error {
					if !level3Dir.Exists() || level3Dir.IsHidden() {
						return nil
					}
					if !level3Dir.IsDir() {
						// fmt.Println("Directory expected but found file:", level3Dir)
						return nil
					}
					return level3Dir.ListDirContext(ctx, func(uuidDir fs.File) error {
						if !uuidDir.Exists() || uuidDir.IsHidden() {
							return nil
						}
						if !uuidDir.IsDir() {
							// fmt.Println("Directory expected but found file:", uuidDir)
							return nil
						}
						uuid, err := Parse(uuidDir)
						if err != nil {
							return err
						}
						return callback(uuidDir, uuid)
					})
				})
			})
		})
	})
}

// RemoveDir deletes uuidSubDir recursevely and all empty parent
// directories of uuidSubDir until but not including baseDir.
func RemoveDir(baseDir, uuidSubDir fs.File) error {
	basePath := baseDir.Path()
	uuidPath := uuidSubDir.Path()
	if !strings.HasPrefix(uuidPath, basePath) || baseDir.FileSystem() != uuidSubDir.FileSystem() {
		return fmt.Errorf("uuidDir(%q) is not a sub directory of baseDir(%q)", uuidPath, basePath)
	}
	if uuidPath == basePath {
		return nil
	}

	// fmt.Println("deleting", uuidDir.Path())
	err := uuidSubDir.RemoveRecursive()
	if err != nil {
		return err
	}
	for {
		uuidSubDir = uuidSubDir.Dir()
		if uuidSubDir.Path() == basePath || !uuidSubDir.IsEmptyDir() {
			return nil
		}
		// fmt.Println("deleting", uuidDir.Path())
		err = uuidSubDir.Remove()
		if err != nil {
			return err
		}
	}
}

// Make sub-directories under baseDir for the passed UUID
func Make(baseDir fs.File, uuid [16]byte) (uuidDir fs.File, err error) {
	uuidDir = Join(baseDir, uuid)
	return uuidDir, baseDir.MakeAllDirs()
}

// Remove the sub-directories under baseDir for the passed UUID
func Remove(baseDir fs.File, uuid [16]byte) error {
	return RemoveDir(baseDir, Join(baseDir, uuid))
}
