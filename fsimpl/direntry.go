package fsimpl

import iofs "io/fs"

// DirEntryFromFileInfo wraps a io/fs.FileInfo as io/fs.DirEntry.
func DirEntryFromFileInfo(info iofs.FileInfo) iofs.DirEntry {
	return dirEntry{info}
}

type dirEntry struct {
	iofs.FileInfo
}

func (dir dirEntry) Type() iofs.FileMode          { return dir.Mode() }
func (dir dirEntry) Info() (iofs.FileInfo, error) { return dir.FileInfo, nil }
