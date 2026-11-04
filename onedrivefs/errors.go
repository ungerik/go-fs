package onedrivefs

import (
	"errors"
	"net/http"

	"github.com/ungerik/go-fs"
)

// isConflict reports whether err is an error response of the
// Graph API that means a name is already in use, also one of
// the monitor of a copy, which has the code without the status.
func isConflict(err error) bool {
	e := apiErrorOf(err)
	return e != nil && (e.StatusCode == http.StatusConflict || e.Code == "nameAlreadyExists")
}

// wrapErr converts error responses of the Graph API about the clean
// path p to the fs error types:
//   - 404 Not Found to fs.ErrDoesNotExist,
//   - 403 Forbidden to fs.ErrPermission, joined with err,
//     whose message tells which permission is missing,
//   - a name conflict to fs.ErrAlreadyExists.
//
// All other errors, including transport errors, an expired access
// token and throttling, are returned unchanged, so they are never
// mistaken for a missing file.
func (f *fileSystem) wrapErr(p string, err error) error {
	switch e := apiErrorOf(err); {
	case e == nil:
		return err
	case e.StatusCode == http.StatusNotFound:
		return fs.NewErrDoesNotExist(f.file(p))
	case e.StatusCode == http.StatusForbidden:
		if errors.Is(err, fs.ErrPermission{}) {
			return err // Already converted
		}
		return errors.Join(fs.NewErrPermission(f.file(p)), err)
	case isConflict(err):
		return fs.NewErrAlreadyExists(f.file(p))
	}
	return err
}
