package fs

import (
	"io/fs"
	"net/http"
	"os"
)

type httpFileSystem struct {
	root File
}

func (fs httpFileSystem) Open(name string) (http.File, error) {
	if fs.root == "" || name == "" {
		return nil, ErrEmptyPath
	}
	file := fs.root.Join(name)
	info := file.Info()
	if !info.Exists {
		return nil, os.ErrNotExist
	}
	f := &httpFile{info: info.StdFileInfo()}
	if info.IsDir {
		f.dir = file
	} else {
		r, err := file.OpenReadSeeker()
		if err != nil {
			return nil, err
		}
		f.ReadSeekCloser = r
	}
	return f, nil
}

type httpFile struct {
	ReadSeekCloser             // set when not a directory
	dir            File        // set when directory
	info           fs.FileInfo // always set
}

func (f *httpFile) Readdir(count int) (files []fs.FileInfo, err error) {
	err = f.dir.ListDirInfo(func(info FileInfo) error {
		if !info.IsHidden {
			files = append(files, info.StdFileInfo())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if count > 0 && len(files) > count {
		files = files[:count]
	}
	return files, nil
}

func (f *httpFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}
