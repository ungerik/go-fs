package multipartfs

import (
	"bytes"
	"context"
	iofs "io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// upload is one uploaded file of a test multipart form.
type upload struct {
	field   string
	name    string
	content string
}

// newFormBody returns the encoded multipart form body
// with the passed uploads and non file form values.
func newFormBody(t *testing.T, uploads []upload, values map[string]string) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = new(bytes.Buffer)
	w := multipart.NewWriter(body)
	for _, u := range uploads {
		part, err := w.CreateFormFile(u.field, u.name)
		require.NoError(t, err)
		_, err = part.Write([]byte(u.content))
		require.NoError(t, err)
	}
	for name, value := range values {
		require.NoError(t, w.WriteField(name, value))
	}
	require.NoError(t, w.Close())
	return body, w.FormDataContentType()
}

// newFormRequest returns a POST request with a multipart form body.
func newFormRequest(t *testing.T, uploads []upload, values map[string]string) *http.Request {
	t.Helper()
	body, contentType := newFormBody(t, uploads, values)
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", contentType)
	return request
}

// newTestFS returns a MultipartFileSystem for the passed uploads and values
// that is closed at the end of the test.
func newTestFS(t *testing.T, uploads []upload, values map[string]string) *MultipartFileSystem {
	t.Helper()
	multipartFS, err := FromRequestForm(newFormRequest(t, uploads, values), 1<<20)
	require.NoError(t, err, "FromRequestForm")
	t.Cleanup(func() { _ = multipartFS.Close() })
	return multipartFS
}

