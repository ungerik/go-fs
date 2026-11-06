package onedrivefs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

// Small upload limits for the tests, so that the conformance suite's
// large content uses upload sessions with several fragments
const (
	testFragmentSize = fakeFragmentUnit
	testMaxSimple    = 512 << 10
)

// newTestFS returns a registered file system on a fakeGraph with small
// upload limits and short delays, closed by t.Cleanup.
func newTestFS(t *testing.T) (*fileSystem, *fakeGraph) {
	t.Helper()
	g := newFakeGraph(t, "b!test-"+strings.ReplaceAll(t.Name(), "/", "-"))
	f := newFileSystem(g.driveID, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: fakeToken}), &Options{
		HTTPClient: g.srv.Client(),
		GraphURL:   g.graphURL(),
	})
	f.maxSimple = testMaxSimple
	f.fragmentSize = testFragmentSize
	f.retryDelay = time.Millisecond
	f.pollDelay = time.Millisecond
	require.NoError(t, fs.Register(f))
	t.Cleanup(func() { _ = f.Close() })
	return f, g
}

// TestConformance runs the conformance suite offline against the fake Graph API.
func TestConformance(t *testing.T) {
	f, g := newTestFS(t)
	require.NoError(t, f.MakeDir("/conformance", 0))
	fstest.RunConformance(t, f, fstest.Config{
		Name:    "OneDrive",
		Prefix:  f.Prefix(),
		TestDir: "/conformance",
	})
	assert.Positive(t, g.callCount("UploadFragment"), "the large content must be uploaded with an upload session")
	assert.Positive(t, g.callCount("Copy"), "copies must be server-side")
	assert.Positive(t, g.callCount("Monitor"), "copies must be awaited")
	assert.Zero(t, g.leaks, "the access token must not be sent to pre-authenticated URLs")
}

// TestConformanceOnline runs the conformance suite against a real drive
// if ONEDRIVE_ACCESS_TOKEN is set, see README.md.
func TestConformanceOnline(t *testing.T) {
	accessToken := os.Getenv("ONEDRIVE_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("Skipping OneDrive filesystem test: ONEDRIVE_ACCESS_TOKEN environment variable not set")
	}
	drive := os.Getenv("ONEDRIVE_DRIVE")
	if drive == "" {
		drive = "me/drive"
	}
	testDir := os.Getenv("ONEDRIVE_TEST_DIR")
	if testDir == "" {
		testDir = "/go-fs-test"
	}

	odfs, err := New(t.Context(), drive, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken}), nil)
	require.NoError(t, err, "New")
	assert.Equal(t, Prefix+odfs.ID(), odfs.Prefix())
	t.Cleanup(func() {
		if err := odfs.Close(); err != nil {
			t.Logf("Error closing filesystem: %v", err)
		}
	})
	if err := odfs.(fs.WriteFileSystem).MakeDir(testDir, 0); err != nil && !errors.Is(err, os.ErrExist) {
		require.NoError(t, err, "MakeDir(%q)", testDir)
	}

	fstest.RunConformance(t, odfs, fstest.Config{
		Name:    "OneDrive",
		Prefix:  odfs.Prefix(),
		TestDir: testDir,
	})
}

