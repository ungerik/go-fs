package fs

import (
	"errors"
	"net/http"
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
// status code 404 error is returned if the file does not exist
// and a status code 500 error if there was any other error while reading it.
func ServeFileHTTP(response http.ResponseWriter, request *http.Request, file FileReader, contentType ...string) {
	data, err := file.ReadAll()
	if err != nil {
		// ErrDoesNotExist implements http.Handler with a 404 response
		var handler http.Handler
		if errors.As(err, &handler) {
			handler.ServeHTTP(response, request)
			return
		}

		statusCode := http.StatusInternalServerError
		http.Error(response, http.StatusText(statusCode), statusCode)
		return
	}

	if len(contentType) == 0 || contentType[0] == "" {
		response.Header().Add("Content-Type", http.DetectContentType(data))
	} else {
		response.Header().Add("Content-Type", contentType[0])
	}
	response.Write(data)
}
