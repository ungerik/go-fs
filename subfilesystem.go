package fs

import (
	"path/filepath"
	"strings"
)

type SubFileSystem struct {
	Parent   FileSystem
	BasePath string
}

func NewSubFileSystem(parent FileSystem, basePath string) *SubFileSystem {
	return &SubFileSystem{parent, basePath}
}

func (fs *SubFileSystem) IsReadOnly() bool {
	return fs.Parent.IsReadOnly()
}

func (fs *SubFileSystem) Prefix() string {
	return fs.Parent.Prefix()
}

func (fs *SubFileSystem) Name() string {
	return "Sub file system of " + fs.Parent.Name()
}

func (fs *SubFileSystem) File(subPath string) File {
	if strings.HasPrefix(subPath, fs.Prefix()) || strings.HasPrefix(subPath, "..") || strings.ContainsRune(subPath, ':') {
		panic("invalid subPath for SubFileSystem: " + subPath)
	}
	cleanedPath := filepath.Clean(subPath)
	if cleanedPath == "/" || strings.ContainsRune(cleanedPath, ':') {
		panic("invalid subPath for SubFileSystem: " + subPath)
	}
	return fs.Parent.File(filepath.Join(fs.BasePath, cleanedPath))
}