// TestNew checks the drive request with the access token,
// the forms of the drive argument and the identity of the file system.
func TestNew(t *testing.T) {
	g := newFakeGraph(t, "b!drive-id")
	tokens := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: fakeToken})
	opts := &Options{HTTPClient: g.srv.Client(), GraphURL: g.graphURL() + "/"}
	for _, drive := range []string{"me/drive", "/me/drive/", "b!drive-id", "drives/b!drive-id"} {
		odfs, err := New(t.Context(), drive, tokens, opts)
		require.NoError(t, err, "New(%q)", drive)
		assert.Equal(t, "b!drive-id", odfs.ID())
		assert.Equal(t, "onedrive://b!drive-id", odfs.Prefix())
		assert.Equal(t, "OneDrive", odfs.Name())
		assert.Equal(t, "OneDrive onedrive://b!drive-id", odfs.String())
	}

	_, err := New(t.Context(), "me/drive", oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "wrong-token"}), opts)
	assert.ErrorContains(t, err, "InvalidAuthenticationToken")
	assert.NotContains(t, err.Error(), "wrong-token", "the token must not be part of the error")
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))

	_, err = New(t.Context(), "users/nobody/drive", tokens, opts)
	assert.ErrorContains(t, err, "itemNotFound")
	for _, drive := range []string{"", "/", "me/drive?x", "drives/../me/drive"} {
		_, err = New(t.Context(), drive, tokens, opts)
		assert.ErrorIs(t, err, os.ErrInvalid, "New(%q)", drive)
	}
	_, err = New(t.Context(), "me/drive", nil, opts)
	assert.ErrorContains(t, err, "no token source")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = New(ctx, "me/drive", tokens, opts)
	assert.ErrorIs(t, err, context.Canceled)
	err = NewAndRegister(ctx, "me/drive", tokens, opts)
	assert.ErrorIs(t, err, context.Canceled)

	require.NoError(t, NewAndRegister(t.Context(), "me/drive", tokens, opts))
	registered, ok := fs.GetFileSystemByPrefixOrNil("onedrive://b!drive-id").(*fileSystem)
	require.True(t, ok, "NewAndRegister must register the file system")
	require.NoError(t, registered.Close())
	assert.False(t, fs.IsRegistered(registered), "Close must unregister the file system")
}

// TestMove_ReplaceOnlyCheckedVersion checks that Move and CopyFile replace
// only the version of the destination file that relocate checked: another
// client uploads a newer version between the check and the delete,
// which must survive the failed Move or CopyFile.
func TestMove_ReplaceOnlyCheckedVersion(t *testing.T) {
	for name, relocate := range map[string]func(f *fileSystem, ctx context.Context, src, dest string) error{
		"Move":     (*fileSystem).Move,
		"CopyFile": (*fileSystem).CopyFile,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f, g := newTestFS(t)
			g.put("/src.txt", []byte("src"))
			g.put("/dest.txt", []byte("dest"))
			g.onCall("Delete", func() {
				dest := g.items["/dest.txt"]
				dest.data = []byte("newer")
				dest.version++
			})

			err := relocate(f, ctx, "/src.txt", "/dest.txt")
			assert.ErrorIs(t, err, os.ErrExist, "the newer destination is in the way")
			assert.Equal(t, 1, g.callCount("Delete"), "the hook must have replaced the destination")
			assert.Equal(t, "newer", string(g.get("/dest.txt").data), "the newer version must not be deleted")
			assert.Equal(t, "src", string(g.get("/src.txt").data), "the source must be kept")
		})
	}
}

// TestMove_OntoOwnAlias is the regression test for a Move or CopyFile
// onto a name that the server takes for another name of the source, like
// another Unicode normalization of it, and refuses as a conflict:
// the conflict handling deleted the destination item, which was the
// source, so the only copy went to the recycle bin.
func TestMove_OntoOwnAlias(t *testing.T) {
	for name, relocate := range map[string]func(f *fileSystem, ctx context.Context, src, dest string) error{
		"Move":     (*fileSystem).Move,
		"CopyFile": (*fileSystem).CopyFile,
	} {
		t.Run(name, func(t *testing.T) {
			f, g := newTestFS(t)
			src := g.put("/caf\u00e9.txt", []byte("only copy"))
			g.mtx.Lock()
			g.items["/cafe\u0301.txt"] = src // the server's alias of the source
			g.mtx.Unlock()
			conflict := fakeFailure{status: http.StatusConflict, code: "nameAlreadyExists"}
			g.setFailNext("Patch", conflict, conflict)

			err := relocate(f, t.Context(), "/caf\u00e9.txt", "/cafe\u0301.txt")
			require.NoError(t, err)
			assert.Zero(t, g.callCount("Delete"), "the source must not be deleted")
			assert.Equal(t, "only copy", string(src.data))
			assert.Same(t, src, g.get(src.path), "the source must still be in the drive")
		})
	}
}

