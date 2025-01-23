package fs

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"time"
)

// NameIndex returns the slice index of the first file
// where the passed filename equals the result from the Name method
// or -1 in case of no match.
func NameIndex[F interface{ Name() string }](files []F, filename string) int {
	for i, f := range files {
		if f.Name() == filename {
			return i
		}
	}
	return -1
}

// ContainsName returns true if the passed filename
// matches the result from the Name method of any of the files.
func ContainsName[F interface{ Name() string }](files []F, filename string) bool {
	for _, f := range files {
		if f.Name() == filename {
			return true
		}
	}
	return false
}

// LocalPathIndex returns the slice index of the first file
// where the passed localPath equals the result from the LocalPath method
// or -1 in case of no match.
func LocalPathIndex[F interface{ LocalPath() string }](files []F, localPath string) int {
	for i, f := range files {
		if f.LocalPath() == localPath {
			return i
		}
	}
	return -1
}

// ContainsLocalPath returns true if the passed localPath
// matches the result from the LocalPath method of any of the files.
func ContainsLocalPath[F interface{ LocalPath() string }](files []F, localPath string) bool {
	for _, f := range files {
		if f.LocalPath() == localPath {
			return true
		}
	}
	return false
}

// ContentHashIndex returns the slice index of the first file
// where the passed hash equals the result from the ContentHashContext method
// or -1 in case of no match.
func ContentHashIndex[F interface {
	ContentHashContext(ctx context.Context) (string, error)
}](ctx context.Context, files []F, hash string) (int, error) {
	for i, f := range files {
		fHash, err := f.ContentHashContext(ctx)
		if err != nil {
			return -1, err
		}
		if fHash == hash {
			return i, nil
		}
	}
	return -1, nil
}

// NotExistsIndex returns the slice index of the first FileReader
// where the Exists method returned false
// or -1 in case of no match.
func NotExistsIndex[F interface{ Exists() bool }](files []F) int {
	for i, f := range files {
		if !f.Exists() {
			return i
		}
	}
	return -1
}

// AllExist returns true if the Exists method of all files returned true
func AllExist[F interface{ Exists() bool }](files []F) bool {
	for _, f := range files {
		if !f.Exists() {
			return false
		}
	}
	return true
}

// FileURLs returns the URLs of the passed files
func FileURLs[F interface{ URL() string }](files []F) []string {
	fileURLs := make([]string, len(files))
	for i, file := range files {
		fileURLs[i] = file.URL()
	}
	return fileURLs
}

// FilePaths returns the FileSystem specific paths the passed files
func FilePaths[F interface{ Path() string }](files []F) []string {
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path()
	}
	return paths
}

// FileNames returns the names of the passed files
func FileNames[T interface{ Name() string }](files []T) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name()
	}
	return names
}

func SortByName[F interface{ Name() string }](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.Name(), b.Name())
	})
}

func SortByNameDirsFirst[F FileReader](files []F) {
	sort.Slice(files, func(i, j int) bool {
		fi := files[i]
		fj := files[j]
		if isLess, ok := compareDirsFirst(fi, fj); ok {
			return isLess
		}
		return fi.Name() < fj.Name()
	})
}

func SortByPath[F interface{ Path() string }](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.Path(), b.Path())
	})
}

func SortByLocalPath[F interface{ LocalPath() string }](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.LocalPath(), b.LocalPath())
	})
}

func SortBySize[F interface{ Size() int64 }](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.Size(), b.Size())
	})
}

func SortByModified[F interface{ Modified() time.Time }](files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Modified().Before(files[j].Modified())
	})
}

func SortByModifiedDirsFirst(files []File) {
	sort.Slice(files, func(i, j int) bool {
		fi := files[i]
		fj := files[j]
		if isLess, ok := compareDirsFirst(fi, fj); ok {
			return isLess
		}
		return fi.Modified().Before(fj.Modified())
	})
}

func compareDirsFirst(fi, fj FileReader) (isLess, ok bool) {
	idir := fi.IsDir()
	jdir := fj.IsDir()
	if idir == jdir {
		return false, false
	}
	return idir, true
}
