package ftpfs

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

const (
	testUser     = "testuser"
	testPassword = "testpass"
)

// testAddress returns the address of the test server with the test user.
func testAddress(srv *testFTPServer) string {
	return fmt.Sprintf("ftp://%s@%s", testUser, srv.addr())
}

// dialInProcess connects to an in-process test FTP server (see
// testserver_test.go), so the read/write code paths can be exercised
// without Docker or a real server. The file system is closed when the
// test finishes.
func dialInProcess(t *testing.T, srv *testFTPServer) *fileSystem {
	t.Helper()
	return dialInProcessWith(t, srv, &Options{Password: testPassword})
}

// dialInProcessWith dials the test server with opts.
func dialInProcessWith(t *testing.T, srv *testFTPServer, opts *Options) *fileSystem {
	t.Helper()
	ftpFS, err := Dial(t.Context(), testAddress(srv), opts)
	require.NoError(t, err, "Dial in-process FTP server")
	t.Cleanup(func() { ftpFS.Close() })
	return ftpFS.(*fileSystem)
}

// Test_fileSystem_InProcess exercises the read/write methods against an
// in-process FTP server. These tests guard against the regressions that the
// removed random-access file type had: OpenWriter overwriting instead of
// accumulating multi-chunk writes, and OpenReadWriter never advancing the
// read offset (re-reading forever, never reaching EOF).
func Test_fileSystem_InProcess(t *testing.T) {
	ctx := t.Context()

	t.Run("OpenWriter accumulates multi-chunk writes", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		w, err := ftpFS.OpenWriter("/multi.txt", 0)
		require.NoError(t, err, "OpenWriter")
		for _, chunk := range []string{"Hello, ", "FTP ", "world!"} {
			n, err := w.Write([]byte(chunk))
			require.NoError(t, err, "Write")
			require.Equal(t, len(chunk), n, "Write byte count")
		}
		require.NoError(t, w.Close(), "Close writer")

		// The bug: each Write issued a fresh STOR, so only the last chunk
		// survived. The whole concatenation must be stored instead.
		assert.Equal(t, "Hello, FTP world!", string(srv.fileContent("/multi.txt")))
	})

	t.Run("OpenReader reads full content", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		content := []byte("line one\nline two\nline three\n")
		require.NoError(t, ftpFS.WriteAll(ctx, "/read.txt", content, 0))

		r, err := ftpFS.OpenReader("/read.txt")
		require.NoError(t, err, "OpenReader")
		got, err := io.ReadAll(r)
		require.NoError(t, err, "io.ReadAll")
		require.NoError(t, r.Close(), "Close reader")
		require.NoError(t, r.Close(), "second Close of the reader is a no-op")
		assert.Equal(t, content, got)
	})

	t.Run("ReadAll and WriteAll round-trip", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		content := []byte("round-trip content")
		require.NoError(t, ftpFS.WriteAll(ctx, "/rt.txt", content, 0), "WriteAll")
		got, err := ftpFS.ReadAll(ctx, "/rt.txt")
		require.NoError(t, err, "ReadAll")
		assert.Equal(t, content, got)
	})

	t.Run("WriteAll truncates existing content", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		require.NoError(t, ftpFS.WriteAll(ctx, "/trunc.txt", []byte("a long initial content"), 0))
		require.NoError(t, ftpFS.WriteAll(ctx, "/trunc.txt", []byte("short"), 0))

		// No stale trailing bytes from the longer first write.
		assert.Equal(t, "short", string(srv.fileContent("/trunc.txt")))
	})

	t.Run("Touch preserves existing content and creates missing files", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		// Touching an existing file must NOT truncate it. The generic
		// emulation opens the file with O_TRUNC and would destroy "keep me".
		require.NoError(t, ftpFS.WriteAll(ctx, "/touch.txt", []byte("keep me"), 0))
		srv.configure(func(s *testFTPServer) { s.nodes["/touch.txt"].modified = time.Now().Add(-time.Hour) })
		require.NoError(t, ftpFS.Touch("/touch.txt", 0), "Touch existing")
		assert.Equal(t, "keep me", string(srv.fileContent("/touch.txt")), "Touch must preserve existing content")
		assert.Equal(t, 1, srv.count("MFMT"), "Touch must set the time with MFMT")
		info, err := ftpFS.Stat("/touch.txt")
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now(), info.Modified, time.Minute, "Touch must update the modification time")

		// Touching a missing file creates it (empty).
		require.NoError(t, ftpFS.Touch("/touched-new.txt", 0), "Touch missing")
		got, err := ftpFS.ReadAll(ctx, "/touched-new.txt")
		require.NoError(t, err, "touched file must exist")
		assert.Empty(t, got, "newly touched file is empty")
	})

	t.Run("OpenReadWriter reads to EOF then seeks and modifies", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		require.NoError(t, ftpFS.WriteAll(ctx, "/rw.txt", []byte("Initial content"), 0))

		rw, err := ftpFS.OpenReadWriter("/rw.txt", 0)
		require.NoError(t, err, "OpenReadWriter")

		// The bug: Read never advanced the offset, so io.ReadAll would loop
		// forever and never reach EOF. This must terminate with the content.
		got, err := io.ReadAll(rw)
		require.NoError(t, err, "io.ReadAll")
		assert.Equal(t, "Initial content", string(got))

		// Seek back to the start and overwrite the prefix.
		pos, err := rw.Seek(0, io.SeekStart)
		require.NoError(t, err, "Seek")
		assert.Equal(t, int64(0), pos)

		n, err := rw.Write([]byte("Updated"))
		require.NoError(t, err, "Write")
		assert.Equal(t, len("Updated"), n)
		require.NoError(t, rw.Close(), "Close")

		// "Updated" (7 bytes) overwrites "Initial", leaving " content".
		assert.Equal(t, "Updated content", string(srv.fileContent("/rw.txt")))
	})

	t.Run("Append accumulates", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		require.NoError(t, ftpFS.Append(ctx, "/app.txt", []byte("line1\n"), 0), "first Append")
		require.NoError(t, ftpFS.Append(ctx, "/app.txt", []byte("line2\n"), 0), "second Append")
		assert.Equal(t, "line1\nline2\n", string(srv.fileContent("/app.txt")))
	})

	t.Run("OpenAppendWriter appends to existing file", func(t *testing.T) {
		srv := newTestFTPServer(t)
		ftpFS := dialInProcess(t, srv)

		require.NoError(t, ftpFS.WriteAll(ctx, "/appw.txt", []byte("head"), 0))

		w, err := ftpFS.OpenAppendWriter("/appw.txt", 0)
		require.NoError(t, err, "OpenAppendWriter")
		_, err = w.Write([]byte("-tail1"))
		require.NoError(t, err, "Write")
		_, err = w.Write([]byte("-tail2"))
		require.NoError(t, err, "Write")
		require.NoError(t, w.Close(), "Close")

		assert.Equal(t, "head-tail1-tail2", string(srv.fileContent("/appw.txt")))
	})
}