// TestCopyFile_Monitor checks the answers of the copy monitor: a redirect
// to the new item is a completed copy, a status this version doesn't know
// is an error instead of an endless wait, and a failure of the monitor
// is not a missing source.
func TestCopyFile_Monitor(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f, g := newTestFS(t)
	g.put("/src.txt", []byte("src"))

	g.setFailNext("Monitor", fakeFailure{status: http.StatusSeeOther})
	require.NoError(t, f.CopyFile(ctx, "/src.txt", "/a.txt"))
	assert.Equal(t, "src", string(g.get("/a.txt").data))

	g.setFailNext("Monitor", fakeFailure{status: http.StatusOK, code: "unknownStatus"})
	err := f.CopyFile(ctx, "/src.txt", "/b.txt")
	assert.ErrorContains(t, err, "unknownStatus")
	assert.NoError(t, ctx.Err(), "the copy must not wait for the deadline")

	g.setFailNext("Monitor", fakeFailure{status: http.StatusNotFound, code: "itemNotFound"})
	err = f.CopyFile(ctx, "/src.txt", "/c.txt")
	assert.Equal(t, http.StatusNotFound, statusOf(err))
	assert.NotErrorIs(t, err, os.ErrNotExist, "the monitor is gone, not the source")
}

// TestCopyFile_SourceBecameFolder checks a CopyFile whose source became
// a folder after the check: the conflict handling checks the source again
// before it deletes the destination file.
func TestCopyFile_SourceBecameFolder(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/src", []byte("src"))
	g.put("/dest.txt", []byte("dest"))
	g.onCall("Copy", func() {
		delete(g.items, "/src")
		g.items["/src"] = &fakeItem{path: "/src", isDir: true, id: "folder", version: 1}
		g.items["/src/a.txt"] = &fakeItem{path: "/src/a.txt", data: []byte("a"), id: "a", version: 1}
	})

	err := f.CopyFile(ctx, "/src", "/dest.txt")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.Equal(t, "dest", string(g.get("/dest.txt").data), "the destination must not be deleted")
	assert.Zero(t, g.callCount("Delete"), "nothing must be deleted")
}

// TestMove_CaseOnly checks a rename that only changes the case of the
// name, and the rename through a temporary name if the server refuses
// it as a conflict, which must not delete the file.
func TestMove_CaseOnly(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/name.txt", []byte("content"))

	require.NoError(t, f.Move(ctx, "/name.txt", "/Name.txt"))
	assert.Equal(t, "/Name.txt", g.get("/name.txt").path)
	assert.NoError(t, f.CopyFile(ctx, "/name.txt", "/NAME.TXT"), "a copy onto itself is a no-op")
	assert.Zero(t, g.callCount("Copy"))

	g.setFailNext("Patch", fakeFailure{status: http.StatusConflict, code: "nameAlreadyExists"})
	require.NoError(t, f.Move(ctx, "/Name.txt", "/NAME.txt"))
	assert.Equal(t, "/NAME.txt", g.get("/name.txt").path)
	assert.Equal(t, "content", string(g.get("/name.txt").data))
	assert.Len(t, g.items, 2, "no temporary file is left behind")

	// The original name is restored if the rename from the temporary name fails
	g.setFailNext("Patch", fakeFailure{status: http.StatusConflict, code: "nameAlreadyExists"}, fakeFailure{}, fakeFailure{status: http.StatusInternalServerError})
	err := f.Move(ctx, "/NAME.txt", "/name.txt")
	assert.Equal(t, http.StatusInternalServerError, statusOf(err))
	assert.Equal(t, "/NAME.txt", g.get("/name.txt").path)
	assert.Len(t, g.items, 2, "no temporary file is left behind")

	// Only the case of a parent differs: the same item, nothing to rename
	g.put("/dir/a.txt", []byte("a"))
	patches := g.callCount("Patch")
	require.NoError(t, f.Move(ctx, "/dir/a.txt", "/DIR/a.txt"))
	assert.Equal(t, patches, g.callCount("Patch"))
	assert.Equal(t, "/dir/a.txt", g.get("/dir/a.txt").path)
}

