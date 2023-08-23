package fs

import (
	"errors"
	"net/http"
	"os"
	"time"
)

// ServeFileHTTPHandler returns a http.Handler that serves the passed file with a Content-Type header via HTTP.
// If no contentType is passed then it will be deduced from the filename and if that fails from the content.
// A status code 404 error is returned if the file does not exist
// and a status code 500 error if there was any other error while reading it.
//
// Uses http.ServeContent under the hood.
func ServeFileHTTPHandler(file FileReader, contentType ...string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ServeFileHTTP(response, request, file, contentType...)
	})
}

// ServeFileHTTP serves the passed file with a Content-Type header via HTTP.
// If no contentType is passed then it will be deduced from the filename and if that fails from the content.
// A status code 404 error is returned if the file does not exist
// and a status code 500 error if there was any other error while reading it.
//
// Uses http.ServeContent under the hood.
func ServeFileHTTP(response http.ResponseWriter, request *http.Request, file FileReader, contentType ...string) {
	readSeeker, err := file.OpenReadSeeker()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(response, request)
		} else {
			http.Error(response, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}
	defer readSeeker.Close()

	for _, t := range contentType {
		response.Header().Add("Content-Type", t)
	}
	var modTime time.Time
	if f, ok := file.(File); ok {
		modTime = f.Modified()
	}
	http.ServeContent(response, request, file.Name(), modTime, readSeeker)
}
