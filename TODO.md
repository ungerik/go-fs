# TODO.md

## Open Tasks

- Return `ErrFileSystemClosed` from all closable `FileSystem` implementations
  (verify each backend covers this consistently).
  Note that LocalFileSystem is not closable.
- Add integration tests for `dropboxfs`.
- Add integration tests for `ftpfs`.
- Flesh out `TestFileReads`, `TestFileMetaData`, and add `TestFileWrites`
  with comprehensive coverage across backends.