// TestCopyFileAndMove_Errors checks the errors of CopyFile and Move
// that the conformance suite doesn't reach, and that nothing is copied
// or moved for them.
func TestCopyFileAndMove_Errors(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/dir/a.txt", []byte("a"))
	g.put("/file.txt", []byte("file"))

	assert.ErrorIs(t, f.CopyFile(ctx, "/missing.txt", "/new.txt"), os.ErrNotExist)
	assert.ErrorIs(t, f.CopyFile(ctx, "/dir", "/new"), fs.ErrIsDirectory{}, "Graph would copy the whole folder")
	assert.ErrorIs(t, f.CopyFile(ctx, "/file.txt", "/"), fs.ErrIsDirectory{})
	assert.ErrorIs(t, f.CopyFile(ctx, "/file.txt", "/dir/a.txt/x"), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.CopyFile(ctx, "/file.txt", "/missing/x"), os.ErrNotExist)
	assert.Zero(t, g.callCount("Copy"))

	assert.ErrorIs(t, f.Move(ctx, "/missing.txt", "/new.txt"), os.ErrNotExist)
	assert.ErrorIs(t, f.Move(ctx, "/file.txt", "/"), os.ErrExist)
	assert.ErrorIs(t, f.Move(ctx, "/file.txt", "/dir/a.txt/x"), fs.ErrIsNotDirectory{})
	assert.Equal(t, "file", string(g.get("/file.txt").data))
	assert.Equal(t, "a", string(g.get("/dir/a.txt").data))
}

// TestNotebookAndShortcut checks that a OneNote notebook, which has
// children but no folder facet, is a directory that Remove never
// deletes, because Graph deletes it with its sections, and that
// a shortcut is neither a file nor a directory.
func TestNotebookAndShortcut(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/Notebook/Section.one", []byte("section"))
	g.get("/Notebook").kind = "package"
	g.put("/Shared", nil)
	g.get("/Shared").kind = "remoteItem"

	info, err := f.Stat("/Notebook")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	assert.ErrorIs(t, f.Remove("/Notebook"), fs.ErrDirectoryNotEmpty{})
	assert.Zero(t, g.callCount("Delete"))
	assert.NotNil(t, g.get("/Notebook/Section.one"))

	var listed []string
	require.NoError(t, f.ListDir(ctx, "/Notebook", nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.Name)
		return nil
	}))
	assert.Equal(t, []string{"Section.one"}, listed)

	info, err = f.Stat("/Shared")
	require.NoError(t, err)
	assert.False(t, info.IsDir, "a shortcut is not a directory of this drive")
	assert.False(t, info.IsRegular, "a shortcut is not a file")
}

