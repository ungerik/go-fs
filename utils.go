package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const copyBufferSize = 1024 * 1024 * 4

// CopyFile copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
func CopyFile(src FileReader, dest File, perm ...Permissions) error {
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
func CopyFileBuf(src FileReader, dest File, buf *[]byte, perm ...Permissions) error {
	return CopyFileBufContext(context.Background(), src, dest, buf, perm...)
}

// CopyFileBufContext copies a single file between different file systems.
// If dest has a path that does not exist, then the directories
// up to that path will be created.
// If dest is an existing directory, then a file with the base name
// of src will be created there.
// buf must point to a []byte variable.
// If that variable is initialized with a byte slice, then this slice will be used as buffer,
// else a byte slice will be allocated for the variable.
// Use this function to re-use buffers between CopyFileBufContext calls.
func CopyFileBufContext(ctx context.Context, src FileReader, dest File, buf *[]byte, perm ...Permissions) error {
	if buf == nil {
		panic("CopyFileBuf: buf is nil") // not a file system error
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Handle directories
	if dest.IsDir() {
		dest = dest.Join(src.Name())
	} else {
		err := dest.Dir().MakeDir()
		if err != nil {
			return fmt.Errorf("CopyFileBuf: can't make directory %q: %w", dest.Dir(), err)
		}
	}

	// Use same file system copy if possible
	srcFile, srcIsFile := src.(File)
	if srcIsFile {
		fs := srcFile.FileSystem()
		if fs == dest.FileSystem() {
			return fs.CopyFile(ctx, srcFile.Path(), dest.Path(), buf)
		}
	}

	r, err := src.OpenReader()
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open src reader: %w", err)
	}
	defer r.Close()

	if len(perm) == 0 && srcIsFile {
		perm = []Permissions{srcFile.Permissions()}
	}
	w, err := dest.OpenWriter(perm...)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: can't open dest writer: %w", err)
	}
	defer w.Close()

	if *buf == nil {
		*buf = make([]byte, copyBufferSize)
	}
	_, err = io.CopyBuffer(w, r, *buf)
	if err != nil {
		return fmt.Errorf("CopyFileBuf: error from io.CopyBuffer: %w", err)
	}
	return nil
}

// CopyRecursive can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursive(src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(context.Background(), src, dest, patterns, &buf)
}

// CopyRecursiveContext can copy between files of different file systems.
// The filter patterns are applied on filename level, not the whole path.
func CopyRecursiveContext(ctx context.Context, src, dest File, patterns ...string) error {
	var buf []byte
	return copyRecursive(ctx, src, dest, patterns, &buf)
}

func copyRecursive(ctx context.Context, src, dest File, patterns []string, buf *[]byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if !src.IsDir() {
		// Just copy one file
		return CopyFileBufContext(ctx, src, dest, buf)
	}

	if dest.Exists() && !dest.IsDir() {
		return fmt.Errorf("Can't copy a directory (%s) over a file (%s)", src.URL(), dest.URL())
	}

	// TODO better check
	if !dest.Exists() {
		err := dest.MakeDir()
		if err != nil {
			return fmt.Errorf("copyRecursive: can't make dest dir %q: %w", dest, err)
		}
	}

	// Copy directories recursive
	return src.ListDirContext(ctx, func(file File) error {
		return copyRecursive(ctx, file, dest.Join(file.Name()), patterns, buf)
	}, patterns...)
}

// FilesToURLs returns the URLs of a slice of Files.
func FilesToURLs(files []File) []string {
	fileURLs := make([]string, len(files))
	for i, file := range files {
		fileURLs[i] = file.URL()
	}
	return fileURLs
}

// FilesToPaths returns the FileSystem specific paths of a slice of Files.
func FilesToPaths(files []File) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path()
	}
	return paths
}

// FilesToNames returns a string slice with the names pars from the files
func FilesToNames(files []File) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name()
	}
	return names
}

// FilesToFileReaders converts a slice of File to a slice of FileReader
func FilesToFileReaders(files []File) []FileReader {
	fileReaders := make([]FileReader, len(files))
	for i, file := range files {
		fileReaders[i] = file
	}
	return fileReaders
}

// StringsToFiles returns Files for the given fileURIs.
func StringsToFiles(fileURIs []string) []File {
	files := make([]File, len(fileURIs))
	for i := range fileURIs {
		files[i] = File(fileURIs[i])
	}
	return files
}

// StringsToFileReaders returns FileReaders for the given fileURIs.
func StringsToFileReaders(fileURIs []string) []FileReader {
	fileReaders := make([]FileReader, len(fileURIs))
	for i := range fileURIs {
		fileReaders[i] = File(fileURIs[i])
	}
	return fileReaders
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

// CurrentWorkingDir returns the current working directory of the process.
// In case of an erorr, Exists() of the result File will return false.
func CurrentWorkingDir() File {
	cwd, _ := os.Getwd()
	return File(cwd)
}
