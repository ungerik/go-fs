package dropboxfs

import (
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/ungerik/go-fs"
)

// lookupErrorOf returns the LookupError of a typed Dropbox API error
// about the path of a request, for relocations the source path, or nil.
func lookupErrorOf(err error) *files.LookupError {
	if e, ok := errors.AsType[files.GetMetadataAPIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.DownloadAPIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.ListFolderAPIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.ListFolderContinueAPIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.DeleteV2APIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.PathLookup
	}
	if r := relocationErrorOf(err); r != nil {
		return r.FromLookup
	}
	return nil
}

// writeErrorOf returns the WriteError of a typed Dropbox API error
// about the path a request writes to, for relocations the destination
// path, or nil.
func writeErrorOf(err error) *files.WriteError {
	if e, ok := errors.AsType[files.CreateFolderV2APIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.UploadAPIError](err); ok && e.EndpointError != nil && e.EndpointError.Path != nil {
		return e.EndpointError.Path.Reason
	}
	if e, ok := errors.AsType[files.UploadSessionFinishAPIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.Path
	}
	if e, ok := errors.AsType[files.DeleteV2APIError](err); ok && e.EndpointError != nil {
		return e.EndpointError.PathWrite
	}
	if r := relocationErrorOf(err); r != nil {
		return r.To
	}
	return nil
}

// relocationErrorOf returns the RelocationError
// of a MoveV2 or CopyV2 API error, or nil.
func relocationErrorOf(err error) *files.RelocationError {
	if e, ok := errors.AsType[files.MoveV2APIError](err); ok {
		return e.EndpointError
	}
	if e, ok := errors.AsType[files.CopyV2APIError](err); ok {
		return e.EndpointError
	}
	return nil
}

// lookupTag returns the tag of the LookupError of err, or "".
func lookupTag(err error) string {
	if l := lookupErrorOf(err); l != nil {
		return l.Tag
	}
	return ""
}

// conflictTag returns the tag of the WriteConflictError of err
// (files.WriteConflictErrorFile, ...Folder or ...FileAncestor),
// or "" if err is not a write conflict.
func conflictTag(err error) string {
	w := writeErrorOf(err)
	if w == nil || w.Tag != files.WriteErrorConflict {
		return ""
	}
	if w.Conflict == nil {
		return files.WriteConflictErrorFile
	}
	return w.Conflict.Tag
}

// isNotExistError reports whether err is a typed Dropbox API error that
// means a file or folder does not exist, as opposed to a transport or
// authorization error (network outage, rate limit, 5xx, expired token).
//
// The distinction matters: treating a transient error as "does not exist"
// would make existing files appear to vanish, which is dangerous for
// callers that check existence before overwriting or deleting.
func isNotExistError(err error) bool {
	return lookupTag(err) == files.LookupErrorNotFound
}

// isConflictError reports whether err is a typed Dropbox API error
// that means something is already in the way of the written path.
func isConflictError(err error) bool {
	return conflictTag(err) != ""
}

// wrapErr converts typed Dropbox errors about filePath to the fs error types:
//   - a missing path to fs.ErrDoesNotExist,
//   - a folder where a file was expected, like downloading or uploading
//     onto a folder, to fs.ErrIsDirectory,
//   - a file where a folder was expected, like listing a file or writing
//     below a file, to fs.ErrIsNotDirectory,
//   - an existing file in the way to fs.ErrAlreadyExists,
//   - a missing write permission to fs.ErrPermission.
//
// All other errors, including transport and authorization errors,
// are returned unchanged.
func (dbfs *fileSystem) wrapErr(filePath string, err error) error {
	if err == nil {
		return nil
	}
	switch lookupTag(err) {
	case files.LookupErrorNotFound:
		return fs.NewErrDoesNotExist(dbfs.file(filePath))
	case files.LookupErrorNotFile:
		return fs.NewErrIsDirectory(dbfs.file(filePath))
	case files.LookupErrorNotFolder:
		return fs.NewErrIsNotDirectory(dbfs.file(filePath))
	}
	switch conflictTag(err) {
	case files.WriteConflictErrorFolder:
		return fs.NewErrIsDirectory(dbfs.file(filePath))
	case files.WriteConflictErrorFileAncestor:
		return fs.NewErrIsNotDirectory(dbfs.file(path.Dir(filePath)))
	case files.WriteConflictErrorFile:
		return fs.NewErrAlreadyExists(dbfs.file(filePath))
	}
	if w := writeErrorOf(err); w != nil && w.Tag == files.WriteErrorNoWritePermission {
		return fs.NewErrPermission(dbfs.file(filePath))
	}
	return err
}

// wrapRelocationErr converts an error of MoveV2 or CopyV2 from src to dest:
// errors about the source are wrapped for src, errors about the destination
// for dest, see wrapErr, a missing write permission for the source
// (a move deletes it) is an fs.ErrPermission for src, and moving a folder
// into itself is an error wrapping os.ErrInvalid.
func (dbfs *fileSystem) wrapRelocationErr(src, dest string, err error) error {
	if r := relocationErrorOf(err); r != nil {
		switch r.Tag {
		case files.RelocationErrorCantMoveFolderIntoItself, files.RelocationErrorDuplicatedOrNestedPaths:
			return fmt.Errorf("%w: can't move or copy %s to %s: %w", os.ErrInvalid, dbfs.file(src), dbfs.file(dest), err)
		case files.RelocationErrorFromLookup:
			return dbfs.wrapErr(src, err)
		case files.RelocationErrorFromWrite:
			// writeErrorOf returns the write error of the destination,
			// so wrapErr would not see this one
			if r.FromWrite != nil && r.FromWrite.Tag == files.WriteErrorNoWritePermission {
				return fs.NewErrPermission(dbfs.file(src))
			}
			return err
		}
	}
	return dbfs.wrapErr(dest, err)
}