// Test_fileSystem_InProcessConformance runs the conformance suite against
// the in-process server over plain FTP, over explicit TLS (FTPS with a
// self-signed certificate) with a server that requires the data connections
// to resume the TLS session of the control connection like vsftpd and
// FileZilla Server, and against a server without MLST support, so all
// paths are verified on every platform without Docker.
func Test_fileSystem_InProcessConformance(t *testing.T) {
	for _, variant := range []struct {
		name, scheme string
		configure    func(s *testFTPServer)
	}{
		{name: "FTP", scheme: "ftp"},
		{name: "FTPS", scheme: "ftps", configure: func(s *testFTPServer) { s.requireTLSReuse = true }},
		{name: "FTP without MLST", scheme: "ftp", configure: func(s *testFTPServer) { s.noMLST = true }},
		{name: "FTP with dot entries", scheme: "ftp", configure: func(s *testFTPServer) { s.dotEntries = true }},
		{name: "FTP without MLST with dot entries", scheme: "ftp", configure: func(s *testFTPServer) {
			s.noMLST = true
			s.dotEntries = true
		}},
	} {
		t.Run(variant.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			if variant.configure != nil {
				srv.configure(variant.configure)
			}
			srv.mkdirAll("/home/testuser")
			address := fmt.Sprintf("%s://%s@%s", variant.scheme, testUser, srv.addr())
			ftpFS, err := Dial(t.Context(), address, &Options{Password: testPassword, InsecureSkipVerify: true})
			require.NoError(t, err, "Dial in-process %s server", variant.scheme)
			fstest.RunConformance(t, ftpFS, fstest.Config{
				Name:    strings.ToUpper(variant.scheme),
				Prefix:  address,
				TestDir: "/home/testuser/testdata",
			})
		})
	}
}

