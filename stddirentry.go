package fs

import iofs "io/fs"

var (
	_ iofs.DirEntry = StdDirEntry{}
)

// StdDirEntry implements the io/fs.DirEntry interface
// from the standard library for a File.
//
// Like the entries of os.ReadDir it describes the directory entry itself
// and doesn't follow a symbolic link: for a link IsDir returns false,
// Type returns io/fs.ModeSymlink and Info describes the link,
// so io/fs.WalkDir doesn't descend into linked directories.
//
// A StdDirEntry from [NewStdDirEntry] answers from the FileInfo
// of the directory listing, a StdDirEntry with only the File set
// (see [File.StdDirEntry]) calls [File.Lstat] for every method except Name.
type StdDirEntry struct {
	File File

	info *FileInfo // from a directory listing, nil to call File.Lstat
}

// NewStdDirEntry returns a StdDirEntry for the FileInfo of a directory
// listing, like the ones passed to the callback of [File.ListDirInfo].
// The methods of the entry use info without accessing the file system again.
func NewStdDirEntry(info *FileInfo) StdDirEntry {
	return StdDirEntry{File: info.File, info: info}
}

// Name returns the name of the file (or subdirectory) described by the entry.
// This name is only the final element of the path (the base name), not the entire path.
// For example, Name would return "hello.go" not "/home/gopher/hello.go".
func (de StdDirEntry) Name() string {
	if de.info != nil && de.info.Name != "" {
		return de.info.Name
	}
	return de.File.Name()
}

// IsDir reports whether the entry describes a directory.
// It returns false for a symbolic link to a directory.
func (de StdDirEntry) IsDir() bool {
	info, err := de.lstat()
	return err == nil && info.IsDir && !info.IsSymlink
}

// Type returns the type bits for the entry.
// The type bits are a subset of the usual FileMode bits, those returned by the FileMode.Type method.
// For a symbolic link it returns io/fs.ModeSymlink.
func (de StdDirEntry) Type() iofs.FileMode {
	info, err := de.lstat()
	if err != nil {
		return 0
	}
	return entryFileInfo(info).Mode().Type()
}

// Info returns the FileInfo for the file or subdirectory described by the entry.
// The returned FileInfo may be from the time of the original directory read
// or from the time of the call to Info. If the file has been removed or renamed
// since the directory read, Info may return an error satisfying errors.Is(err, ErrNotExist).
// If the entry denotes a symbolic link, Info reports the information about the link itself,
// not the link's target.
func (de StdDirEntry) Info() (iofs.FileInfo, error) {
	info, err := de.lstat()
	if err != nil {
		return nil, err
	}
	return entryFileInfo(info), nil
}

// lstat returns the FileInfo of the entry itself.
func (de StdDirEntry) lstat() (*FileInfo, error) {
	if de.info != nil {
		return de.info, nil
	}
	return de.File.Lstat()
}

// entryFileInfo returns the io/fs.FileInfo of a directory entry,
// which describes a symbolic link itself.
func entryFileInfo(info *FileInfo) iofs.FileInfo {
	if info.IsSymlink {
		return linkFileInfo{fileInfo{info}}
	}
	return info.StdFileInfo()
}
