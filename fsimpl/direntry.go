package fsimpl

import iofs "io/fs"

var _ iofs.DirEntry = dirEntry{}

// DirEntryFromFileInfo wraps a io/fs.FileInfo as io/fs.DirEntry.
func DirEntryFromFileInfo(info iofs.FileInfo) iofs.DirEntry {
	return dirEntry{info}
}

// dirEntry implements io/fs.DirEntry by wrapping an io/fs.FileInfo.
// Name, Size, IsDir and Info are provided by the embedded FileInfo.
type dirEntry struct {
	iofs.FileInfo
}

// Type returns the type bits of the wrapped FileInfo's mode,
// as required by the io/fs.DirEntry interface.
func (dir dirEntry) Type() iofs.FileMode { return dir.Mode().Type() }

// Info returns the wrapped io/fs.FileInfo.
func (dir dirEntry) Info() (iofs.FileInfo, error) { return dir.FileInfo, nil }