// Test_fileSystem_Identity checks the prefix, ID, Name and String, which
// never contain the password, and that the default port is omitted
// from the prefix and accepted as an alias.
func Test_fileSystem_Identity(t *testing.T) {
	srv := newTestFTPServer(t)
	_, port, err := net.SplitHostPort(srv.addr())
	require.NoError(t, err)
	ftpFS, err := Dial(t.Context(), fmt.Sprintf("ftp://testuser:S3cret@127.0.0.1:%s", port), nil)
	require.NoError(t, err)
	defer ftpFS.Close()

	prefix := "ftp://testuser@127.0.0.1:" + port
	assert.Equal(t, prefix, ftpFS.Prefix())
	assert.Equal(t, prefix, ftpFS.ID())
	assert.Equal(t, "FTP", ftpFS.Name())
	assert.Equal(t, "FTP "+prefix, ftpFS.String())
	assert.Equal(t, fs.File(prefix+"/"), ftpFS.RootDir())
	assert.Equal(t, "/a/b", ftpFS.CleanPath("a", "skip", "..", "/", "b", "/"))
	assert.Equal(t, fs.File(prefix+"/a/b"), ftpFS.(*fileSystem).JoinCleanFile("a", "skip", "..", "/", "b", "/"))
	assert.Empty(t, ftpFS.(*fileSystem).PrefixAliases(), "no alias for a non-default port")
	_, pass := srv.credentials()
	assert.Equal(t, "S3cret", pass, "the password of the address is used for the login")

	// A FileInfo and an error carry URIs without the password
	_, err = ftpFS.Stat("/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.NotContains(t, err.Error(), "S3cret")

	tlsFS := newFileSystem(&dialConfig{prefix: "ftps://user@example.com", port: defaultPort, secure: true})
	assert.Equal(t, "FTPS", tlsFS.Name())
	assert.Equal(t, "FTPS ftps://user@example.com", tlsFS.String())
	assert.Equal(t, []string{"ftps://user@example.com:21"}, tlsFS.PrefixAliases())
}

// TestClosedFileSystem verifies that a closed FTP file system returns
// fs.ErrFileSystemClosed from every operation instead of dialing a new
// connection, and that Close is idempotent and unregisters.
func TestClosedFileSystem(t *testing.T) {
	srv := newTestFTPServer(t)
	f := dialInProcess(t, srv)
	require.NoError(t, fs.Register(f))
	require.NoError(t, f.Close())
	require.NoError(t, f.Close(), "Close on an already closed file system is a no-op")
	assert.False(t, fs.IsRegistered(f), "Close must unregister")
	logins := srv.logins.Load()
	ctx := t.Context()

	_, err := f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	_, err = f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(ctx, "/a", "/b"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Remove("/file"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil }), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.ListDirRecursive(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil }), fs.ErrFileSystemClosed)
	_, err = f.ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.WriteAll(ctx, "/file", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Append(ctx, "/file", []byte("x"), 0), fs.ErrFileSystemClosed)
	_, err = f.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenAppendWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Touch("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
	assert.Equal(t, logins, srv.logins.Load(), "a closed file system must not dial")
}

