package fs

import (
	"errors"
	"fmt"
	"io"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
func CopyFile(src, dest File, perm ...Permissions) error {
	var buf []byte
	return CopyFileBuf(src, dest, &buf, perm...)
}

// CopyFileBuf copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// buf must point to a []byte variable.
// If that variable is initialized with a byte slice, then this slice will be used as buffer,
// else a byte slice will be allocated for the variable.
// Use this function to re-use buffers between CopyFileBuf calls.
func CopyFileBuf(src, dest File, buf *[]byte, perm ...Permissions) error {
	if buf == nil {
		panic("CopyFileBuf: buf is nil")
	}

	// Handle directories
	if dest.IsDir() {
		dest = dest.Relative(src.Name())
	} else {
		err := dest.Dir().MakeDir()
		if err != nil {
			return err
		}
	}

	// Use same file system copy if possible
	fs := src.FileSystem()
	if fs == dest.FileSystem() {
		return fs.CopyFile(src.Path(), dest.Path(), buf)
	}

	r, err := src.OpenReader()
	if err != nil {
		return err
	}
	defer r.Close()

	if len(perm) == 0 {
		perm = []Permissions{src.Permissions()}
	}
	w, err := dest.OpenWriter(perm...)
	if err != nil {
		return err
	}
	defer w.Close()

	if *buf == nil {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	return err
}

// CopyRecursive can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursive(src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(src, dest, patterns, &buf)
}

func copyRecursive(src, dest File, patterns []string, buf *[]byte) error {
	if !src.IsDir() {
		// Just copy one file
		return CopyFileBuf(src, dest, buf)
	}

	if dest.Exists() && !dest.IsDir() {
		return fmt.Errorf("Can't copy a directory (%s) over a file (%s)", src.URL(), dest.URL())
	}

	// TODO better check
	if !dest.Exists() {
		err := dest.MakeDir()
		if err != nil {
			return err
		}
	}

	// Copy directories recursive
	return src.ListDir(func(file File) error {
		return copyRecursive(file, dest.Relative(file.Name()), patterns, buf)
	}, patterns...)
}

// FilesToURLs returns the URLs of a slice of Files.
func FilesToURLs(files []File) (fileURLs []string) {
	fileURLs = make([]string, len(files))
	for i := range files {
		fileURLs[i] = files[i].URL()
	}
	return fileURLs
}

// FilesToPaths returns the FileSystem specific paths of a slice of Files.
func FilesToPaths(files []File) (paths []string) {
	paths = make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Path()
	}
	return paths
}

// URIsToFiles returns Files for the given fileURIs.
func URIsToFiles(fileURIs []string) (files []File) {
	files = make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = File(fileURIs[i])
	}
	return files
}

type FileCallback func(File) error

func (f FileCallback) FileInfoCallback(file File, info FileInfo) error {
	return f(file)
}

// SameFile returns if a and b describe the same file or directory
func SameFile(a, b File) bool {
	aFS, aPath := a.ParseRawURI()
	bFS, bPath := b.ParseRawURI()
	return aFS == bFS && aPath == bPath
}

// IdenticalDirContents returns true if the files in dirA and dirB are identical in size and content.
// If recursive is true, then directories will be considered too.
func IdenticalDirContents(dirA, dirB File, recursive bool) (identical bool, err error) {
	if SameFile(dirA, dirB) {
		return true, nil
	}

	fileInfosA := make(map[string]FileInfo)
	err = dirA.ListDirInfo(func(file File, info FileInfo) error {
		if !info.IsDir || recursive {
			fileInfosA[info.Name] = info
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	fileInfosB := make(map[string]FileInfo, len(fileInfosA))
	hasDiff := errors.New("hasDiff")
	err = dirB.ListDirInfo(func(file File, info FileInfo) error {
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
		return false, err
	}

	for filename, infoA := range fileInfosA {
		infoB := fileInfosB[filename]

		if recursive && infoA.IsDir {
			identical, err = IdenticalDirContents(dirA.Relative(filename), dirB.Relative(filename), true)
			if !identical {
				return false, err
			}
		} else {
			hashA := infoA.ContentHash
			if hashA == "" {
				hashA, err = dirA.Relative(filename).ContentHash()
				if err != nil {
					return false, err
				}
			}
			hashB := infoB.ContentHash
			if hashB == "" {
				hashB, err = dirB.Relative(filename).ContentHash()
				if err != nil {
					return false, err
				}
			}

			if hashA != hashB {
				return false, nil
			}
		}
	}

	return true, nil
}
