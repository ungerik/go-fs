# TODO.md - README Update Tasks

## README Accuracy Report for go-fs

The README is partially outdated. Key findings:

### Major Issues

- ✅ **MemFileSystem EXISTS** - marked as TODO but implemented in [memfilesystem.go](memfilesystem.go)
- ✅ **ZipFileSystem EXISTS** - marked as TODO but fully implemented in [zipfs/](zipfs/)
- ✅ **S3 FileSystem EXISTS** - marked as TODO but implemented in [s3fs/](s3fs/)

### Missing Documentation

**Go 1.23+ Iterator Methods:**
- `ListDirIter()`, `ListDirIterContext()`
- `ListDirRecursiveIter()`
- `Glob()`, `MustGlob()`

**Standard Library Compatibility:**
- `StdFS()` - wraps File as `io/fs.FS` compatible type
- `StdDirEntry()` - wraps File as `io/fs.DirEntry`

**JSON/XML Operations:**
- `ReadJSON()`, `WriteJSON()` - documented but implementation details expanded
- `ReadXML()`, `WriteXML()` - not mentioned in README at all

**Additional File Methods:**
- `IsReadable()`, `IsWritable()` - check file access
- `PathWithSlashes()` - path with forward slashes
- `LocalPath()`, `MustLocalPath()` - get local filesystem path
- `TrimExt()` - remove file extension
- `ExtLower()` - lowercase extension
- `ToAbsPath()` - convert to absolute path
- `HasAbsPath()` - check if path is absolute
- `IsEmptyDir()` - check if directory is empty
- `Truncate()` - resize files
- `User()`, `SetUser()`, `Group()`, `SetGroup()` - ownership operations
- `WithName()`, `WithData()` for MemFile

**Advanced Listing:**
- `ListDirRecursiveMax()` - list with limit recursively
- `ListDirChan()` - channel-based listing
- `ListDirRecursiveChan()` - recursive channel-based listing

**Gob Encoding:**
- `GobEncode()`, `GobDecode()` - mentioned briefly but not in examples

### Undocumented FileSystem Implementations

The README only mentions HTTP and multipartfs as examples. These exist but aren't documented:
- **dropboxfs** - Dropbox integration
- **ftpfs** - FTP file system
- **sftpfs** - SFTP file system
- **s3fs** - AWS S3 integration (mentioned in TODO)
- **zipfs** - ZIP archive file system (mentioned in TODO)
- **memfilesystem** - In-memory file system (mentioned in TODO)

### Incomplete Sections

**Watching the local file system** section is empty (just says "TODO description")
- Should document the `Watch()` method which exists and is implemented

### TODO Items Status

| TODO Item | Status |
|-----------|--------|
| MemFileSystem | ✅ **Mostly complete** (exists but some methods incomplete) |
| ZipFileSystem | ✅ **Complete** (fully implemented) |
| Return ErrFileSystemClosed | ❓ Not verified |
| S3 | ✅ **Complete** (implemented with AWS SDK v2) |
| Test dropboxfs | ⚠️ Needs testing |
| Test ftpfs | ⚠️ Needs testing (note typo in README: "fptfs") |
| TestFileReads and TestFileMetaData | ⚠️ Need comprehensive tests |
| add TestFileWrites | ⚠️ Need comprehensive tests |

### Recommendations

1. **Update README to reflect completed features**: Remove MemFileSystem, ZipFileSystem, and S3 from TODO list
2. **Add comprehensive API reference**: Document iterator methods, Glob support, StdFS wrappers
3. **Document all filesystem implementations**: Add sections for dropboxfs, ftpfs, sftpfs, s3fs, zipfs
4. **Complete "Watching" section**: Add examples of file watching functionality
5. **Add examples for**: JSON/XML operations, Glob patterns, iterators, StdFS compatibility
6. **Fix typo**: "fptfs" → "ftpfs" in TODO list
7. **Document MemFileSystem status**: It exists but is partially complete with some panics
