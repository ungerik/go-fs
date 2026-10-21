package multipartfs

import (
	"bytes"
	"context"
	"io"
	iofs "io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// upload is one uploaded file of a test multipart form.
type upload struct {
	field   string
	name    string
	content string
}

// dispositionEscaper escapes a quoted Content-Disposition parameter
// the same way mime/multipart.Writer.CreateFormFile does.
var dispositionEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// newFormBody returns the encoded multipart form body
// with the passed uploads and non file form values.
// The file parts get raw Content-Disposition headers,
// so field and file names reach mime/multipart as a browser sends them.
func newFormBody(t *testing.T, uploads []upload, values map[string]string) (body *bytes.Buffer, contentType string) {
	t.Helper()
	body = new(bytes.Buffer)
	w := multipart.NewWriter(body)
	for _, u := range uploads {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="`+dispositionEscaper.Replace(u.field)+`"; filename="`+dispositionEscaper.Replace(u.name)+`"`)
		header.Set("Content-Type", "application/octet-stream")
		part, err := w.CreatePart(header)
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
	multipartFS, err := NewFromRequest(newFormRequest(t, uploads, values), 1<<20, "")
	require.NoError(t, err, "NewFromRequest")
	require.NoError(t, fs.Register(multipartFS))
	t.Cleanup(func() { _ = multipartFS.Close() })
	return multipartFS
}

func TestNewFromRequest(t *testing.T) {
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

func TestNewFromRequest_noMultipart(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader([]byte("not a form")))
	_, err := NewFromRequest(request, 1<<20, "")
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

	multipartFS, err := New(form, "")
	require.NoError(t, err)
	require.NoError(t, fs.Register(multipartFS))
	defer func() { _ = multipartFS.Close() }()

	assert.Same(t, form, multipartFS.Form)
	data, err := multipartFS.ReadAll(t.Context(), multipartFS.CleanPath("files", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "A", string(data))

	_, err = New(nil, "")
	assert.Error(t, err, "a nil form would panic on every later call")
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
	multipartFS, err := NewFromRequest(newFormRequest(t, []upload{
		{field: "files", name: "a.txt", content: "A"},
	}, nil), 1<<20, "")
	require.NoError(t, err, "NewFromRequest")
	require.NoError(t, fs.Register(multipartFS))
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
	assert.Equal(t, iofs.FileMode(0444), info.Mode(), "read-only like the Permissions of Stat")
	assert.True(t, info.ModTime().IsZero(), "multipart parts carry no modification time")
	assert.Nil(t, info.Sys())

	// The reader and the file system must agree on the permissions,
	// the reader used to report 0666 for a read-only file system
	fsInfo, err := multipartFS.Stat(multipartFS.CleanPath("files", "a (2).txt"))
	require.NoError(t, err)
	assert.Equal(t, fsInfo.Permissions, fs.PermissionsFromStdFileInfo(info))
	assert.Equal(t, fs.AllRead, fsInfo.Permissions)
}

// TestOpenReader_Directory verifies that opening the root or a form field
// directory reports fs.ErrIsDirectory. Both used to be reported as missing,
// although Stat describes them as existing directories.
func TestOpenReader_Directory(t *testing.T) {
	multipartFS := newTestFS(t, []upload{{field: "files", name: "a.txt", content: "A"}}, nil)

	for _, dirPath := range []string{multipartFS.CleanPath(""), multipartFS.CleanPath("files")} {
		_, err := multipartFS.OpenReader(dirPath)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenReader(%q)", dirPath)
		assert.NotErrorIs(t, err, os.ErrNotExist, "OpenReader(%q)", dirPath)
		_, err = multipartFS.ReadAll(t.Context(), dirPath)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "ReadAll(%q)", dirPath)
	}

	// Missing paths are still reported as missing
	_, err := multipartFS.OpenReader(multipartFS.CleanPath("missing"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = multipartFS.OpenReader(multipartFS.CleanPath("files", "missing.txt"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	// After Close a directory is reported as closed, not as directory
	require.NoError(t, multipartFS.Close())
	_, err = multipartFS.OpenReader(multipartFS.CleanPath("files"))
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// newDiskFS returns a MultipartFileSystem whose uploads larger than one
// byte are stored in temporary files in a new directory, which is returned.
func newDiskFS(t *testing.T, uploads []upload, values map[string]string) (*MultipartFileSystem, string) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir) // os.TempDir on Unix
	t.Setenv("TMP", tempDir)    // os.TempDir on Windows
	multipartFS, err := NewFromRequest(newFormRequest(t, uploads, values), 1, "")
	require.NoError(t, err, "NewFromRequest")
	require.NotEmpty(t, dirEntries(t, tempDir), "the uploads are stored in temporary files")
	t.Cleanup(func() { _ = multipartFS.Close() })
	return multipartFS, tempDir
}

func dirEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	return entries
}

// TestClose_ClosesReaders verifies that Close closes the readers that are
// still open before it removes the temporary files. On Windows open files
// can't be removed, so the temporary files leaked.
func TestClose_ClosesReaders(t *testing.T) {
	multipartFS, tempDir := newDiskFS(t, []upload{
		{field: "files", name: "a.txt", content: "stored on disk"},
	}, map[string]string{"comment": "kept in memory"})

	reader, err := multipartFS.OpenReader(multipartFS.CleanPath("files", "a.txt"))
	require.NoError(t, err)
	buf := make([]byte, 6)
	_, err = io.ReadFull(reader, buf)
	require.NoError(t, err)

	require.NoError(t, multipartFS.Close())
	assert.Empty(t, dirEntries(t, tempDir), "the temporary files are removed")
	_, err = reader.Read(buf)
	assert.ErrorIs(t, err, os.ErrClosed, "Close of the file system closed the reader")
	assert.NoError(t, reader.Close(), "closing a closed reader is no error")

	// The form values are kept in memory and stay usable, as documented
	assert.Equal(t, "kept in memory", multipartFS.FormValue("comment"))
	assert.Equal(t, []string{"kept in memory"}, multipartFS.FormValues("comment"))
}

// TestClose_Retry verifies that a Close that failed to remove the
// temporary files returns the error and can be retried. Close used to
// mark the file system as closed first, so a second Close returned nil
// and the files leaked.
func TestClose_Retry(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory that denies removing files")
	}
	multipartFS, tempDir := newDiskFS(t, []upload{
		{field: "files", name: "a.txt", content: "stored on disk"},
	}, nil)
	filePath := multipartFS.CleanPath("files", "a.txt")

	require.NoError(t, os.Chmod(tempDir, 0o500)) //#nosec G302 -- the test directory must deny removing files
	t.Cleanup(func() { _ = os.Chmod(tempDir, 0o700) })
	assert.Error(t, multipartFS.Close(), "the temporary files can't be removed")
	_, err := multipartFS.Stat(filePath)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "the file system is closed even if removing failed")
	assert.NotEmpty(t, dirEntries(t, tempDir))

	require.NoError(t, os.Chmod(tempDir, 0o700)) //#nosec G302 -- restore the permissions of the test directory
	require.NoError(t, multipartFS.Close(), "the retry removes the temporary files")
	assert.Empty(t, dirEntries(t, tempDir))
	require.NoError(t, multipartFS.Close(), "Close is idempotent after success")
}

// fileSystemName returns the file system name of a file uploaded as name:
// mime/multipart reduces it with filepath.Base, which on Windows also cuts
// at backslashes, and names that are no path element become "unnamed".
func fileSystemName(name string) string {
	name = path.Base(filepath.Base(name))
	switch name {
	case ".", "..", "/":
		return "unnamed"
	}
	return name
}

// checkFormFile asserts that file, as returned by FormFile or FormFiles,
// resolves to the upload u through every fs.File method that reads it.
// dirName is the expected directory name of the form field.
func checkFormFile(t *testing.T, formFS *MultipartFileSystem, file fs.File, u upload, dirName string) {
	t.Helper()
	name := fileSystemName(u.name)

	assert.Equal(t, name, file.Name(), "Name")
	assert.Equal(t, "/"+dirName+"/"+name, file.Path(), "Path")
	assert.Equal(t, fsimpl.Ext(name, "/"), file.Ext(), "Ext")
	data, err := file.ReadAll(t.Context())
	if assert.NoError(t, err, "ReadAll") {
		assert.Equal(t, u.content, string(data), "ReadAll reads the upload")
	}

	reader, err := file.OpenReader()
	if assert.NoError(t, err, "OpenReader") {
		data, err = io.ReadAll(reader)
		assert.NoError(t, err)
		assert.Equal(t, u.content, string(data), "OpenReader reads the upload")
		assert.NoError(t, reader.Close())
	}

	assert.True(t, file.Exists(), "Exists")
	info, err := file.Stat()
	if assert.NoError(t, err, "Stat") {
		assert.Equal(t, name, info.Name(), "Stat name")
		assert.Equal(t, int64(len(u.content)), info.Size(), "Stat size")
	}

	// The path of a file has to address the same upload
	// when passed to the file system methods.
	header, err := formFS.GetMultipartFileHeader(file.Path())
	if assert.NoError(t, err, "GetMultipartFileHeader(file.Path())") {
		assert.Equal(t, filepath.Base(u.name), header.Filename, "header of the upload")
	}
	data, err = formFS.ReadAll(t.Context(), file.Path())
	if assert.NoError(t, err, "ReadAll(file.Path())") {
		assert.Equal(t, u.content, string(data))
	}
	assert.Equal(t, file, formFS.File(file.Path()), "File(file.Path()) is the same file")

	// The parent directory is the form field
	dir := file.Dir()
	assert.Equal(t, dirName, dir.Name(), "Dir().Name()")
	assert.True(t, dir.IsDir(), "Dir() is a directory")
	assert.Equal(t, file, dir.Join(name), "Dir().Join(Name()) is the file")
	var listed []*fs.FileInfo
	err = dir.ListDirInfo(t.Context(), func(info *fs.FileInfo) error {
		if info.Name == name {
			listed = append(listed, info)
		}
		return nil
	})
	if assert.NoError(t, err, "Dir().ListDirInfo") && assert.Len(t, listed, 1, "Dir().ListDirInfo lists the file once") {
		assert.Equal(t, file, listed[0].File, "ListDirInfo reports the same file")
		data, err = listed[0].File.ReadAll(t.Context())
		if assert.NoError(t, err, "ReadAll of the listed file") {
			assert.Equal(t, u.content, string(data))
		}
	}
}

func TestFormFiles_fileNames(t *testing.T) {
	// Every name mime/multipart can yield has to address exactly its upload,
	// especially names that look URL escaped, which must not be decoded.
	names := []string{
		"report 1.pdf",
		"50% off.pdf",
		"report%201.pdf",
		"100%25.txt",
		`say "hi".txt`,
		`"`,
		"a%2Fb.txt",
		"..%2Fother%2Fx.txt",
		"..%2F..",
		"%2E%2E",
		"%2e",
		"%",
		"%%",
		"%2",
		"%zz.txt",
		"%00.txt",
		`back\slash.txt`,
		"Ünïcödé 文件 🚀.pdf",
		"+plus+ & ?query=1#hash.txt",
		" spaces  around .txt ",
		".hidden",
		"..",
		".",
		"/", // filepath.Base("/") is "/"
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			u := upload{field: "doc", name: name, content: "content of " + name}
			formFS := newTestFS(t, []upload{u}, nil)

			files := formFS.FormFiles("doc")
			require.Len(t, files, 1)
			checkFormFile(t, formFS, files[0], u, "doc")

			file, err := formFS.FormFile("doc")
			require.NoError(t, err)
			assert.Equal(t, files[0], file, "FormFile returns the first of FormFiles")
		})
	}
}