func TestFromRequestForm(t *testing.T) {
	uploads := []upload{
		{field: "files", name: "hello.txt", content: "Hello, World!"},
		{field: "files", name: "empty.txt", content: ""},
		{field: "avatar", name: "me.png", content: "PNG"},
	}
	multipartFS := newTestFS(t, uploads, map[string]string{"email": "test@example.com"})

	// The file system has to be usable through the global registry,
	// because that is how a returned fs.File finds its file system.
	assert.True(t, fs.IsRegistered(multipartFS), "registered by the constructor")
	assert.Equal(t, multipartFS.Prefix(), multipartFS.ID(), "ID is the unique prefix")
	assert.Contains(t, multipartFS.String(), multipartFS.Prefix())
	readable, writable := multipartFS.ReadableWritable()
	assert.True(t, readable)
	assert.False(t, writable, "a parsed form can only be read")

	// The form values are not files, they are only reachable via the form.
	assert.Equal(t, "test@example.com", multipartFS.FormValue("email"))
	assert.Equal(t, []string{"test@example.com"}, multipartFS.FormValues("email"))
	assert.Equal(t, "", multipartFS.FormValue("missing"))
	assert.Nil(t, multipartFS.FormValues("missing"))

	// FormFile returns the first file of a form field.
	file, err := multipartFS.FormFile("files")
	require.NoError(t, err)
	assert.Equal(t, multipartFS.Prefix()+"/files/hello.txt", string(file))
	data, err := file.ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Hello, World!", string(data))
	assert.Equal(t, int64(len("Hello, World!")), file.Size(), "size from the multipart header")

	// FormFiles returns all files of a form field in upload order.
	files := multipartFS.FormFiles("files")
	require.Len(t, files, 2)
	assert.Equal(t, multipartFS.Prefix()+"/files/hello.txt", string(files[0]))
	assert.Equal(t, multipartFS.Prefix()+"/files/empty.txt", string(files[1]))
	assert.Nil(t, multipartFS.FormFiles("missing"))

	// A form field without files has no directory.
	_, err = multipartFS.FormFile("email")
	assert.ErrorIs(t, err, os.ErrNotExist, "a form value is not a file")
	_, err = multipartFS.FormFile("missing")
	assert.ErrorIs(t, err, os.ErrNotExist)

	// The MIME header of a part is only reachable via its file header.
	header, err := multipartFS.GetMultipartFileHeader(multipartFS.CleanPath("avatar", "me.png"))
	require.NoError(t, err)
	assert.Equal(t, "me.png", header.Filename)
	assert.Equal(t, "application/octet-stream", header.Header.Get("Content-Type"))
	_, err = multipartFS.GetMultipartFileHeader(multipartFS.CleanPath("avatar"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a directory has no file header")
}

func TestFromRequestForm_noMultipart(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not a form")))
	_, err := FromRequestForm(request, 1<<20)
	assert.Error(t, err, "parsing must fail instead of returning an empty file system")
}

func TestNew(t *testing.T) {
	// Forms are not always parsed from a http.Request,
	// New wraps a form that was parsed by the caller.
	body, contentType := newFormBody(t, []upload{{field: "files", name: "a.txt", content: "A"}}, nil)
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	form, err := multipart.NewReader(body, params["boundary"]).ReadForm(1 << 20)
	require.NoError(t, err)

	multipartFS := New(form)
	defer func() { _ = multipartFS.Close() }()

	assert.Same(t, form, multipartFS.Form)
	data, err := multipartFS.ReadAll(t.Context(), multipartFS.CleanPath("files", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "A", string(data))

	assert.Panics(t, func() { New(nil) }, "a nil form would panic on every later call")
}

func TestUniqueFileNames(t *testing.T) {
	// A file system path has to identify exactly one file,
	// so files uploaded with the same or an unusable name
	// must not shadow each other.
	multipartFS := newTestFS(t, []upload{
		{field: "files", name: "a.txt", content: "1"},
		{field: "files", name: "a.txt", content: "2"},
		{field: "files", name: "a.txt", content: "3"},
		{field: "files", name: "..", content: "4"},
		{field: "files", name: ".", content: "5"},
	}, nil)

	files := multipartFS.FormFiles("files")
	require.Len(t, files, 5)
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name()
	}
	assert.Equal(t, []string{"a.txt", "a (2).txt", "a (3).txt", "unnamed", "unnamed (2)"}, names)

	// Every uploaded file has to be readable under its unique name.
	for i, file := range files {
		data, err := file.ReadAll(t.Context())
		require.NoError(t, err, "read %s", file)
		assert.Equal(t, []string{"1", "2", "3", "4", "5"}[i], string(data))
	}

	// The unique names are what the directory listing reports.
	var listed []string
	err := multipartFS.ListDir(t.Context(), multipartFS.CleanPath("files"), nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.Name)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, names, listed)
}

func TestRootDir(t *testing.T) {
	multipartFS := newTestFS(t, []upload{
		{field: "files", name: "a.txt", content: "A"},
		{field: "avatar", name: "me.png", content: "PNG"},
	}, map[string]string{"email": "test@example.com"})

	// Walking the file system starts at the root directory,
	// so it has to exist and be listable.
	root := multipartFS.RootDir()
	assert.Equal(t, multipartFS.Prefix()+Separator, string(root))
	assert.True(t, root.Exists(), "the root directory always exists")
	assert.True(t, root.IsDir(), "the root directory is a directory")

	info, err := multipartFS.Stat(multipartFS.CleanPath(string(root)))
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	assert.Equal(t, root, info.File)

	// The root contains the form fields with files, not the form values.
	dirs, err := root.ListDirMax(t.Context(), -1)
	require.NoError(t, err)
	assert.Equal(t, []fs.File{
		multipartFS.JoinCleanFile("avatar"),
		multipartFS.JoinCleanFile("files"),
	}, dirs, "sorted form field directories")
}

func TestListDirPatterns(t *testing.T) {
	multipartFS := newTestFS(t, []upload{
		{field: "files", name: "a.txt", content: "A"},
		{field: "files", name: "b.md", content: "B"},
		{field: "avatar", name: "me.png", content: "PNG"},
	}, nil)

	listNames := func(dirPath string, patterns ...string) []string {
		var names []string
		err := multipartFS.ListDir(t.Context(), dirPath, patterns, func(info *fs.FileInfo) error {
			names = append(names, info.Name)
			return nil
		})
		require.NoError(t, err)
		return names
	}

	// Patterns have to be applied on every level, not just within a field.
	assert.Equal(t, []string{"avatar", "files"}, listNames(multipartFS.CleanPath("")))
	assert.Equal(t, []string{"files"}, listNames(multipartFS.CleanPath(""), "f*"))
	assert.Equal(t, []string{"a.txt", "b.md"}, listNames(multipartFS.CleanPath("files")))
	assert.Equal(t, []string{"a.txt"}, listNames(multipartFS.CleanPath("files"), "*.txt"))
	assert.Nil(t, listNames(multipartFS.CleanPath("files"), "*.jpg"))
}

func TestListDirErrors(t *testing.T) {
	multipartFS := newTestFS(t, []upload{{field: "files", name: "a.txt", content: "A"}}, nil)

	nop := func(*fs.FileInfo) error { return nil }

	err := multipartFS.ListDir(t.Context(), multipartFS.CleanPath("files", "a.txt"), nil, nop)
	var notDir fs.ErrIsNotDirectory
	assert.ErrorAs(t, err, &notDir, "listing a file reports that it is not a directory")

	err = multipartFS.ListDir(t.Context(), multipartFS.CleanPath("missing"), nil, nop)
	assert.ErrorIs(t, err, os.ErrNotExist)

	err = multipartFS.ListDir(t.Context(), multipartFS.CleanPath("files", "a.txt", "deeper"), nil, nop)
	assert.ErrorIs(t, err, os.ErrNotExist, "the file system has only two levels")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = multipartFS.ListDir(ctx, multipartFS.CleanPath("files"), nil, nop)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestClose(t *testing.T) {
	multipartFS, err := FromRequestForm(newFormRequest(t, []upload{
		{field: "files", name: "a.txt", content: "A"},
	}, nil), 1<<20)
	require.NoError(t, err)
	filePath := multipartFS.CleanPath("files", "a.txt")

	require.NoError(t, multipartFS.Close())
	assert.NoError(t, multipartFS.Close(), "Close is idempotent")
	assert.False(t, fs.IsRegistered(multipartFS), "Close unregisters the file system")

	// Close removes the uploaded files, so nothing may be served afterwards.
	// Small files are kept in memory, so this needs an explicit check.
	_, err = multipartFS.Stat(filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = multipartFS.Exists(filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = multipartFS.OpenReader(filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = multipartFS.ReadAll(t.Context(), filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = multipartFS.GetMultipartFileHeader(filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = multipartFS.ListDir(t.Context(), multipartFS.CleanPath("files"), nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = multipartFS.FormFile("files")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.Nil(t, multipartFS.FormFiles("files"))
}

func TestOpenReaderStat(t *testing.T) {
	// The reader returned by OpenReader is an io/fs.File,
	// so its Stat has to describe the uploaded part
	// under the name that identifies it in the file system.
	multipartFS := newTestFS(t, []upload{
		{field: "files", name: "a.txt", content: "A"},
		{field: "files", name: "a.txt", content: "BB"},
	}, nil)

	reader, err := multipartFS.OpenReader(multipartFS.CleanPath("files", "a (2).txt"))
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	info, err := reader.(fs.ReadCloser).Stat()
	require.NoError(t, err)
	assert.Equal(t, "a (2).txt", info.Name(), "the unique file system name, not the uploaded one")
	assert.Equal(t, int64(2), info.Size())
	assert.False(t, info.IsDir())
	assert.Equal(t, iofs.FileMode(0666), info.Mode())
	assert.True(t, info.ModTime().IsZero(), "multipart parts carry no modification time")
	assert.Nil(t, info.Sys())
}
