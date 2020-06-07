package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// SameFile returns if a and b describe the same file or directory
func SameFile(a, b File) bool {
	aFS, aPath := a.ParseRawURI()
	bFS, bPath := b.ParseRawURI()
	return aFS == bFS && aPath == bPath
}

const compareContentHashSizeThreshold = 1 << 24 // 16MB

// IdenticalFileContents returns if the passed files have identical content.
// An error is returned if one of the files does not exist (ErrDoesNotExist)
// or if less than 2 files are passed.
// Compares files larger than 16MB via content hash to not allocate too much memory.
func IdenticalFileContents(ctx context.Context, files ...FileReader) (identical bool, err error) {
	if len(files) < 2 {
		return false, fmt.Errorf("need at least 2 files to compare, got %d", len(files))
	}
	if !files[0].Exists() {
		return false, NewErrDoesNotExistFileReader(files[0])
	}
	size := files[0].Size()
	for _, file := range files[1:] {
		if !file.Exists() {
			return false, NewErrDoesNotExistFileReader(file)
		}
		if file.Size() != size {
			return false, nil
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}

	// Compare bytes directly in memory up to compareContentHashSizeThreshold
	// use content hash for larger files to not take up too much RAM
	if size <= compareContentHashSizeThreshold {
		ref, err := files[0].ReadAll()
		if err != nil {
			return false, err
		}
		for _, file := range files[1:] {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			comp, err := file.ReadAll()
			if err != nil {
				return false, err
			}
			if bytes.Compare(comp, ref) != 0 {
				return false, nil
			}
		}
	} else {
		ref, err := files[0].ContentHash()
		if err != nil {
			return false, err
		}
		for _, file := range files[1:] {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			comp, err := file.ContentHash()
			if err != nil {
				return false, err
			}
			if comp != ref {
				return false, nil
			}
		}
	}

	return true, nil
}

// IdenticalDirContents returns true if the files in dirA and dirB are identical in size and content.
// If recursive is true, then directories will be considered too.
func IdenticalDirContents(ctx context.Context, dirA, dirB File, recursive bool) (identical bool, err error) {
	if SameFile(dirA, dirB) {
		return true, nil
	}

	fileInfosA := make(map[string]FileInfo)
	err = dirA.ListDirInfoContext(ctx, func(file File, info FileInfo) error {
		if !info.IsDir || recursive {
			fileInfosA[info.Name] = info
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("IdenticalDirContents: error listing dirA %q: %w", dirA, err)
	}

	fileInfosB := make(map[string]FileInfo, len(fileInfosA))
	hasDiff := errors.New("hasDiff")
	err = dirB.ListDirInfoContext(ctx, func(file File, info FileInfo) error {
		if !info.IsDir || recursive {
			infoA, found := fileInfosA[info.Name]
			if !found || info.Size != infoA.Size || info.IsDir != infoA.IsDir {
				return hasDiff
			}
			fileInfosB[info.Name] = info
		}
		return nil
	})
	if err == hasDiff || len(fileInfosB) != len(fileInfosA) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("IdenticalDirContents: error listing dirB %q: %w", dirB, err)
	}

	for filename, infoA := range fileInfosA {
		infoB := fileInfosB[filename]

		if recursive && infoA.IsDir {
			identical, err = IdenticalDirContents(ctx, dirA.Join(filename), dirB.Join(filename), true)
			if !identical {
				return false, err
			}
		} else {
			hashA := infoA.ContentHash
			if hashA == "" {
				hashA, err = dirA.Join(filename).ContentHash()
				if err != nil {
					return false, fmt.Errorf("IdenticalDirContents: error content hashing %q: %w", filename, err)
				}
			}
			hashB := infoB.ContentHash
			if hashB == "" {
				hashB, err = dirB.Join(filename).ContentHash()
				if err != nil {
					return false, fmt.Errorf("IdenticalDirContents: error content hashing %q: %w", filename, err)
				}
			}

			if hashA != hashB {
				return false, nil
			}
		}
	}

	return true, nil
}
