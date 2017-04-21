package fs

import "sort"

func compareDirsFirst(fi, fj File) (isLess, ok bool) {
	idir := fi.IsDir()
	jdir := fj.IsDir()
	if idir == jdir {
		return false, false
	}
	return idir, true
}

func SortByName(files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})
}

func SortByNameDirsFirst(files []File) {
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

func SortBySize(files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size() < files[j].Size()
	})
}

func SortByModTime(files []File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime().Before(files[j].ModTime())
	})
}

func SortByModTimeDirsFirst(files []File) {
	sort.Slice(files, func(i, j int) bool {
		fi := files[i]
		fj := files[j]
		if isLess, ok := compareDirsFirst(fi, fj); ok {
			return isLess
		}
		return fi.ModTime().Before(fj.ModTime())
	})
}
