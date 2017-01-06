package fs

import (
	"io/ioutil"
	"strings"
)

var (
	Local    = LocalFileSystem{DefaultCreatePermissions: UserAndGroupReadWrite}
	Registry = []FileSystem{Local}
)

func Select(uri string) FileSystem {
	for _, fs := range Registry {
		if strings.HasPrefix(uri, fs.Prefix()) {
			return fs
		}
	}
	return Local
}

func SelectFile(uri string) File {
	return Select(uri).File(uri)
}

func Touch(uri string, perm ...Permissions) (File, error) {
	file := SelectFile(uri)
	err := file.Touch(perm...)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func MakeDir(uri string, perm ...Permissions) (File, error) {
	file := SelectFile(uri)
	err := file.MakeDir(perm...)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func Read(uri string) ([]byte, error) {
	reader, err := SelectFile(uri).OpenReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return ioutil.ReadAll(reader)
}

func Write(uri string, data []byte, perm ...Permissions) error {
	writer, err := SelectFile(uri).OpenWriter(perm...)
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.Write(data)
	return err
}

func Append(uri string, data []byte, perm ...Permissions) error {
	writer, err := SelectFile(uri).OpenAppendWriter(perm...)
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.Write(data)
	return err
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

func Truncate(uri string, size int64) error {
	return SelectFile(uri).Truncate(size)
}

func Remove(uri string) error {
	return SelectFile(uri).Remove()
}
