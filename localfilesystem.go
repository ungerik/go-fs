package fs

import (
	"os"
	"time"
)

const LocalPrefix = "file://"

var LocalDefaultCreatePermissions = UserGroupReadWrite

type LocalFileSystem struct{}

func (LocalFileSystem) IsLocal() bool {
	return true
}

func (LocalFileSystem) Prefix() string {
	return LocalPrefix
}

func (LocalFileSystem) SelectFile(uri string) File {
	return newLocalFile(uri)
}

func (LocalFileSystem) CreateFile(uri string, perm ...Permissions) (File, error) {
	p := CombinePermissions(perm, LocalDefaultCreatePermissions)
	file := Local.SelectFile(uri)
	f, err := os.OpenFile(file.Path(), os.O_RDWR|os.O_CREATE|os.O_TRUNC, os.FileMode(p))
	if err != nil {
		return nil, err
	}
	err = f.Close()
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (LocalFileSystem) MakeDir(uri string, perm ...Permissions) (File, error) {
	p := CombinePermissions(perm, LocalDefaultCreatePermissions)
	file := Local.SelectFile(uri)
	err := os.MkdirAll(file.Path(), os.FileMode(p))
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (LocalFileSystem) Touch(uri string) error {
	if file := Local.SelectFile(uri); file.Exists() {
		now := time.Now()
		return os.Chtimes(file.Path(), now, now)
	} else {
		_, err := Local.CreateFile(uri)
		return err
	}
}