// TestEnsureRegistered_RefCount verifies that EnsureRegistered reference-counts
// the file system: a second EnsureRegistered for the same address reuses the
// single registered connection, releasing one reference keeps the connection
// open, and only releasing the last reference closes and unregisters it.
func TestEnsureRegistered_RefCount(t *testing.T) {
	srv := newTestFTPServer(t)
	address := fmt.Sprintf("ftp://%s:%s@%s", testUser, testPassword, srv.addr())
	prefix := testAddress(srv)

	free1, err := EnsureRegistered(t.Context(), address, nil)
	require.NoError(t, err, "first EnsureRegistered")
	free2, err := EnsureRegistered(t.Context(), address, nil)
	require.NoError(t, err, "second EnsureRegistered")
	assert.Equal(t, int64(1), srv.logins.Load(), "the second call must reuse the connection")

	registered, ok := fs.GetFileSystemByPrefixOrNil(prefix).(*fileSystem)
	require.True(t, ok, "ftp fileSystem registered under prefix %q", prefix)

	// A URI without the password resolves to the registered file system
	data := []byte("shared")
	require.NoError(t, fs.File(prefix+"/shared.txt").WriteAll(t.Context(), data))
	assert.Equal(t, data, srv.fileContent("/shared.txt"))

	require.NoError(t, free1())
	require.NoError(t, free1(), "free is idempotent")
	assert.False(t, registered.closed.Load(), "connection must stay open while a reference remains")
	assert.True(t, fs.IsRegistered(registered), "still registered after releasing one of two references")

	require.NoError(t, free2())
	assert.True(t, registered.closed.Load(), "connection closed after the last reference is released")
	assert.False(t, fs.IsRegistered(registered), "unregistered after the last reference is released")

	free, err := EnsureRegistered(t.Context(), "sftp://host", nil)
	require.Error(t, err)
	require.NotNil(t, free, "free is never nil")
	require.NoError(t, free())
}

// TestEnsureRegistered_Concurrent verifies that concurrent EnsureRegistered
// calls share one connection and that nobody gets a closed file system
// while holding a reference.
func TestEnsureRegistered_Concurrent(t *testing.T) {
	srv := newTestFTPServer(t)
	address := testAddress(srv)
	opts := &Options{Password: testPassword}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				free, err := EnsureRegistered(t.Context(), address, opts)
				if !assert.NoError(t, err) {
					return
				}
				_, err = fs.File(address + "/").Stat()
				assert.NoError(t, err, "Stat while holding a reference")
				assert.NoError(t, free())
			}
		})
	}
	wg.Wait()
	assert.Nil(t, fs.GetFileSystemByPrefixOrNil(address), "unregistered after all frees")
}

// TestDialAndRegister checks that DialAndRegister registers the file system
// and that a second registration of the same prefix fails and closes its
// own connection instead of the registered one.
func TestDialAndRegister(t *testing.T) {
	srv := newTestFTPServer(t)
	address := testAddress(srv)
	opts := &Options{Password: testPassword}
	require.NoError(t, DialAndRegister(t.Context(), address, opts))
	registered := fs.GetFileSystemByPrefixOrNil(address)
	require.NotNil(t, registered)
	defer registered.Close()

	err := DialAndRegister(t.Context(), address, opts)
	require.ErrorIs(t, err, fs.ErrAlreadyRegistered)
	_, err = registered.Stat("/")
	assert.NoError(t, err, "the registered file system must still work")

	err = DialAndRegister(t.Context(), "ftp://u:S3cret@bad host", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "S3cret")
}