func TestFormFiles_fieldNames(t *testing.T) {
	// Form field names are not sanitized by mime/multipart,
	// they can contain anything, including separators.
	fields := []struct {
		field   string
		dirName string
	}{
		{field: "doc", dirName: "doc"},
		{field: "a b", dirName: "a b"},
		{field: "a/b", dirName: "a%2Fb"},
		{field: "/", dirName: "%2F"},
		{field: "a%2Fb", dirName: "a%252Fb"},
		{field: "100%", dirName: "100%25"},
		{field: "100%25", dirName: "100%2525"},
		{field: `say "hi"`, dirName: `say "hi"`},
		{field: `back\slash`, dirName: `back\slash`},
		{field: "..", dirName: "%2E%2E"},
		{field: ".", dirName: "%2E"},
		{field: "%2E%2E", dirName: "%252E%252E"},
		{field: "../other", dirName: "..%2Fother"},
		{field: "Ünïcödé", dirName: "Ünïcödé"},
	}
	for _, tc := range fields {
		t.Run(tc.field, func(t *testing.T) {
			u := upload{field: tc.field, name: "file.txt", content: "content of " + tc.field}
			formFS := newTestFS(t, []upload{u}, nil)

			files := formFS.FormFiles(tc.field)
			require.Len(t, files, 1)
			checkFormFile(t, formFS, files[0], u, tc.dirName)

			var dirs []string
			err := formFS.RootDir().ListDirInfo(t.Context(), func(info *fs.FileInfo) error {
				dirs = append(dirs, info.Name)
				assert.Equal(t, files[0].Dir(), info.File, "root listing reports the field directory")
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, []string{tc.dirName}, dirs, "root listing has the directory name of the field")
		})
	}
}

func TestFormFiles_crossField(t *testing.T) {
	// A file name of one field must not be able to address
	// the upload of another field: "doc/../other/x.txt"
	// cleaned would be "/other/x.txt".
	doc := upload{field: "doc", name: "..%2Fother%2Fx.txt", content: "doc upload"}
	other := upload{field: "other", name: "x.txt", content: "other upload"}
	formFS := newTestFS(t, []upload{doc, other}, nil)

	docFiles := formFS.FormFiles("doc")
	require.Len(t, docFiles, 1)
	checkFormFile(t, formFS, docFiles[0], doc, "doc")

	otherFiles := formFS.FormFiles("other")
	require.Len(t, otherFiles, 1)
	checkFormFile(t, formFS, otherFiles[0], other, "other")
}

func TestFormFiles_similarNames(t *testing.T) {
	// Field and file names that are the same after URL decoding
	// or path cleaning must still address their own uploads.
	uploads := []upload{
		{field: "doc", name: "report 1.pdf"},
		{field: "doc", name: "report%201.pdf"},
		{field: "doc", name: "report%25201.pdf"},
		{field: "doc", name: "%2E%2E"},
		{field: "doc", name: "%2E"},
		{field: "doc", name: "%2F"},
		{field: "doc", name: "..%2Fother%2Fx.txt"},
		{field: "a", name: "b%2Fc"},
		{field: "a/b", name: "c"},
		{field: "a%2Fb", name: "c"},
		{field: "..", name: "x.txt"},
		{field: "%2E%2E", name: "x.txt"},
		{field: "other", name: "x.txt"},
		{field: "doc/../other", name: "x.txt"},
	}
	byField := make(map[string][]upload)
	for i := range uploads {
		uploads[i].content = "upload " + uploads[i].field + " " + uploads[i].name
		byField[uploads[i].field] = append(byField[uploads[i].field], uploads[i])
	}
	formFS := newTestFS(t, uploads, nil)

	for field, fieldUploads := range byField {
		files := formFS.FormFiles(field)
		require.Len(t, files, len(fieldUploads), "field %q", field)
		for i, u := range fieldUploads {
			checkFormFile(t, formFS, files[i], u, fieldDir(field))
		}
	}
}
