package multipartfs_test

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"

	"github.com/ungerik/go-fs/multipartfs"
)

// ExampleFromRequestForm treats the files of an uploaded multipart form
// as a read-only file system, so a handler can pass them to any code
// that takes an fs.File or fs.FileReader.
func ExampleFromRequestForm() {
	request := uploadRequest()

	formFS, err := multipartfs.FromRequestForm(request, 4*1024*1024)
	if err != nil {
		panic(err)
	}
	// Close removes the temporary files of the form
	defer formFS.Close()

	fmt.Println(formFS.FormValue("comment"))

	file, err := formFS.FormFile("attachment")
	if err != nil {
		panic(err)
	}
	fmt.Println(file.Name())

	content, err := file.ReadAllString(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(content)

	// Output:
	// looks good
	// notes.txt
	// file content
}

// uploadRequest builds a POST request with a multipart form
// like a browser would send it.
func uploadRequest() *http.Request {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("comment", "looks good"); err != nil {
		panic(err)
	}
	part, err := form.CreateFormFile("attachment", "notes.txt")
	if err != nil {
		panic(err)
	}
	if _, err = part.Write([]byte("file content")); err != nil {
		panic(err)
	}
	if err = form.Close(); err != nil {
		panic(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}