// TestCommandInjection verifies that a path or credential with CR, LF
// or NUL is rejected before it is sent. Stat("/nope\r\nDELE /victim.txt")
// used to delete /victim.txt.
func TestCommandInjection(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	ctx := t.Context()
	srv.writeFile("/victim.txt", "keep me")

	for _, name := range []string{"/nope\r\nDELE /victim.txt", "/nope\nDELE /victim.txt", "/nope\rDELE /victim.txt", "/a\x00b"} {
		_, err := ftpFS.Stat(name)
		assert.ErrorIs(t, err, os.ErrInvalid, "Stat(%q)", name)
		assert.ErrorIs(t, ftpFS.Remove(name), os.ErrInvalid, "Remove(%q)", name)
		assert.ErrorIs(t, ftpFS.WriteAll(ctx, name, []byte("x"), 0), os.ErrInvalid, "WriteAll(%q)", name)
		assert.ErrorIs(t, ftpFS.Move(ctx, "/victim.txt", name), os.ErrInvalid, "Move to %q", name)
		_, err = ftpFS.OpenReader(name)
		assert.ErrorIs(t, err, os.ErrInvalid, "OpenReader(%q)", name)
		_, err = ftpFS.OpenWriter(name, 0)
		assert.ErrorIs(t, err, os.ErrInvalid, "OpenWriter(%q)", name)
	}
	_, err := ftpFS.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, "keep me", string(srv.fileContent("/victim.txt")), "an injected DELE must not run")
	assert.Zero(t, srv.count("DELE"))
}

