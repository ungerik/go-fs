package multipartfs

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fstest"
)

// newSeedRequest returns a multipart form request that uploads
// the passed seed files under the form field fieldName.
func newSeedRequest(t *testing.T, fieldName string, seed fstest.Seed) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, content := range seed {
		part, err := w.CreateFormFile(fieldName, name)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, w.WriteField("email", "test@example.com"))
	require.NoError(t, w.Close())

	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", w.FormDataContentType())
	return request
}

func TestConformance(t *testing.T) {
	seed := fstest.FlatSeed()
	multipartFS, err := FromRequestForm(newSeedRequest(t, "files", seed), 1<<20)
	require.NoError(t, err, "FromRequestForm")

	fstest.RunConformance(t, multipartFS, fstest.Config{
		Prefix:  multipartFS.Prefix(),
		TestDir: "files",
		Seed:    seed,
	})
}
