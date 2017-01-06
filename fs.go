package fs

import (
	"encoding/json"
	"encoding/xml"
	"sort"
	"strings"
)

var (
	Local    = LocalFileSystem{DefaultCreatePermissions: UserAndGroupReadWrite}
	Registry = []FileSystem{Local}
)

func GetFileSystem(uri string) FileSystem {
	for _, fs := range Registry {
		if strings.HasPrefix(uri, fs.Prefix()) {
			return fs
		}
	}
	return Local
}

func GetFile(uri string) File {
	return GetFileSystem(uri).File(uri)
}

func ListDir(uri string, callback func(File) error, patterns ...string) error {
	return GetFile(uri).ListDir(callback, patterns...)
}

func Touch(uri string, perm ...Permissions) (File, error) {
	file := GetFile(uri)
	err := file.Touch(perm...)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func MakeDir(uri string, perm ...Permissions) (File, error) {
	file := GetFile(uri)
	err := file.MakeDir(perm...)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func Truncate(uri string, size int64) error {
	return GetFile(uri).Truncate(size)
}

func Remove(uri string) error {
	return GetFile(uri).Remove()
}

func Read(uri string) ([]byte, error) {
	return GetFile(uri).ReadAll()
}

func Write(uri string, data []byte, perm ...Permissions) error {
	return GetFile(uri).WriteAll(data, perm...)
}

func Append(uri string, data []byte, perm ...Permissions) error {
	return GetFile(uri).Append(data, perm...)
}

func ReadString(uri string) (string, error) {
	data, err := Read(uri)
	if data == nil || err != nil {
		return "", err
	}
	return string(data), nil
}

func WriteString(uri string, data string, perm ...Permissions) error {
	return Write(uri, []byte(data), perm...)
}

func AppendString(uri string, data string, perm ...Permissions) error {
	return Append(uri, []byte(data), perm...)
}

func ReadJSON(file File, output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func WriteJSON(file File, input interface{}, indent ...string) error {
	b, err := json.MarshalIndent(input, "", strings.Join(indent, ""))
	if err != nil {
		return err
	}
	return file.WriteAll(b)
}

func ReadXML(file File, output interface{}) error {
	data, err := file.ReadAll()
	if err != nil {
		return err
	}
	return xml.Unmarshal(data, output)
}

func WriteXML(file File, input interface{}, indent ...string) error {
	b, err := xml.MarshalIndent(input, "", strings.Join(indent, ""))
	if err != nil {
		return err
	}
	b = append([]byte(xml.Header), b...)
	return file.WriteAll(b)
}

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