// TestListDirRecursive_ListingError verifies that a directory that can't
// be listed is an error. It used to be skipped silently because the walker
// stops on a listing error and its error was only checked inside the loop.
func TestListDirRecursive_ListingError(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/root/ok.txt", "x")
	srv.writeFile("/root/sub/deeper/a.txt", "x")
	srv.writeFile("/root/sub/b.md", "x")

	var got []string
	err := ftpFS.ListDirRecursive(t.Context(), "/root", []string{"*.txt"}, func(info *fs.FileInfo) error {
		got = append(got, strings.TrimPrefix(string(info.File), ftpFS.Prefix()))
		return nil
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"/root/ok.txt", "/root/sub/deeper/a.txt"}, got)

	srv.configure(func(s *testFTPServer) { s.failListContains = "deeper" })
	got = nil
	err = ftpFS.ListDirRecursive(t.Context(), "/root", nil, func(info *fs.FileInfo) error {
		got = append(got, string(info.File))
		return nil
	})
	require.Error(t, err, "listing /root/sub/deeper failed")
	assert.Empty(t, got, "no partial result")

	err = ftpFS.ListDirRecursive(t.Context(), "/root/ok.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{})
	err = ftpFS.ListDirRecursive(t.Context(), "/missing", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestDotEntries verifies that the entries "." and ".." that most servers
// list for every directory are skipped. RemoveAll would recurse into the
// directory itself forever and into its parent, deleting files outside of
// the removed directory, and ListDirRecursive would never end.
func TestDotEntries(t *testing.T) {
	for _, variant := range []struct {
		name   string
		noMLST bool
	}{
		{"MLSD", false},
		{"LIST", true},
	} {
		t.Run(variant.name, func(t *testing.T) {
			ctx := t.Context()
			srv := newTestFTPServer(t)
			srv.configure(func(s *testFTPServer) {
				s.noMLST = variant.noMLST
				s.dotEntries = true
				// A walk into "." or ".." would recurse forever,
				// the test needs less than 20 listings
				s.maxListings = 100
			})
			ftpFS := dialInProcess(t, srv)
			srv.writeFile("/root/sibling.txt", "keep")
			srv.writeFile("/root/dir/a.txt", "a")
			srv.writeFile("/root/dir/sub/b.txt", "b")

			var names []string
			err := ftpFS.ListDir(ctx, "/root/dir", nil, func(info *fs.FileInfo) error {
				names = append(names, info.Name)
				return nil
			})
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"a.txt", "sub"}, names)

			var files []string
			err = ftpFS.ListDirRecursive(ctx, "/root/dir", nil, func(info *fs.FileInfo) error {
				files = append(files, strings.TrimPrefix(string(info.File), ftpFS.Prefix()))
				return nil
			})
			require.NoError(t, err)
			assert.ElementsMatch(t, []string{"/root/dir/a.txt", "/root/dir/sub/b.txt"}, files)

			// A refused RMD of a directory with only dot entries
			// is not reported as a directory with entries
			srv.mkdirAll("/root/empty")
			srv.configure(func(s *testFTPServer) { s.refuse["RMD /root/empty"] = 1 })
			err = ftpFS.Remove("/root/empty")
			require.Error(t, err)
			assert.NotErrorIs(t, err, fs.ErrDirectoryNotEmpty{})
			require.NoError(t, ftpFS.Remove("/root/empty"))
			assert.ErrorIs(t, ftpFS.Remove("/root/dir"), fs.ErrDirectoryNotEmpty{})

			require.NoError(t, ftpFS.RemoveAll(ctx, "/root/dir"))
			assert.Equal(t, []string{"/root", "/root/sibling.txt"}, srv.paths(), "only the directory is removed")
		})
	}
}

// TestStat_MissingFileWithMLST verifies that Stat of a missing file on a
// server with MLST support doesn't list the parent directory.
func TestStat_MissingFileWithMLST(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/dir/file.txt", "x")

	_, err := ftpFS.Stat("/dir/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Zero(t, srv.count("MLSD"), "Stat must not list the parent directory")
	assert.Zero(t, srv.count("LIST"), "Stat must not list the parent directory")

	// Without MLST the parent is listed
	srv.configure(func(s *testFTPServer) { s.noMLST = true })
	noMLST := dialInProcess(t, srv)
	info, err := noMLST.Stat("/dir/file.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.Size)
	_, err = noMLST.Stat("/dir/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = noMLST.Stat("/missing-dir/missing.txt")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestMove_ServerWithoutReplace verifies that Move replaces an existing
// file on servers that refuse to rename onto an existing file.
func TestMove_ServerWithoutReplace(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.configure(func(s *testFTPServer) { s.rntoNoReplace = true })
	srv.writeFile("/src.txt", "source")
	srv.writeFile("/dest.txt", "destination")

	require.NoError(t, ftpFS.Move(t.Context(), "/src.txt", "/dest.txt"))
	assert.Equal(t, "source", string(srv.fileContent("/dest.txt")))
	assert.False(t, srv.exists("/src.txt"))
	assert.Equal(t, []string{"/dest.txt"}, srv.paths(), "no temporary file is left")

	err := ftpFS.Move(t.Context(), "/missing.txt", "/other.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, []string{"/dest.txt"}, srv.paths(), "no temporary file is left")
}

// TestMove_RefusedRenameKeepsDestination is the regression test for Move
// onto an existing file, which deleted the destination after any failure
// reply of the rename. The reply doesn't tell whether the server refused
// RNFR (the source) or RNTO (the destination), so a refused RNFR deleted
// the destination and left the source where it was.
func TestMove_RefusedRenameKeepsDestination(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/src.txt", "source")
	srv.writeFile("/dest.txt", "destination")

	// The server refuses to rename the source, like for missing permissions
	srv.configure(func(s *testFTPServer) { s.refuse["RNFR /src.txt"] = 100 })
	require.Error(t, ftpFS.Move(ctx, "/src.txt", "/dest.txt"))
	assert.Equal(t, "destination", string(srv.fileContent("/dest.txt")), "the destination must be kept")
	assert.Equal(t, "source", string(srv.fileContent("/src.txt")), "the source must be kept")
	assert.ElementsMatch(t, []string{"/src.txt", "/dest.txt"}, srv.paths(), "no temporary file is left")

	// The rename onto the destination fails twice: the server refuses
	// to replace it and then refuses the source in its place, so the
	// destination is renamed back from its temporary name
	srv.configure(func(s *testFTPServer) { s.refuse = map[string]int{"RNTO /dest.txt": 2} })
	require.Error(t, ftpFS.Move(ctx, "/src.txt", "/dest.txt"))
	assert.Equal(t, "destination", string(srv.fileContent("/dest.txt")), "the destination must be restored")
	assert.Equal(t, "source", string(srv.fileContent("/src.txt")), "the source must be kept")
	assert.ElementsMatch(t, []string{"/src.txt", "/dest.txt"}, srv.paths(), "no temporary file is left")

	// If the destination can't be renamed back either,
	// the error tells where it is
	srv.configure(func(s *testFTPServer) { s.refuse = map[string]int{"RNTO /dest.txt": 3} })
	err := ftpFS.Move(ctx, "/src.txt", "/dest.txt")
	require.Error(t, err)
	assert.False(t, srv.exists("/dest.txt"))
	var tmp string
	for _, p := range srv.paths() {
		if p != "/src.txt" {
			tmp = p
		}
	}
	require.NotEmpty(t, tmp, "the replaced file is kept under a temporary name")
	assert.Equal(t, "destination", string(srv.fileContent(tmp)))
	assert.Contains(t, err.Error(), tmp, "the error names the temporary file")

	// The source vanished after it was checked: nothing is renamed
	srv.writeFile("/dest.txt", "destination")
	require.NoError(t, ftpFS.RemoveAll(ctx, tmp))
	srv.configure(func(s *testFTPServer) {
		s.refuse = map[string]int{}
		s.deleteOnRNFR = "/src.txt"
	})
	err = ftpFS.Move(ctx, "/src.txt", "/dest.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, "destination", string(srv.fileContent("/dest.txt")), "the destination must be kept")
	assert.Equal(t, []string{"/dest.txt"}, srv.paths(), "no temporary file is left")
}

// TestRemove_Root verifies that the root directory is never removed.
func TestRemove_Root(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/keep.txt", "x")

	assert.Error(t, ftpFS.Remove("/"))
	assert.Error(t, ftpFS.RemoveAll(t.Context(), "/"))
	assert.True(t, srv.exists("/keep.txt"))
}

// TestIPv6 verifies that an IPv6 address with and without a port works.
func TestIPv6(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	ln.Close()
	srv := newTestFTPServerOn(t, "[::1]:0")
	_, port, err := net.SplitHostPort(srv.addr())
	require.NoError(t, err)
	address := fmt.Sprintf("ftp://%s@[::1]:%s", testUser, port)
	ftpFS, err := Dial(t.Context(), address, &Options{Password: testPassword})
	require.NoError(t, err)
	defer ftpFS.Close()
	assert.Equal(t, address, ftpFS.Prefix())

	data := []byte("over IPv6")
	require.NoError(t, ftpFS.(*fileSystem).WriteAll(t.Context(), "/v6.txt", data, 0))
	got, err := ftpFS.(*fileSystem).ReadAll(t.Context(), "/v6.txt")
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestDialAndRegisterWithPublicOnlineServers(t *testing.T) {
	if os.Getenv("GOFS_ONLINE_TESTS") == "" {
		t.Skip("set GOFS_ONLINE_TESTS=1 to run tests against public internet FTP servers")
	}
	// https://www.sftp.net/public-online-sftp-servers
	for _, prefix := range []string{"ftp://demo@test.rebex.net", "ftps://demo@test.rebex.net"} {
		t.Run(prefix, func(t *testing.T) {
			err := DialAndRegister(t.Context(), prefix, &Options{Password: "password"})
			if err != nil && strings.HasPrefix(prefix, PrefixTLS) && errors.Is(err, io.EOF) {
				t.Skipf("FTPS server not reachable: %v", err)
			}
			require.NoError(t, err, "DialAndRegister")
			ftpFS := fs.GetFileSystemByPrefixOrNil(prefix)
			require.NotNil(t, ftpFS)
			defer ftpFS.Close()

			f := fs.File(prefix + "/readme.txt")
			assert.Equal(t, "readme.txt", f.Name())
			data, err := f.ReadAll(t.Context())
			require.NoError(t, err, "ReadAll")
			assert.NotEmpty(t, data, "read more than zero bytes from readme.txt")
		})
	}
}

// TestRemoveRoot verifies that the root directory is never removed
// and that the error wraps os.ErrInvalid like in the other backends.
func TestRemoveRoot(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/file.txt", "content")
	assert.ErrorIs(t, ftpFS.Remove("/"), os.ErrInvalid, "Remove of the root")
	assert.ErrorIs(t, ftpFS.RemoveAll(t.Context(), "/"), os.ErrInvalid, "RemoveAll of the root")
	_, err := ftpFS.Stat("/file.txt")
	assert.NoError(t, err, "nothing must be removed")
}
