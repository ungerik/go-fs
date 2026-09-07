package httpfs_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/ungerik/go-fs"
	_ "github.com/ungerik/go-fs/httpfs" // registers http:// and https://
)

// Example reads a URL through the File API. Importing httpfs registers
// the "http://" and "https://" file systems, so any http URL is a fs.File.
func Example() {
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "Hello HTTP")
		},
	))
	defer server.Close()

	file := fs.File(server.URL + "/greeting.txt")

	content, err := file.ReadAllString(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(file.Name())
	fmt.Println(content)

	// Output:
	// greeting.txt
	// Hello HTTP
}
