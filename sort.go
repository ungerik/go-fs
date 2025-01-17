package fs

import (
	"cmp"
	"slices"
	"sort"
)

func compareDirsFirst(fi, fj FileReader) (isLess, ok bool) {
	idir := fi.IsDir()
	jdir := fj.IsDir()
	if idir == jdir {
		return false, false
	}
	return idir, true
}

func SortByName[F FileReader](files []F) {
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

func SortByPath(files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path() < files[j].Path()
	})
}

func SortByLocalPath[F FileReader](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.LocalPath(), b.LocalPath())
	})
}

func SortBySize[F FileReader](files []F) {
	slices.SortFunc(files, func(a, b F) int {
		return cmp.Compare(a.Size(), b.Size())
	})
}

func SortByModified(files []File) {
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
