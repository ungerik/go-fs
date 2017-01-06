package fs

import (
	"io/ioutil"
	"strings"
)

var (
	Local    LocalFileSystem
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
	return Select(uri).SelectFile(uri)
}

func CreateFile(uri string, perm ...Permissions) (File, error) {
	return Select(uri).CreateFile(uri, perm...)
}

func MakeDir(uri string) (File, error) {
	return Select(uri).MakeDir(uri)
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
	writer, err := SelectFile(uri).OpenWriter()
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = writer.Write(data)
	return err
}

func Append(uri string, data []byte, perm ...Permissions) error {
	writer, err := SelectFile(uri).OpenAppendWriter()
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
