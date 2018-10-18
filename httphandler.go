package fs

import (
	"net/http"
)

// HTTPHandler returns a http.Handler that serves the passed file with a Content-Type header.
// If no contentType is passed then http.DetectContentType is used with the file content.
// 404 error is returned if the file does not exist and a 500 error if there was any other
// error while reading it.
func HTTPHandler(file FileReader, contentType ...string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ServeHTTP(response, request, file, contentType...)
	})
}

// ServeHTTP serves the passed file with a Content-Type header.
// If no contentType is passed then http.DetectContentType is used with the file content.
// 404 error is returned if the file does not exist and a 500 error if there was any other
// error while reading it.
func ServeHTTP(response http.ResponseWriter, request *http.Request, file FileReader, contentType ...string) {
	data, err := file.ReadAll()
	if err != nil {
		statusCode := http.StatusInternalServerError
		if IsErrDoesNotExist(err) {
			statusCode = http.StatusNotFound
		}
		http.Error(response, http.StatusText(statusCode), statusCode)
		return
	}

	if len(contentType) == 0 {
		response.Header().Add("Content-Type", http.DetectContentType(data))
	} else {
		for _, ct := range contentType {
			response.Header().Add("Content-Type", ct)
		}
	}
	response.Write(data)
}
