package fsimpl

import "io/fs"

// DirEntryFromFileInfo wraps a io/fs.FileInfo as io/fs.DirEntry.
func DirEntryFromFileInfo(info fs.FileInfo) fs.DirEntry {
	return dirEntry{info}
}

type dirEntry struct {
	fs.FileInfo
}

func (dir dirEntry) Type() fs.FileMode          { return dir.Mode() }
func (dir dirEntry) Info() (fs.FileInfo, error) { return dir.FileInfo, nil }
