# TODO.md - README Update Tasks

## README Accuracy Report for go-fs

The README is partially outdated. Key findings:

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
- **s3fs** - AWS S3 integration
- **zipfs** - ZIP archive file system
- **memfilesystem** - In-memory file system

### TODO Items Status

| TODO Item                          | Status                                  |
| ---------------------------------- | --------------------------------------- |
| MemFileSystem                      | ⚠️ Mostly complete (some methods panic) |
| Return ErrFileSystemClosed         | ❓ Not verified                          |
| Test dropboxfs                     | ⚠️ Needs testing                        |
| Test ftpfs                         | ⚠️ Needs testing                        |
| TestFileReads and TestFileMetaData | ⚠️ Need comprehensive tests             |
| add TestFileWrites                 | ⚠️ Need comprehensive tests             |

### Recommendations

1. **Add comprehensive API reference**: Document iterator methods, Glob support, StdFS wrappers
2. **Document all filesystem implementations**: Add sections for dropboxfs, ftpfs, sftpfs, s3fs, zipfs
3. **Add examples for**: JSON/XML operations, Glob patterns, iterators, StdFS compatibility
4. **Document MemFileSystem status**: It exists but is partially complete with some panics
