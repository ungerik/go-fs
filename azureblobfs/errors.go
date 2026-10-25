package azureblobfs

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"

	"github.com/ungerik/go-fs"
)

// ErrSnapshotsPresent is wrapped by the error of Remove, RemoveAll and
// Move for a blob with snapshots unless Options.DeleteSnapshots is set.
// The blob is not deleted, and Move copies nothing.
const ErrSnapshotsPresent fs.SentinelError = "azureblobfs: blob has snapshots"

// errListingWithoutBlobs is returned for a listing answer without a Blobs
// element, which azcore decodes without error into a nil Segment.
// Azure always sends the element, so the answer came from something else,
// like a misconfigured proxy.
var errListingWithoutBlobs = errors.New("azureblobfs: listing answer without Blobs element")

// errAborted is the error that the upload of an aborted writer reads.
var errAborted = errors.New("azureblobfs: upload aborted")

func isNotFound(err error) bool {
	return bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound, bloberror.ResourceNotFound)
}

// isPermissionDenied reports whether err is a 403 Forbidden response.
func isPermissionDenied(err error) bool {
	if respErr, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return respErr.StatusCode == http.StatusForbidden
	}
	return false
}

// isPreconditionFailed reports whether err is a 412 Precondition Failed
// response, like the ConditionNotMet answer to a failed If-Match.
func isPreconditionFailed(err error) bool {
	if respErr, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return respErr.StatusCode == http.StatusPreconditionFailed
	}
	return false
}

var (
	// urlQuery matches URLs with a query in error messages,
	// the query can carry the signature of a SAS token.
	urlQuery = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'?#]*)\?[^\s"'#]*`)

	// sasSignature matches a SAS signature parameter outside of a URL.
	sasSignature = regexp.MustCompile(`(?i)\bsig=[^&\s"']*`)
)

// redactedError has the message of err without SAS signatures.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactErr returns err with the queries of URLs and SAS signatures
// removed from its message, so that a SAS token of the container URL
// doesn't leak into logs. Transport errors of net/http, for example,
// contain the full request URL. errors.Is and errors.As still see err.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := sasSignature.ReplaceAllString(urlQuery.ReplaceAllString(msg, "$1"), "sig=REDACTED")
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, err: err}
}
