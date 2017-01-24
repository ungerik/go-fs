package fs

import "sort"

func compareDirsFirst(fi, fj File) (less, doSort bool) {
	idir := fi.IsDir()
	jdir := fj.IsDir()
	if idir == jdir {
		return false, false
	}
	return idir, true
}

type sortableFileNames struct {
	files     []File
	dirsFirst bool
}

func (s *sortableFileNames) Len() int {
	return len(s.files)
}

func (s *sortableFileNames) Less(i, j int) bool {
	fi := s.files[i]
	fj := s.files[j]
	if s.dirsFirst {
		if less, doSort := compareDirsFirst(fi, fj); doSort {
			return less
		}
	}
	return fi.Path() < fj.Path()
}

func (s *sortableFileNames) Swap(i, j int) {
	s.files[i], s.files[j] = s.files[j], s.files[i]
}

func SortByName(files []File, dirsFirst bool) {
	sort.Sort(&sortableFileNames{files, dirsFirst})
}

func SortBySize(files []File, dirsFirst bool) {

}

func SortByDate(files []File, dirsFirst bool) {

}
