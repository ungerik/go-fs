// Package fs provides a unified file system abstraction for Go.
//
// The central type is [File], a string that holds a local path or a URI
// and dispatches its methods to the [FileSystem] registered for the URI
// prefix. A string without a prefix is a path on the local file system
// [Local], so any local path literal is a valid File:
//
//	data, err := fs.File("~/notes.txt").ReadAll(ctx)
//	err = fs.File("s3://bucket/notes.txt").WriteAll(ctx, data)
//
// [FileReader] is the read-only interface implemented by File and by
// [MemFile], an in-memory file with a name.
//
// # Context rule
//
// A method takes a [context.Context] as first parameter only if it can
// take long: reading or writing content (ReadAll, WriteAll, Append,
// ContentHash, Truncate, MoveTo), listing directories (ListDir*, Glob,
// RemoveRecursive) and dialing connections. Metadata, open, and path
// methods have no context parameter.
//
// # Errors
//
// Every file system reports missing files with an error wrapping
// [os.ErrNotExist], MakeDir on an existing path with [os.ErrExist],
// write operations on read-only file systems with [ErrReadOnlyFileSystem],
// use after Close with [ErrFileSystemClosed], and cancellation with the
// context error. [ErrUnsupported] wraps [errors.ErrUnsupported] for
// operations a file system can't provide.
//
// # Implementing a file system
//
// A backend implements [FileSystem] and, if it can write,
// [WriteFileSystem]. Every other operation is an optional interface like
// [ReadAllFileSystem] or [MoveFileSystem] with a generic emulation in this
// package; a backend implements one only when it can do the operation
// more efficiently. Package fsimpl provides helpers for implementers and
// package fstest the conformance suite every backend passes.
package fs
