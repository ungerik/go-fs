package fs

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// ServeFileHTTPHandler returns a http.Handler that serves the passed file with a Content-Type header.
// If no contentType is passed then http.DetectContentType is used with the file content.
// 404 error is returned if the file does not exist and a 500 error if there was any other
// error while reading it.
func ServeFileHTTPHandler(file FileReader, contentType ...string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ServeFileHTTP(response, request, file, contentType...)
	})
}

// ServeFileHTTP serves the passed file with a Content-Type header via HTTP.
// If no contentType is passed then http.DetectContentType is used with the file content.
// A status code 404 error is returned if the file does not exist
// and a status code 500 error if there was any other error while reading it.
func ServeFileHTTP(response http.ResponseWriter, request *http.Request, file FileReader, contentType ...string) {
	data, err := file.ReadAllContext(request.Context())
	if err != nil {
		// ErrDoesNotExist implements http.Handler with a 404 response
		var errHandler http.Handler
		if errors.As(err, &errHandler) {
			errHandler.ServeHTTP(response, request)
			return
		}

		if !errors.Is(err, context.Canceled) {
			http.Error(response, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
		return
	}

	response.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if len(contentType) == 0 {
		response.Header().Add("Content-Type", http.DetectContentType(data))
	} else {
		for _, t := range contentType {
			response.Header().Add("Content-Type", t)
		}
	}
	response.Write(data) //#nosec G104
}