// TestRootAndEmptyPath checks that the root is reported without
// a request, that the methods refuse an empty path, which would
// otherwise clean to the root, and that the root is not a file.
func TestRootAndEmptyPath(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)

	info, err := f.Stat("/")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	assert.Equal(t, f.RootDir(), info.File)
	assert.Empty(t, g.calls, "the root is reported without a request")

	_, err = f.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, f.ListDir(ctx, "", nil, func(*fs.FileInfo) error { return nil }), fs.ErrEmptyPath)
	_, err = f.OpenReader("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = f.OpenWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, f.WriteAll(ctx, "", nil, 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.RemoveAll(ctx, ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.CopyFile(ctx, "", "/x"), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Move(ctx, "/x", ""), fs.ErrEmptyPath)
	assert.Empty(t, g.calls, "no request for an empty path")

	_, err = f.OpenReader("/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	_, err = f.OpenWriter("/", 0)
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.ErrorIs(t, f.WriteAll(ctx, "/", []byte("x"), 0), fs.ErrIsDirectory{})
	assert.ErrorIs(t, f.MakeDir("/", 0), os.ErrExist)
}

// TestRemove checks that Remove refuses folders with content,
// deletes only the checked version, and the errors of Remove and RemoveAll.
func TestRemove(t *testing.T) {
	ctx := t.Context()
	f, g := newTestFS(t)
	g.put("/dir/a.txt", []byte("a"))
	assert.ErrorIs(t, f.Remove("/dir"), fs.ErrDirectoryNotEmpty{})
	assert.ErrorIs(t, f.Remove("/missing"), os.ErrNotExist)
	assert.ErrorIs(t, f.Remove("/"), os.ErrInvalid)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/"), os.ErrInvalid)
	assert.NoError(t, f.RemoveAll(ctx, "/missing"))

	g.onCall("Delete", func() { g.items["/dir/a.txt"].version++ })
	err := f.Remove("/dir/a.txt")
	assert.Equal(t, http.StatusPreconditionFailed, statusOf(err), "a changed file must not be deleted")
	assert.NotNil(t, g.get("/dir/a.txt"))

	require.NoError(t, f.Remove("/dir/a.txt"))
	require.NoError(t, f.Remove("/dir"))
	require.NoError(t, f.WriteAll(ctx, "/tree/sub/b.txt", []byte("b"), 0))
	require.NoError(t, f.RemoveAll(ctx, "/tree"))
	assert.Len(t, g.items, 1, "only the root must be left")
}

// TestTouch checks that Touch creates an empty file and sets the
// modification time of an existing file without changing its content.
func TestTouch(t *testing.T) {
	f, g := newTestFS(t)
	require.NoError(t, f.Touch("/new.txt", 0))
	assert.Empty(t, g.get("/new.txt").data)

	g.put("/existing.txt", []byte("keep"))
	g.get("/existing.txt").modified = time.Now().Add(-time.Hour)
	before := time.Now().Add(-time.Second)
	require.NoError(t, f.Touch("/existing.txt", 0))
	info, err := f.Stat("/existing.txt")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(g.get("/existing.txt").data))
	assert.False(t, info.Modified.Before(before), "Modified %s must be the time of Touch", info.Modified)

	// A file created between the PATCH and the upload
	g.onCall("PutContent", func() {
		g.items["/race.txt"] = &fakeItem{path: "/race.txt", data: []byte("other"), id: "race", version: 1}
	})
	require.NoError(t, f.Touch("/race.txt", 0))
	assert.Equal(t, "other", string(g.get("/race.txt").data), "Touch must not truncate a file")

	// Below a file the upload is refused, and the file stays
	assert.ErrorIs(t, f.Touch("/existing.txt/x", 0), fs.ErrIsNotDirectory{})
	assert.Equal(t, "keep", string(g.get("/existing.txt").data))

	// A conflict without a file at the path is not a touched file
	g.setFailNext("PutContent", fakeFailure{status: http.StatusConflict, code: "nameAlreadyExists"})
	assert.Error(t, f.Touch("/conflict.txt", 0))
	assert.Nil(t, g.get("/conflict.txt"))
}

// TestClosedFileSystem verifies that after Close every method
// returns fs.ErrFileSystemClosed without a request.
func TestClosedFileSystem(t *testing.T) {
	f, g := newTestFS(t)
	require.NoError(t, f.Close())
	assert.False(t, fs.IsRegistered(f), "Close must unregister the filesystem")
	assert.NoError(t, f.Close(), "Close is idempotent")

	ctx := t.Context()
	_, err := f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.WriteAll(ctx, "/file", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Touch("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Remove("/file"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.CopyFile(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	_, err = f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil }), fs.ErrFileSystemClosed)
	assert.Empty(t, g.calls, "no request must be sent after Close")
}

// TestValidateName verifies the names that a copy or move into the file
// system rejects with ValidateName before it writes anything.
func TestValidateName(t *testing.T) {
	f := &fileSystem{}
	for _, name := range []string{"a.txt", "Größe.txt", "#hash%.txt", ".hidden", "forms", "con-tract.txt", "a.lock"} {
		assert.NoError(t, f.ValidateName(name), "%q", name)
	}
	for _, name := range []string{"\xff.txt", "a\x00b", "tab\t", "a:b", `a\b`, " lead", "trail ", "trail.", ".lock", "Desktop.ini", "~$doc.docx", "x_vti_y", "CON", "nul.txt", "com1 .txt", "LPT9"} {
		assert.ErrorIs(t, f.ValidateName(name), os.ErrInvalid, "%q", name)
	}
}
