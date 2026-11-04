package onedrivefs

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestErrors checks the mapping of the error responses and that the
// pre-authenticated URLs are redacted from errors.
func TestErrors(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/dir/file.txt", []byte("x"))

	g.setFailNext("GetItem", fakeFailure{status: http.StatusForbidden, code: "accessDenied"})
	_, err := f.Stat("/dir/file.txt")
	assert.ErrorIs(t, err, os.ErrPermission)
	assert.Equal(t, 1, strings.Count(err.Error(), "accessDenied"), "the error must tell why, once: %s", err)

	g.setFailNext("Download", fakeFailure{status: http.StatusUnauthorized, code: "unauthenticated"})
	_, err = f.OpenReader("/dir/file.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tempauth", "the pre-authenticated URL must be redacted")
	assert.NotContains(t, err.Error(), "/download/", "the pre-authenticated URL must be redacted")

	_, err = f.OpenReader("/dir")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	_, err = f.OpenReader("/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)
	err = f.ListDir(ctx, "/dir/file.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.MakeDir("/dir/file.txt/sub", 0), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.MakeDir("/missing/sub", 0), os.ErrNotExist)
	assert.ErrorIs(t, f.MakeDir("/DIR", 0), os.ErrExist, "names are case-insensitive")
	assert.ErrorIs(t, f.WriteAll(ctx, "/dir/file.txt/sub/x.txt", []byte("x"), 0), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.WriteAll(ctx, "/dir", []byte("x"), 0), fs.ErrIsDirectory{})
	assert.ErrorIs(t, f.WriteAll(ctx, "/dir", make([]byte, testMaxSimple+1), 0), fs.ErrIsDirectory{})
	assert.Zero(t, g.callCount("CreateUploadSession"), "an upload session onto a folder must not be created")
	assert.ErrorIs(t, f.Move(ctx, "/dir/file.txt", "/missing/file.txt"), os.ErrNotExist)
	assert.ErrorIs(t, f.Move(ctx, "/dir/file.txt", "/dir/file.txt/x"), fs.ErrIsNotDirectory{})
}
