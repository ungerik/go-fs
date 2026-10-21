// Package uuiddir stores an unlimited number of UUID-named directories
// without ever putting too many entries into a single directory.
//
// Why: file systems (and the tools and object-store listings around them)
// degrade badly when one directory holds millions of entries, while an
// application that keys its data by UUID easily produces that many.
// Splitting the 32 hex digits of a UUID into nested sub-directories bounds
// the fan-out per directory level: the first level holds at most 256
// entries (2 hex digits), the next two at most 4096 each (3 hex digits),
// so no directory grows without limit no matter how many UUIDs are stored.
// The layout is deterministic, so a UUID maps to exactly one path and the
// path can be parsed back into the UUID.
//
// Example:
//
//	The UUID f0498fad-437c-4954-ad82-8ec2cc202628 maps to the path
//	f0/498/fad/437c4954/ad828ec2cc202628
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
//
// Example:
//
//	Splitting the UUID f0498fad-437c-4954-ad82-8ec2cc202628 returns
//	[]string{"f0", "498", "fad", "437c4954", "ad828ec2cc202628"}
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

// Join returns a directory with the split UUID and pathParts joined to baseDir.
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

// FormatString returns the split UUID joined with slashes.
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

// levels is the number of directory levels of a UUID path, see Split.
const levels = 5

// Enum calls callback for every directory that represents an UUID under baseDir.
//
// It walks the five levels of the layout with File.ListDirInfo, so the
// FileInfo of the listing decides whether an entry is a directory,
// without another Stat per entry. Hidden entries, files and leaf
// directories whose path doesn't parse as UUID are skipped.
func Enum(ctx context.Context, baseDir fs.File, callback func(uuidDir fs.File, uuid [16]byte) error) error {
	return enumLevel(ctx, baseDir, 1, callback)
}

// enumLevel lists the directories of dir, which is at the passed level
// of the layout, and descends into them until the UUID directories.
func enumLevel(ctx context.Context, dir fs.File, level int, callback func(uuidDir fs.File, uuid [16]byte) error) error {
	return dir.ListDirInfo(ctx, func(info *fs.FileInfo) error {
		if !info.IsDir || info.IsHidden {
			return nil
		}
		if level < levels {
			return enumLevel(ctx, info.File, level+1, callback)
		}
		uuid, err := Parse(info.File)
		if err != nil {
			// Skip directories that are not a valid UUID path
			// like files at the other levels are skipped
			return nil
		}
		return callback(info.File, uuid)
	})
}

// RemoveDir deletes uuidSubDir recursively and all empty parent
// directories of uuidSubDir until but not including baseDir.
func RemoveDir(ctx context.Context, baseDir, uuidSubDir fs.File) error {
	basePath := baseDir.Path()
	uuidPath := uuidSubDir.Path()
	if uuidPath == basePath {
		return nil
	}
	sep := baseDir.FileSystem().Separator()
	if baseDir.FileSystem() != uuidSubDir.FileSystem() ||
		!strings.HasPrefix(uuidPath, strings.TrimSuffix(basePath, sep)+sep) {
		return fmt.Errorf("uuidDir(%q) is not a sub directory of baseDir(%q)", uuidPath, basePath)
	}

	err := uuidSubDir.RemoveRecursive(ctx)
	if err != nil {
		return err
	}
	for {
		uuidSubDir = uuidSubDir.Dir()
		if uuidSubDir.Path() == basePath || !uuidSubDir.IsEmptyDir() {
			return nil
		}
		err = uuidSubDir.Remove()
		if err != nil {
			return err
		}
	}
}

// Make sub-directories under baseDir for the passed UUID
func Make(baseDir fs.File, uuid [16]byte) (uuidDir fs.File, err error) {
	uuidDir = Join(baseDir, uuid)
	return uuidDir, uuidDir.MakeAllDirs()
}

// Remove the sub-directories under baseDir for the passed UUID
func Remove(ctx context.Context, baseDir fs.File, uuid [16]byte) error {
	return RemoveDir(ctx, baseDir, Join(baseDir, uuid))
}
