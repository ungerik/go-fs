package ftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/textproto"
	"os"
	"path"
	"slices"
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

// TestMove_MissingSourceWithoutMLST checks Move of a missing source on a
// server without MLST support: it fails with an error wrapping
// os.ErrNotExist before it sends RNFR, which only servers with MLST support
// get for a source that MLST reports as missing, because it can be a
// dangling symbolic link there.
func TestMove_MissingSourceWithoutMLST(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.noMLST = true })
	srv.writeFile("/dest.txt", "destination")
	ftpFS := dialInProcess(t, srv)

	assert.ErrorIs(t, ftpFS.Move(ctx, "/missing.txt", "/moved.txt"), os.ErrNotExist)
	assert.ErrorIs(t, ftpFS.Move(ctx, "/missing.txt", "/dest.txt"), os.ErrNotExist)
	assert.Zero(t, srv.count("RNFR"), "no rename of a missing source")
	assert.Equal(t, []string{"/dest.txt"}, srv.paths())
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
	// Relative paths of the root reached the server as its login directory
	for _, root := range []string{"/", ".", "./", "dir/.."} {
		assert.ErrorIs(t, ftpFS.Remove(root), os.ErrInvalid, "Remove(%q)", root)
		assert.ErrorIs(t, ftpFS.RemoveAll(t.Context(), root), os.ErrInvalid, "RemoveAll(%q)", root)
	}
	assert.ErrorIs(t, ftpFS.Remove(""), fs.ErrEmptyPath, `Remove("")`)
	assert.ErrorIs(t, ftpFS.RemoveAll(t.Context(), ""), fs.ErrEmptyPath, `RemoveAll("")`)
	_, err := ftpFS.Stat("/file.txt")
	assert.NoError(t, err, "nothing must be removed")
}

// seedLinks seeds the test server with files, a directory and symbolic
// links below /root: links to a file with a relative and an absolute
// target, to a link, with a ".." in the target, to a directory,
// a dangling link and a cycle of two links.
func seedLinks(srv *testFTPServer) {
	srv.writeFile("/root/file.txt", "data")
	srv.writeFile("/root/dir/file.txt", "content")
	srv.symlink("file.txt", "/root/filelink")
	srv.symlink("/root/file.txt", "/root/abslink")
	srv.symlink("filelink", "/root/chain")
	srv.symlink("../file.txt", "/root/dir/uplink")
	srv.symlink("dir", "/root/dirlink")
	srv.symlink("missing.txt", "/root/dangling")
	srv.symlink("loop2", "/root/loop1")
	srv.symlink("loop1", "/root/loop2")
}

// relPaths returns the paths of the files of infos without the prefix of the file system.
func relPaths(f *fileSystem, infos []*fs.FileInfo) []string {
	paths := make([]string, len(infos))
	for i, info := range infos {
		paths[i] = strings.TrimPrefix(string(info.File), f.Prefix())
	}
	return paths
}

// TestSymbolicLinks checks the symbolic links that a server without MLST
// support lists with LIST like "ls -l": Stat follows a link and describes
// its target with IsSymlink set, ListDir and ListDirRecursive describe the
// link itself with neither IsDir nor IsRegular and never follow it, and
// Remove, RemoveAll and Move act on the link itself. ListDir used to report
// a link with IsRegular set and Stat didn't follow links, so a link to
// a directory looked like a file.
func TestSymbolicLinks(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.noMLST = true })
	seedLinks(srv)
	ftpFS := dialInProcess(t, srv)
	require.NoError(t, fs.Register(ftpFS))
	file := ftpFS.JoinCleanFile

	t.Run("Stat follows links", func(t *testing.T) {
		for _, link := range []string{"/root/filelink", "/root/abslink", "/root/chain", "/root/dir/uplink", "/root/dirlink/uplink"} {
			info, err := ftpFS.Stat(link)
			require.NoError(t, err, "Stat(%q)", link)
			assert.Equal(t, file(link), info.File, "Stat(%q).File is the link", link)
			assert.Equal(t, path.Base(link), info.Name, "Stat(%q).Name is the link", link)
			assert.True(t, info.IsSymlink, "Stat(%q).IsSymlink", link)
			assert.True(t, info.IsRegular, "Stat(%q).IsRegular of a link to a file", link)
			assert.False(t, info.IsDir, "Stat(%q).IsDir of a link to a file", link)
			assert.Equal(t, int64(len("data")), info.Size, "Stat(%q).Size is the size of the target", link)
		}
		info, err := ftpFS.Stat("/root/dirlink")
		require.NoError(t, err)
		assert.True(t, info.IsSymlink, "Stat of a link to a directory has IsSymlink set")
		assert.True(t, info.IsDir, "Stat of a link to a directory describes the directory")
		assert.False(t, info.IsRegular)
		assert.Equal(t, DefaultDirPermissions, info.Permissions)

		info, err = ftpFS.Stat("/root/file.txt")
		require.NoError(t, err)
		assert.False(t, info.IsSymlink, "Stat of a file")

		_, err = ftpFS.Stat("/root/dangling")
		require.ErrorIs(t, err, os.ErrNotExist, "Stat of a dangling link")
		assert.ErrorContains(t, err, string(file("/root/dangling")), "the error names the link")
		_, err = ftpFS.Stat("/root/loop1")
		require.ErrorContains(t, err, "too many levels of symbolic links")
		assert.NotErrorIs(t, err, os.ErrNotExist)

		// Without fs.SymbolicLinkFileSystem File.Lstat returns the Stat
		assert.True(t, file("/root/filelink").IsSymbolicLink())
		assert.False(t, file("/root/file.txt").IsSymbolicLink())
		assert.True(t, file("/root/dirlink").IsDir())
		assert.True(t, file("/root/chain").IsRegular())
	})

	t.Run("ListDir describes links", func(t *testing.T) {
		infos := map[string]*fs.FileInfo{}
		err := ftpFS.ListDir(ctx, "/root", nil, func(info *fs.FileInfo) error {
			infos[info.Name] = info
			return nil
		})
		require.NoError(t, err)
		links := []string{"abslink", "chain", "dangling", "dirlink", "filelink", "loop1", "loop2"}
		assert.ElementsMatch(t, append([]string{"dir", "file.txt"}, links...), slices.Collect(maps.Keys(infos)))
		for _, name := range links {
			info := infos[name]
			if !assert.NotNil(t, info, "ListDir must list %s", name) {
				continue
			}
			assert.Equal(t, file("/root", name), info.File)
			assert.True(t, info.IsSymlink, "%s.IsSymlink", name)
			assert.False(t, info.IsDir, "%s.IsDir: the listing doesn't tell what a link points to", name)
			assert.False(t, info.IsRegular, "%s.IsRegular: the listing doesn't tell what a link points to", name)
		}
		assert.True(t, infos["file.txt"].IsRegular)
		assert.False(t, infos["file.txt"].IsSymlink)
		assert.True(t, infos["dir"].IsDir)
		assert.False(t, infos["dir"].IsSymlink)

		// The server lists the directory that a link at the listed path points to
		var listed []*fs.FileInfo
		err = ftpFS.ListDir(ctx, "/root/dirlink", nil, func(info *fs.FileInfo) error {
			listed = append(listed, info)
			return nil
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"/root/dirlink/file.txt", "/root/dirlink/uplink"}, relPaths(ftpFS, listed))

		err = ftpFS.ListDir(ctx, "/root/filelink", nil, func(*fs.FileInfo) error { return nil })
		assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "ListDir of a link to a file")
		err = ftpFS.ListDir(ctx, "/root/dangling", nil, func(*fs.FileInfo) error { return nil })
		assert.ErrorIs(t, err, os.ErrNotExist, "ListDir of a dangling link")
	})

	t.Run("ListDirRecursive doesn't follow links", func(t *testing.T) {
		var listed []*fs.FileInfo
		err := ftpFS.ListDirRecursive(ctx, "/root", nil, func(info *fs.FileInfo) error {
			assert.False(t, info.IsDir, "ListDirRecursive must not pass a directory, got %s", info.File)
			listed = append(listed, info)
			return nil
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			"/root/abslink", "/root/chain", "/root/dangling", "/root/dir/file.txt", "/root/dir/uplink",
			"/root/dirlink", "/root/file.txt", "/root/filelink", "/root/loop1", "/root/loop2",
		}, relPaths(ftpFS, listed), "the files of the directory of a link are not listed below the link")

		// A link at the listed path is followed
		listed = nil
		err = ftpFS.ListDirRecursive(ctx, "/root/dirlink", nil, func(info *fs.FileInfo) error {
			listed = append(listed, info)
			return nil
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"/root/dirlink/file.txt", "/root/dirlink/uplink"}, relPaths(ftpFS, listed))
		err = ftpFS.ListDirRecursive(ctx, "/root/filelink", nil, func(*fs.FileInfo) error { return nil })
		assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "ListDirRecursive of a link to a file")
	})

	t.Run("ReadAll follows links", func(t *testing.T) {
		data, err := ftpFS.ReadAll(ctx, "/root/chain")
		require.NoError(t, err)
		assert.Equal(t, "data", string(data))
		_, err = ftpFS.ReadAll(ctx, "/root/dirlink")
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "ReadAll of a link to a directory")
		_, err = ftpFS.ReadAll(ctx, "/root/dangling")
		assert.ErrorIs(t, err, os.ErrNotExist, "ReadAll of a dangling link")
	})
}

// TestSymbolicLinks_RemoveAndMove checks that Remove, RemoveAll and Move
// act on a symbolic link itself and never on the files it points to,
// also now that Stat follows links.
func TestSymbolicLinks_RemoveAndMove(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.noMLST = true })
	seedLinks(srv)
	ftpFS := dialInProcess(t, srv)

	require.NoError(t, ftpFS.RemoveAll(ctx, "/root/dirlink"))
	assert.False(t, srv.exists("/root/dirlink"), "RemoveAll removes the link")
	assert.Equal(t, "content", string(srv.fileContent("/root/dir/file.txt")), "RemoveAll must keep the files that a link points to")

	srv.symlink("dir", "/root/dirlink")
	require.NoError(t, ftpFS.Remove("/root/dirlink"), "Remove of a link to a non-empty directory")
	assert.False(t, srv.exists("/root/dirlink"), "Remove removes the link")
	assert.True(t, srv.exists("/root/dir/file.txt"))

	require.NoError(t, ftpFS.Remove("/root/dangling"), "Remove of a dangling link")
	assert.False(t, srv.exists("/root/dangling"))

	require.NoError(t, ftpFS.Move(ctx, "/root/abslink", "/root/dir/moved"))
	assert.False(t, srv.exists("/root/abslink"))
	info, err := ftpFS.Stat("/root/dir/moved")
	require.NoError(t, err)
	assert.True(t, info.IsSymlink, "Move renames the link itself")
	assert.Equal(t, "data", string(srv.fileContent("/root/file.txt")))

	// A directory with links: the links are removed, not their targets
	srv.symlink("/root/file.txt", "/root/dir/sub/abs")
	require.NoError(t, ftpFS.RemoveAll(ctx, "/root/dir"))
	assert.False(t, srv.exists("/root/dir"))
	assert.Equal(t, "data", string(srv.fileContent("/root/file.txt")))

	// Move and MakeDir check a link itself, not the file it points to
	srv.writeFile("/root/new.txt", "new")
	srv.mkdirAll("/root/target")
	srv.symlink("target", "/root/dirlink")
	srv.symlink("missing.txt", "/root/dangling")
	require.NoError(t, ftpFS.Move(ctx, "/root/dangling", "/root/moved"), "Move of a dangling link")
	assert.True(t, srv.exists("/root/moved"))
	require.NoError(t, ftpFS.Move(ctx, "/root/new.txt", "/root/dirlink"), "Move onto a link to a directory replaces the link")
	assert.Equal(t, "new", string(srv.fileContent("/root/dirlink")))
	assert.True(t, srv.exists("/root/target"), "the directory that the replaced link pointed to is kept")
	assert.ErrorIs(t, ftpFS.MakeDir("/root/moved", 0), fs.ErrAlreadyExists{}, "MakeDir of a dangling link")
}

// TestSymbolicLinks_ListedItself checks a server whose LIST lists a file
// or a symbolic link itself like "ls -l", instead of the directory that
// a link points to: ListDir and ListDirRecursive of a link to a directory
// list the directory, and of a file or a link to a file are an
// fs.ErrIsNotDirectory. They used to list a link to a directory as
// its own entry, like /root/dirlink/dirlink.
func TestSymbolicLinks_ListedItself(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) {
		s.noMLST = true
		s.lsArgs = true
	})
	seedLinks(srv)
	ftpFS := dialInProcess(t, srv)
	noCallback := func(*fs.FileInfo) error { return nil }

	for name, list := range map[string]func(context.Context, string, []string, func(*fs.FileInfo) error) error{
		"ListDir":          ftpFS.ListDir,
		"ListDirRecursive": ftpFS.ListDirRecursive,
	} {
		var listed []*fs.FileInfo
		err := list(ctx, "/root/dirlink", nil, func(info *fs.FileInfo) error {
			listed = append(listed, info)
			return nil
		})
		require.NoError(t, err, "%s of a link to a directory", name)
		assert.ElementsMatch(t, []string{"/root/dirlink/file.txt", "/root/dirlink/uplink"}, relPaths(ftpFS, listed), "%s of a link to a directory", name)
		assert.ErrorIs(t, list(ctx, "/root/filelink", nil, noCallback), fs.ErrIsNotDirectory{}, "%s of a link to a file", name)
		assert.ErrorIs(t, list(ctx, "/root/file.txt", nil, noCallback), fs.ErrIsNotDirectory{}, "%s of a file", name)
		assert.ErrorIs(t, list(ctx, "/root/dangling", nil, noCallback), os.ErrNotExist, "%s of a dangling link", name)
	}
}

// TestSymbolicLinks_ListedTarget is a regression test for a server whose
// LIST follows a link to a file and lists the file under the name of the
// link, like "ls -lL": ListDir of a link to a file listed that line as an
// entry below the link, like /root/filelink/filelink, instead of
// returning an fs.ErrIsNotDirectory.
func TestSymbolicLinks_ListedTarget(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) {
		s.noMLST = true
		s.statArgs = true
	})
	seedLinks(srv)
	ftpFS := dialInProcess(t, srv)
	noCallback := func(*fs.FileInfo) error { return nil }

	for name, list := range map[string]func(context.Context, string, []string, func(*fs.FileInfo) error) error{
		"ListDir":          ftpFS.ListDir,
		"ListDirRecursive": ftpFS.ListDirRecursive,
	} {
		for _, p := range []string{"/root/filelink", "/root/abslink", "/root/chain", "/root/file.txt"} {
			assert.ErrorIs(t, list(ctx, p, nil, noCallback), fs.ErrIsNotDirectory{}, "%s(%s)", name, p)
		}
		var listed []*fs.FileInfo
		err := list(ctx, "/root/dirlink", nil, func(info *fs.FileInfo) error {
			listed = append(listed, info)
			return nil
		})
		require.NoError(t, err, "%s of a link to a directory", name)
		assert.ElementsMatch(t, []string{"/root/dirlink/file.txt", "/root/dirlink/uplink"}, relPaths(ftpFS, listed), "%s of a link to a directory", name)
	}
}

// TestSymbolicLinks_MLSDRemoveAll checks that RemoveAll on a server with
// MLST support, which describes a link to a directory like the directory,
// deletes a link itself, at the removed path and below it, and keeps the
// files that the link points to. The test server resolves the links in
// the path of DELE like the kernel. RemoveAll used to delete the files
// outside of the removed tree through the link.
func TestSymbolicLinks_MLSDRemoveAll(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.writeFile("/outside/keep.txt", "keep")
	srv.writeFile("/tree/file.txt", "data")
	srv.writeFile("/tree/sub/file.txt", "data")
	srv.symlink("/outside", "/tree/link")
	srv.symlink("../../outside", "/tree/sub/uplink")
	srv.symlink("outside", "/toplink")
	ftpFS := dialInProcess(t, srv)

	info, err := ftpFS.Stat("/tree/link")
	require.NoError(t, err)
	require.True(t, info.IsDir, "the server describes the link like the directory")

	require.NoError(t, ftpFS.RemoveAll(ctx, "/toplink"))
	assert.False(t, srv.exists("/toplink"), "RemoveAll removes the link at the path")
	require.NoError(t, ftpFS.RemoveAll(ctx, "/tree"))
	assert.Equal(t, []string{"/outside", "/outside/keep.txt"}, srv.paths(), "RemoveAll must keep the files that a link points to")
	assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")))
	assert.Positive(t, srv.count("MLSD"))
}

// TestSymbolicLinks_MLSD checks a server with MLST support that describes
// a symbolic link with the facts of its target like ProFTPD by default:
// ftpfs can't tell its links from files and directories.
func TestSymbolicLinks_MLSD(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.writeFile("/root/file.txt", "data")
	srv.writeFile("/root/dir/file.txt", "content")
	srv.symlink("file.txt", "/root/filelink")
	srv.symlink("dir", "/root/dirlink")
	ftpFS := dialInProcess(t, srv)

	infos := map[string]*fs.FileInfo{}
	err := ftpFS.ListDir(t.Context(), "/root", nil, func(info *fs.FileInfo) error {
		infos[info.Name] = info
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"dir", "dirlink", "file.txt", "filelink"}, slices.Collect(maps.Keys(infos)))
	assert.True(t, infos["filelink"].IsRegular)
	assert.Equal(t, int64(len("data")), infos["filelink"].Size)
	assert.True(t, infos["dirlink"].IsDir)

	info, err := ftpFS.Stat("/root/dirlink")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	assert.Positive(t, srv.count("MLSD"))
	assert.Zero(t, srv.count("LIST"), "the server supports MLSD")
}

// TestSymbolicLinks_MLSDRemoveAllRefusedDelete is the regression test for
// RemoveAll on a server with MLST support, which describes a link to a
// directory like the directory, when the server refuses to delete the link,
// like in a directory without write permission: RemoveAll took the failed
// DELE for the failure of a directory and deleted the files that the link
// points to, outside of the removed tree. It must fail and keep them unless
// LIST of the parent directory lists the path once and as a directory.
// A file whose name contains a line feed followed by a line that lists a
// directory with the name of the link added that line to the listing,
// and the check accepted the link because of it. The FTP client library
// cuts the LIST line of a link at the first " -> ", so it listed a link
// named "a -> b" with the name "a", and one such added line was the only
// entry named "a -> b".
func TestSymbolicLinks_MLSDRemoveAllRefusedDelete(t *testing.T) {
	for _, tc := range []struct {
		name      string
		removed   string // the path of RemoveAll
		link      string // the link to /outside whose DELE is refused
		failLists string // LIST and MLSD of paths containing it fail
		listOmits string // LIST of its parent leaves out this path
		forged    int    // LIST of its parent adds this many lines that list the link as a directory
	}{
		{name: "link below the removed directory", removed: "/tree", link: "/tree/link"},
		{name: "link in a subdirectory", removed: "/tree", link: "/tree/sub/link"},
		{name: "link at the removed path", removed: "/tree/link", link: "/tree/link"},
		{name: "parent directory can't be listed", removed: "/tree/link", link: "/tree/link", failLists: "/tree"},
		// The check must accept only a directory that LIST shows,
		// not refuse only an entry that LIST shows as a link
		{name: "LIST doesn't show the link", removed: "/tree", link: "/tree/link", listOmits: "/tree/link"},
		{name: "forged directory line for the link", removed: "/tree", link: "/tree/link", forged: 1},
		{name: "forged directory line for the link at the removed path", removed: "/tree/link", link: "/tree/link", forged: 1},
		{name: "two directory lines for the link", removed: "/tree", link: "/tree/link", listOmits: "/tree/link", forged: 2},
		{name: "link name with an arrow", removed: "/tree", link: "/tree/a -> b"},
		{name: "forged directory line for a link name with an arrow", removed: "/tree", link: "/tree/a -> b", forged: 1},
		{name: "forged directory line for a link name with an arrow at the removed path", removed: "/tree/a -> b", link: "/tree/a -> b", forged: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/outside/keep.txt", "keep")
			srv.writeFile("/outside/sub/keep.txt", "keep")
			srv.writeFile("/tree/file.txt", "data")
			srv.writeFile("/tree/sub/file.txt", "data")
			srv.symlink("/outside", tc.link)
			srv.configure(func(s *testFTPServer) {
				s.refuse["DELE "+tc.link] = 100
				s.failListContains = tc.failLists
				s.listOmits = tc.listOmits
				s.listForged[tc.link] = tc.forged
			})
			ftpFS := dialInProcess(t, srv)

			err := ftpFS.RemoveAll(t.Context(), tc.removed)
			require.Error(t, err, "RemoveAll must fail for a link that the server doesn't delete")
			assert.ErrorContains(t, err, "permission denied", "the error of the refused DELE")
			// The LIST error is only quoted, so the reply in the chain is the DELE reply
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
				assert.Equal(t, 550, reply.Code, "the 550 reply to DELE")
			}
			assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile(tc.link)), "the error names the link")
			assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")), "RemoveAll must keep the files that the link points to")
			assert.Equal(t, "keep", string(srv.fileContent("/outside/sub/keep.txt")), "RemoveAll must keep the files that the link points to")
			assert.True(t, srv.exists(tc.link), "the link is kept")
			// The connection for LIST is closed after the operation
			require.Eventually(t, func() bool { return srv.open.Load() == 1 }, 5*time.Second, time.Millisecond,
				"%d connections are open", srv.open.Load())

			// Once the server deletes the link, the tree is removed
			srv.configure(func(s *testFTPServer) {
				s.refuse = map[string]int{}
				s.failListContains = ""
				s.listOmits = ""
				s.listForged = map[string]int{}
			})
			require.NoError(t, ftpFS.RemoveAll(t.Context(), tc.removed))
			assert.False(t, srv.exists(tc.removed))
			assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")))
			assert.Equal(t, "keep", string(srv.fileContent("/outside/sub/keep.txt")))
		})
	}
}

// TestSymbolicLinks_MLSDRemoveAllGlobParent is the regression test for the
// LIST that RemoveAll sends on a server with MLST support to tell a
// directory from a symbolic link: it listed the parent directory with
// "LIST -a <dir>", and servers like ProFTPD with UseGlobbing expand glob
// characters in that argument, so LIST of /p[q] listed /pq, whose directory
// sub confirmed the link /p[q]/sub to /outside, and RemoveAll deleted the
// files that the link points to. A directory below a parent with glob
// characters couldn't be removed either. RemoveAll must change to the
// directory with CWD and send LIST without a path argument.
func TestSymbolicLinks_MLSDRemoveAllGlobParent(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.writeFile("/outside/keep.txt", "keep")
	srv.writeFile("/p[q]/file.txt", "data")
	srv.symlink("/outside", "/p[q]/sub")
	srv.mkdirAll("/pq/sub")
	srv.configure(func(s *testFTPServer) {
		s.globArgs = true
		s.refuse["DELE /p[q]/sub"] = 100
	})
	ftpFS := dialInProcess(t, srv)

	err := ftpFS.RemoveAll(ctx, "/p[q]")
	require.Error(t, err, "RemoveAll must fail for a link that the server doesn't delete")
	assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile("/p[q]/sub")), "the error names the link")
	assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")), "RemoveAll must keep the files that the link points to")
	assert.True(t, srv.exists("/p[q]/sub"), "the link is kept")
	assert.True(t, srv.exists("/pq/sub"), "the directory that the glob matches is kept")

	srv.writeFile("/r[s]/dir/sub/file.txt", "data")
	require.NoError(t, ftpFS.RemoveAll(ctx, "/r[s]"), "RemoveAll below a parent directory with glob characters")
	assert.False(t, srv.exists("/r[s]"))

	require.NotEmpty(t, srv.argsOf("LIST"))
	for _, arg := range srv.argsOf("LIST") {
		assert.Equal(t, "-a", arg, "LIST is sent without a path argument")
	}
	assert.Equal(t, []string{"/", "/p[q]", "/", "/r[s]", "/r[s]/dir"}, srv.argsOf("CWD"), "CWD to the parent directory of every removed directory")
}

// TestSymbolicLinks_ArrowInName checks names that contain " -> " on a server
// with MLST support, which the FTP client library takes for the end of the
// name of a symbolic link in a LIST line: RemoveAll removes a directory with
// such a name, whose DELE fails, and Remove and RemoveAll of a dangling link
// with such a name that the server refuses to delete return the error of
// DELE instead of reporting the link as missing, because LIST lists it. See
// TestSymbolicLinks_MLSDRemoveAllRefusedDelete for a link to a directory.
func TestSymbolicLinks_ArrowInName(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.writeFile("/tree/a -> b/sub/file.txt", "data")
	srv.writeFile("/root/file.txt", "data")
	srv.symlink("missing.txt", "/root/a -> b")
	srv.configure(func(s *testFTPServer) { s.refuse["DELE /root/a -> b"] = 100 })
	ftpFS := dialInProcess(t, srv)

	require.NoError(t, ftpFS.RemoveAll(ctx, "/tree"), "RemoveAll of a directory with an arrow in its name")
	assert.False(t, srv.exists("/tree"))

	for name, remove := range map[string]func(string) error{
		"Remove":    ftpFS.Remove,
		"RemoveAll": func(p string) error { return ftpFS.RemoveAll(ctx, p) },
	} {
		err := remove("/root/a -> b")
		require.Error(t, err, "%s of a dangling link that the server doesn't delete", name)
		assert.NotErrorIs(t, err, os.ErrNotExist, "%s: the link exists", name)
		var reply *textproto.Error
		if assert.ErrorAs(t, err, &reply, "%s: the DELE reply is wrapped", name) {
			assert.Equal(t, 550, reply.Code, "%s: the 550 reply to DELE", name)
		}
		assert.True(t, srv.exists("/root/a -> b"), "%s: the link is kept", name)
	}
}

// TestSymbolicLinks_MLSDRemoveAllOptionEntry is the regression test for the
// "LIST -a" that RemoveAll sends on a server with MLST support to tell
// a directory from a symbolic link: servers that don't parse the options of
// LIST, like ftpserverlib with DisableLISTArgs, take "-a" for a path and
// list the entry -a of the directory, so a client that can write into the
// directory of a link to a directory that the server refuses to delete could
// add a directory -a with a directory named like the link, which confirmed
// the link, and RemoveAll deleted the files that the link points to.
// RemoveAll must not use the listing of a directory with an entry named -a,
// also if MLST can't tell whether it has one, and Remove and RemoveAll of
// a path that MLST reports as missing report it as missing then, like for
// a directory that can't be listed, even if the entry -a lists the name.
func TestSymbolicLinks_MLSDRemoveAllOptionEntry(t *testing.T) {
	for _, tc := range []struct {
		name         string
		home         string // the working directory after the login, "/" if empty
		removed      string // the path of RemoveAll
		link         string // the absolute path of the link to /outside whose DELE is refused
		listArgPaths bool   // the server takes the argument of LIST for a path
		mlstReply    string // the reply to MLST of the entry -a next to the link if not empty
		fileDashA    bool   // the entry -a is a file instead of a directory with a directory named like the link
		wantErr      string
	}{
		{name: "server that takes -a for a path", removed: "/tree/link", link: "/tree/link", listArgPaths: true, wantErr: "entry named -a"},
		{name: "relative path", home: "/home/testuser", removed: "tree/link", link: "/home/testuser/tree/link", listArgPaths: true, wantErr: "entry named -a"},
		{name: "MLST fails", removed: "/tree/link", link: "/tree/link", listArgPaths: true, mlstReply: "501 not allowed", wantErr: "not allowed"},
		// RemoveAll can't tell whether the server parses the options
		{name: "server that parses options", removed: "/tree/link", link: "/tree/link", wantErr: "entry named -a"},
		{name: "file -a", removed: "/tree/link", link: "/tree/link", fileDashA: true, wantErr: "entry named -a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/outside/keep.txt", "keep")
			// Also the tree at the relative path below /, which has no entry -a
			srv.writeFile("/tree/file.txt", "data")
			srv.symlink("/outside", tc.link)
			dashA := path.Join(path.Dir(tc.link), "-a")
			if tc.fileDashA {
				srv.writeFile(dashA, "data")
			} else {
				srv.mkdirAll(path.Join(dashA, path.Base(tc.link)))
			}
			srv.configure(func(s *testFTPServer) {
				s.home = tc.home
				s.listArgPaths = tc.listArgPaths
				s.refuse["DELE "+tc.link] = 100
				if tc.mlstReply != "" {
					s.mlstReplies[dashA] = tc.mlstReply
				}
			})
			ftpFS := dialInProcess(t, srv)
			before := srv.paths()

			err := ftpFS.RemoveAll(t.Context(), tc.removed)
			require.Error(t, err, "RemoveAll must fail for a link that the server doesn't delete")
			assert.ErrorContains(t, err, tc.wantErr)
			assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile(tc.removed)), "the error names the link")
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
				assert.Equal(t, "permission denied", reply.Msg, "the reply to DELE")
			}
			assert.Equal(t, before, srv.paths(), "nothing must be deleted")
			assert.Contains(t, srv.argsOf("MLST"), dashA, "MLST checks the entry -a")
		})
	}

	t.Run("path that MLST reports as missing", func(t *testing.T) {
		ctx := t.Context()
		srv := newTestFTPServer(t)
		srv.writeFile("/tree/-a/gone", "data")
		srv.configure(func(s *testFTPServer) { s.listArgPaths = true })
		ftpFS := dialInProcess(t, srv)

		assert.ErrorIs(t, ftpFS.Remove("/tree/gone"), os.ErrNotExist)
		assert.NoError(t, ftpFS.RemoveAll(ctx, "/tree/gone"))
		assert.Equal(t, 2, srv.count("LIST"), "the parent directory is listed")
		assert.True(t, srv.exists("/tree/-a/gone"))
	})
}

// TestSymbolicLinks_MLSTRemoveAndMove checks Remove, RemoveAll and Move of
// symbolic links on a server with MLST support, which describes a link with
// the facts of its target, see the package documentation: a link to a file
// is deleted, renamed or replaced itself, a link to a directory is renamed
// itself, Remove of it fails like for a directory and Move onto it is
// refused like onto a directory. MLST reports a dangling link or a cycle of
// links as missing; RemoveAll kept such a link, and Remove and Move of it
// failed with os.ErrNotExist. Move onto such a link replaces it.
func TestSymbolicLinks_MLSTRemoveAndMove(t *testing.T) {
	type check func(t *testing.T, srv *testFTPServer, before []string)
	// unchanged checks that nothing was changed on the server
	unchanged := func(t *testing.T, srv *testFTPServer, before []string) {
		assert.Equal(t, before, srv.paths(), "nothing must be changed")
	}
	// gone returns a check that the paths no longer exist,
	// and that the targets of the links are kept
	gone := func(paths ...string) check {
		return func(t *testing.T, srv *testFTPServer, _ []string) {
			for _, p := range paths {
				assert.False(t, srv.exists(p), "%s must be removed", p)
			}
			assert.Equal(t, "data", string(srv.fileContent("/root/file.txt")), "the target of the links is kept")
			assert.Equal(t, "content", string(srv.fileContent("/root/dir/file.txt")), "the target of the links is kept")
		}
	}
	// renamed returns a check that the link from was renamed to to
	// with its target, which is kept
	renamed := func(from, to, target string) check {
		return func(t *testing.T, srv *testFTPServer, _ []string) {
			gone(from)(t, srv, nil)
			srv.mu.Lock()
			node := srv.nodes[to]
			srv.mu.Unlock()
			if assert.NotNil(t, node, "%s must exist", to) {
				assert.Equal(t, target, node.link, "%s is the renamed link", to)
			}
		}
	}
	// replaced returns a check that /root/new.txt replaced the link at dest
	replaced := func(dest string) check {
		return func(t *testing.T, srv *testFTPServer, _ []string) {
			gone("/root/new.txt")(t, srv, nil)
			assert.Equal(t, "new", string(srv.fileContent(dest)), "%s is replaced", dest)
		}
	}

	for _, tc := range []struct {
		name    string
		op      func(ctx context.Context, f *fileSystem) error
		wantErr error
		check   check
	}{
		{
			name:  "Remove of a link to a file",
			op:    func(_ context.Context, f *fileSystem) error { return f.Remove("/root/filelink") },
			check: gone("/root/filelink"),
		},
		{
			name:  "RemoveAll of a link to a file",
			op:    func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/filelink") },
			check: gone("/root/filelink"),
		},
		{
			name:  "Move of a link to a file",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/filelink", "/root/moved") },
			check: renamed("/root/filelink", "/root/moved", "file.txt"),
		},
		{
			name:  "Move onto a link to a file",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/new.txt", "/root/filelink") },
			check: replaced("/root/filelink"),
		},
		{
			name:    "Remove of a link to a directory",
			op:      func(_ context.Context, f *fileSystem) error { return f.Remove("/root/dirlink") },
			wantErr: fs.ErrDirectoryNotEmpty{},
			check:   unchanged,
		},
		{
			name:  "RemoveAll of a link to a directory",
			op:    func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/dirlink") },
			check: gone("/root/dirlink"),
		},
		{
			name:  "Move of a link to a directory",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/dirlink", "/root/moved") },
			check: renamed("/root/dirlink", "/root/moved", "dir"),
		},
		{
			name:    "Move onto a link to a directory",
			op:      func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/new.txt", "/root/dirlink") },
			wantErr: os.ErrExist,
			check:   unchanged,
		},
		{
			name:  "Remove of a dangling link",
			op:    func(_ context.Context, f *fileSystem) error { return f.Remove("/root/dangling") },
			check: gone("/root/dangling"),
		},
		{
			name:  "RemoveAll of a dangling link",
			op:    func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/dangling") },
			check: gone("/root/dangling"),
		},
		{
			name:  "Move of a dangling link",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/dangling", "/root/moved") },
			check: renamed("/root/dangling", "/root/moved", "missing.txt"),
		},
		{
			name:  "Move onto a dangling link",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/new.txt", "/root/dangling") },
			check: replaced("/root/dangling"),
		},
		{
			name:  "Remove of a cycle of links",
			op:    func(_ context.Context, f *fileSystem) error { return f.Remove("/root/loop1") },
			check: gone("/root/loop1"),
		},
		{
			name:  "RemoveAll of a cycle of links",
			op:    func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/loop1") },
			check: gone("/root/loop1"),
		},
		{
			name:  "Move of a link of a cycle",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/loop1", "/root/moved") },
			check: renamed("/root/loop1", "/root/moved", "loop2"),
		},
		{
			name:  "Move onto a link of a cycle",
			op:    func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/new.txt", "/root/loop1") },
			check: replaced("/root/loop1"),
		},
		{
			name:    "Remove of a missing path",
			op:      func(_ context.Context, f *fileSystem) error { return f.Remove("/root/missing") },
			wantErr: os.ErrNotExist,
			check:   unchanged,
		},
		{
			name:  "RemoveAll of a missing path",
			op:    func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/missing") },
			check: unchanged,
		},
		{
			name:    "Move of a missing path",
			op:      func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/missing", "/root/moved") },
			wantErr: os.ErrNotExist,
			check:   unchanged,
		},
		{
			name:    "Move of a missing path onto a directory",
			op:      func(ctx context.Context, f *fileSystem) error { return f.Move(ctx, "/root/missing", "/root/dir") },
			wantErr: os.ErrNotExist,
			check:   unchanged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			seedLinks(srv)
			srv.writeFile("/root/new.txt", "new")
			ftpFS := dialInProcess(t, srv)
			before := srv.paths()

			err := tc.op(t.Context(), ftpFS)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			tc.check(t, srv, before)
		})
	}
}

// TestSymbolicLinks_MLSDDangling checks a dangling symbolic link that
// a server with MLST support lists with the RFC 3659 type
// "OS.unix=slink:<target>": the FTP client library reads it as a file,
// Stat reports it as missing, because MLST follows the link, and RemoveAll
// of its directory deletes it.
func TestSymbolicLinks_MLSDDangling(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.writeFile("/root/file.txt", "data")
	srv.symlink("missing.txt", "/root/dangling")
	ftpFS := dialInProcess(t, srv)

	infos := map[string]*fs.FileInfo{}
	err := ftpFS.ListDir(ctx, "/root", nil, func(info *fs.FileInfo) error {
		infos[info.Name] = info
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"dangling", "file.txt"}, slices.Collect(maps.Keys(infos)))
	assert.True(t, infos["dangling"].IsRegular, "the client library reads OS.unix=slink as a file")
	assert.False(t, infos["dangling"].IsSymlink)

	_, err = ftpFS.Stat("/root/dangling")
	assert.ErrorIs(t, err, os.ErrNotExist, "MLST follows the dangling link")

	require.NoError(t, ftpFS.RemoveAll(ctx, "/root"))
	assert.Empty(t, srv.paths(), "RemoveAll deletes the dangling link with its directory")
}

// TestSymbolicLinks_MLSTRemoveRefusedDangling is the regression test for
// Remove and RemoveAll on a server with MLST support of a dangling symbolic
// link or a link of a cycle that the server refuses to delete, like in a
// directory without write permission: MLST reports such a link as missing,
// and the failure reply of DELE was taken for a missing path, so RemoveAll
// returned nil and Remove an error wrapping os.ErrNotExist although the link
// exists. They must return the error of DELE unless LIST of the parent
// directory doesn't list the path.
func TestSymbolicLinks_MLSTRemoveRefusedDangling(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	seedLinks(srv)
	srv.configure(func(s *testFTPServer) {
		s.refuse["DELE /root/dangling"] = 100
		s.refuse["DELE /root/loop1"] = 100
	})
	ftpFS := dialInProcess(t, srv)

	for name, remove := range map[string]func(string) error{
		"Remove":    ftpFS.Remove,
		"RemoveAll": func(p string) error { return ftpFS.RemoveAll(ctx, p) },
	} {
		for _, link := range []string{"/root/dangling", "/root/loop1"} {
			err := remove(link)
			require.Error(t, err, "%s(%s) of a link that the server doesn't delete", name, link)
			assert.NotErrorIs(t, err, os.ErrNotExist, "%s(%s): the link exists", name, link)
			assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile(link)), "%s(%s): the error names the link", name, link)
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "%s(%s): the DELE reply is wrapped", name, link) {
				assert.Equal(t, 550, reply.Code, "%s(%s): the 550 reply to DELE", name, link)
			}
			assert.True(t, srv.exists(link), "%s(%s): the link is kept", name, link)
		}
	}
	// The connections for LIST are closed after the operations
	require.Eventually(t, func() bool { return srv.open.Load() == 1 }, 5*time.Second, time.Millisecond,
		"%d connections are open", srv.open.Load())

	// A missing path is still missing, also in a directory
	// that is missing or that can't be listed
	srv.mkdirAll("/forbidden")
	srv.configure(func(s *testFTPServer) { s.failListContains = "/forbidden" })
	for _, missing := range []string{"/root/missing", "/missing/missing", "/forbidden/missing"} {
		assert.ErrorIs(t, ftpFS.Remove(missing), os.ErrNotExist, "Remove(%s)", missing)
		assert.NoError(t, ftpFS.RemoveAll(ctx, missing), "RemoveAll(%s)", missing)
	}
}

// TestRemoveAll_ListingPerConfirmation checks that RemoveAll on a server
// with MLST support changes to the parent directory of every removed
// directory and lists it with LIST again for every directory, also for
// several directories in it, see TestRemoveAll_DirectoryReplacedByLink,
// but checks a directory for an entry named -a only once.
func TestRemoveAll_ListingPerConfirmation(t *testing.T) {
	srv := newTestFTPServer(t)
	for _, dir := range []string{"a", "b", "c"} {
		srv.writeFile("/tree/"+dir+"/x", "data")
	}
	ftpFS := dialInProcess(t, srv)

	require.NoError(t, ftpFS.RemoveAll(t.Context(), "/tree"))
	assert.Empty(t, srv.paths())
	assert.Equal(t, 4, srv.count("LIST"), "LIST of / for /tree and of /tree for each of /tree/a, /tree/b and /tree/c")
	assert.Equal(t, []string{"/", "/tree", "/tree", "/tree"}, srv.argsOf("CWD"))
	assert.Equal(t, []string{"/tree", "/-a", "/tree/-a"}, srv.argsOf("MLST"), "MLST of /tree and one check of each directory for an entry -a")
}

// TestRemoveAll_DirectoryReplacedByLink is the regression test for the LIST
// listings that RemoveAll on a server with MLST support kept while it
// removed the directories in a directory: another client replaced the
// directory /tree/d2 with a symbolic link to /outside after the LIST of
// /tree that confirmed /tree/d1, the server refused to delete the link, and
// RemoveAll took the kept listing, which listed d2 as a directory, for
// the confirmation and deleted the files that the link points to. Every
// confirmation must list the parent directory again.
func TestRemoveAll_DirectoryReplacedByLink(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.writeFile("/outside/keep.txt", "keep")
	srv.writeFile("/tree/d1/file.txt", "data")
	srv.writeFile("/tree/d2/keep.txt", "data")
	srv.configure(func(s *testFTPServer) {
		// Right before the DELE of d2, which follows the confirmation of d1
		s.linkBefore["DELE /tree/d2"] = "/outside"
		s.refuse["DELE /tree/d2"] = 100
	})
	ftpFS := dialInProcess(t, srv)

	err := ftpFS.RemoveAll(t.Context(), "/tree")
	require.Error(t, err, "RemoveAll must fail for the link that the server doesn't delete")
	assert.ErrorContains(t, err, "doesn't list it as a directory")
	assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile("/tree/d2")), "the error names the link")
	var reply *textproto.Error
	if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
		assert.Equal(t, "permission denied", reply.Msg, "the reply to DELE")
	}
	assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")), "RemoveAll must keep the files that the link points to")
	assert.Equal(t, []string{"/outside", "/outside/keep.txt", "/tree", "/tree/d2"}, srv.paths(), "d1 is removed and the link is kept")
	assert.Equal(t, 3, srv.count("LIST"), "LIST of / for /tree, and of /tree for /tree/d1 and again for /tree/d2")
}

// TestRemoveAll_GlobbedLISTKnownLimitation documents a known limitation, see
// the package documentation: on a server without MLST support that expands
// glob characters in the path argument of LIST, like wu-ftpd or ProFTPD with
// UseGlobbing but without mod_facts, LIST of /tree/a[b] lists /tree/ab, so
// RemoveAll takes the symbolic link /tree/a[b]/L to /victim for the directory
// /tree/ab/L and deletes the file /victim/keep.txt that the link points to.
// Change the test when RemoveAll lists without a path argument there too.
func TestRemoveAll_GlobbedLISTKnownLimitation(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.writeFile("/victim/keep.txt", "keep")
	srv.writeFile("/tree/ab/L/keep.txt", "data")
	srv.symlink("/victim", "/tree/a[b]/L")
	srv.configure(func(s *testFTPServer) {
		s.noMLST = true
		s.globArgs = true
	})
	ftpFS := dialInProcess(t, srv)

	err := ftpFS.RemoveAll(t.Context(), "/tree")
	assert.Error(t, err, "RMD of the link fails")
	assert.False(t, srv.exists("/victim/keep.txt"), "known limitation: RemoveAll deletes the file that the link points to")
	assert.True(t, srv.exists("/tree/ab/L/keep.txt"), "the file that the listing showed is kept")
	assert.True(t, srv.exists("/tree/a[b]/L"), "the link is kept")
}

// TestRemoveAll_RelativePath checks RemoveAll of a relative path on a
// server with MLST support, which resolves it against the working
// directory after the login: the connection for LIST changes to the
// parent directories with CWD, so it must resolve their relative paths
// against that directory too, not against the directory of its last CWD.
func TestRemoveAll_RelativePath(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.home = "/home/testuser" })
	srv.writeFile("/home/testuser/tree/a/b/file.txt", "data")
	srv.writeFile("/home/testuser/keep.txt", "keep")
	ftpFS := dialInProcess(t, srv)

	require.NoError(t, ftpFS.RemoveAll(t.Context(), "tree"))
	assert.Equal(t, []string{"/home", "/home/testuser", "/home/testuser/keep.txt"}, srv.paths())
	assert.Equal(t, []string{"/home/testuser", "/home/testuser/tree", "/home/testuser/tree/a"}, srv.argsOf("CWD"))
}

// TestRemoveAll_RelativePathRefusedDelete checks relative paths on a server
// with MLST support that refuses to delete a symbolic link: RemoveAll of
// a directory with a link to a directory and Remove and RemoveAll of
// a dangling link. The connection for LIST must list the parent directories
// resolved against the working directory after the login: below / the same
// relative paths have a directory at the path of the link to a directory
// and no dangling link, which a listing of / would confirm or not list.
func TestRemoveAll_RelativePathRefusedDelete(t *testing.T) {
	const home = "/home/testuser"
	for _, tc := range []struct {
		name      string
		removed   string // the relative path of RemoveAll
		listOmits bool   // LIST doesn't list the link
		forged    int    // LIST adds this many lines that list the link as a directory
	}{
		{name: "LIST doesn't list the link", removed: "tree", listOmits: true},
		{name: "LIST lists the link", removed: "tree"},
		{name: "LIST lists the link at the removed path", removed: "tree/link"},
		{name: "LIST lists the link twice", removed: "tree", forged: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/outside/keep.txt", "keep")
			srv.writeFile(home+"/tree/file.txt", "data")
			srv.symlink("/outside", home+"/tree/link")
			srv.writeFile("/tree/link/file.txt", "data")
			srv.configure(func(s *testFTPServer) {
				s.home = home
				s.refuse["DELE "+home+"/tree/link"] = 100
				if tc.listOmits {
					s.listOmits = home + "/tree/link"
				}
				s.listForged[home+"/tree/link"] = tc.forged
			})
			ftpFS := dialInProcess(t, srv)

			err := ftpFS.RemoveAll(t.Context(), tc.removed)
			require.Error(t, err, "RemoveAll must fail for a link that the server doesn't delete")
			assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile("tree/link")), "the error names the link")
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
				assert.Equal(t, "permission denied", reply.Msg, "the reply to DELE")
			}
			assert.Equal(t, "keep", string(srv.fileContent("/outside/keep.txt")), "RemoveAll must keep the files that the link points to")
			assert.True(t, srv.exists(home+"/tree/link"), "the link is kept")
			assert.True(t, srv.exists("/tree/link/file.txt"), "the tree below / is kept")
		})
	}

	t.Run("dangling link", func(t *testing.T) {
		ctx := t.Context()
		srv := newTestFTPServer(t)
		srv.symlink("missing.txt", home+"/root/dangling")
		srv.writeFile("/root/file.txt", "data")
		srv.configure(func(s *testFTPServer) {
			s.home = home
			s.refuse["DELE "+home+"/root/dangling"] = 100
		})
		ftpFS := dialInProcess(t, srv)

		for name, remove := range map[string]func(string) error{
			"Remove":    ftpFS.Remove,
			"RemoveAll": func(p string) error { return ftpFS.RemoveAll(ctx, p) },
		} {
			err := remove("root/dangling")
			require.Error(t, err, "%s of a link that the server doesn't delete", name)
			assert.NotErrorIs(t, err, os.ErrNotExist, "%s: the link exists", name)
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "%s: the DELE reply is wrapped", name) {
				assert.Equal(t, "permission denied", reply.Msg, "%s: the reply to DELE", name)
			}
			assert.True(t, srv.exists(home+"/root/dangling"), "%s: the link is kept", name)
		}
	})
}

// TestRemoveAll_ListingPWDFails checks RemoveAll of a relative path on
// a server with MLST support when the connection for LIST can't read the
// working directory after the login, against which it resolves relative
// paths, with PWD: PWD fails or replies a path that is not absolute.
// RemoveAll fails with an error wrapping the failure of DELE and deletes
// nothing. The login directory has a directory home with a directory tree,
// which the relative PWD reply would refer to.
func TestRemoveAll_ListingPWDFails(t *testing.T) {
	for _, tc := range []struct{ name, pwdReply, wantErr string }{
		{name: "PWD fails", pwdReply: "550 PWD not allowed", wantErr: "PWD not allowed"},
		{name: "relative PWD reply", pwdReply: `257 "home"`, wantErr: `PWD replied "home"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/home/testuser/tree/file.txt", "data")
			srv.writeFile("/home/testuser/tree/sub/file.txt", "data")
			srv.mkdirAll("/home/testuser/home/tree")
			srv.configure(func(s *testFTPServer) {
				s.home = "/home/testuser"
				s.pwdReply = tc.pwdReply
			})
			ftpFS := dialInProcess(t, srv)
			before := srv.paths()

			err := ftpFS.RemoveAll(t.Context(), "tree")
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr, "the error tells why there is no listing")
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
				assert.Equal(t, "file not found", reply.Msg, "the reply to DELE, not to PWD")
			}
			assert.Equal(t, before, srv.paths(), "nothing must be deleted")
			assert.Equal(t, 1, srv.count("PWD"))
		})
	}
}

// TestRemoveAll_ListingConnectionBreaks is the regression test for the
// connection that RemoveAll dials for LIST on a server with MLST support
// when it breaks, like when the server closed it while it was idle during
// the deletes on the connection of the operation: RemoveAll failed partway.
// LIST changes nothing on the server, so the connection is dialed again
// once for the listing.
func TestRemoveAll_ListingConnectionBreaks(t *testing.T) {
	for _, verb := range []string{"CWD", "LIST"} {
		t.Run(verb, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/tree/file.txt", "data")
			srv.writeFile("/tree/sub/file.txt", "data")
			// The second listing finds the connection closed
			srv.configure(func(s *testFTPServer) { s.dropAt[verb] = 2 })
			ftpFS := dialInProcess(t, srv)

			require.NoError(t, ftpFS.RemoveAll(t.Context(), "/tree"))
			assert.Empty(t, srv.paths())
			assert.Equal(t, 3, srv.count(verb), "the dropped %s is sent again on a new connection", verb)
			assert.Equal(t, int64(3), srv.logins.Load(), "the connection of the operation and two for LIST")
			require.Eventually(t, func() bool { return srv.open.Load() == 1 }, 5*time.Second, time.Millisecond,
				"%d connections are open", srv.open.Load())
		})
	}
}

// TestRemoveAll_ListingConnectionFails checks RemoveAll on a server with
// MLST support when the connection for LIST can't be used, because LIST
// fails twice or the connection can't be dialed: RemoveAll fails with an
// error wrapping the failure of DELE and deletes nothing, not through a
// symbolic link either. A failure of the connection for LIST is no failure
// of the connection of the operation, which is kept.
func TestRemoveAll_ListingConnectionFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      *Options
		configure func(s *testFTPServer)
		logins    int64 // of the connection of the operation and the connections for LIST
	}{
		{
			name:      "LIST fails twice",
			opts:      &Options{Password: testPassword},
			configure: func(s *testFTPServer) { s.dropBefore["LIST"] = 2 },
			logins:    3,
		},
		{
			name: "dial fails",
			// The dial waits for the greeting of the silent server until the timeout
			opts:      &Options{Password: testPassword, Timeout: 500 * time.Millisecond},
			configure: func(s *testFTPServer) { s.silent = true },
			logins:    1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.writeFile("/outside/keep.txt", "keep")
			srv.writeFile("/tree/sub/file.txt", "data")
			srv.symlink("/outside", "/tree/link")
			srv.configure(func(s *testFTPServer) { s.refuse["DELE /tree/link"] = 100 })
			ftpFS := dialInProcessWith(t, srv, tc.opts)
			before := srv.paths()
			srv.configure(tc.configure)

			err := ftpFS.RemoveAll(t.Context(), "/tree")
			require.Error(t, err)
			var reply *textproto.Error
			if assert.ErrorAs(t, err, &reply, "the DELE reply is wrapped") {
				assert.Equal(t, 550, reply.Code, "the 550 reply to DELE")
			}
			assert.NotContains(t, err.Error(), "broke after a command was sent")
			assert.Equal(t, before, srv.paths(), "nothing must be deleted")
			// RemoveAll is not repeated on a new connection, and
			// the connection for LIST is dialed at most twice
			assert.Equal(t, tc.logins, srv.logins.Load(), "logins")
			require.Eventually(t, func() bool { return srv.open.Load() == 1 }, 5*time.Second, time.Millisecond,
				"%d connections are open", srv.open.Load())

			srv.configure(func(s *testFTPServer) { s.silent = false })
			_, err = ftpFS.Stat("/tree")
			require.NoError(t, err)
			assert.Equal(t, tc.logins, srv.logins.Load(), "the connection of the operation is kept")
		})
	}
}

// TestRemoveAll_ListingCheckConnectionBreaks checks Remove and RemoveAll on
// a server with MLST support when the connection of the operation breaks
// during the MLST that checks a LIST listing for an entry named -a: that is
// no failure of the listing but of the connection of the operation. RemoveAll,
// which removes nothing that should be kept when repeated, is retried on
// a new connection, and Remove, which sent DELE before, returns the
// connection error instead of reporting a refused dangling link as missing.
func TestRemoveAll_ListingCheckConnectionBreaks(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		srv := newTestFTPServer(t)
		srv.writeFile("/tree/sub/file.txt", "data")
		// The first MLST is the one of /tree, the second the check of the listing of /
		srv.configure(func(s *testFTPServer) { s.dropAt["MLST"] = 2 })
		ftpFS := dialInProcess(t, srv)

		require.NoError(t, ftpFS.RemoveAll(t.Context(), "/tree"))
		assert.Empty(t, srv.paths())
		assert.Equal(t, int64(4), srv.logins.Load(), "two connections of the operation and two for LIST")
		require.Eventually(t, func() bool { return srv.open.Load() == 1 }, 5*time.Second, time.Millisecond,
			"%d connections are open", srv.open.Load())
	})

	for _, tc := range []struct {
		name    string
		remove  func(ctx context.Context, f *fileSystem) error
		wantErr string
	}{
		{
			name:    "Remove of a dangling link",
			remove:  func(_ context.Context, f *fileSystem) error { return f.Remove("/root/dangling") },
			wantErr: "broke after a command was sent",
		},
		{
			name:    "RemoveAll of a dangling link",
			remove:  func(ctx context.Context, f *fileSystem) error { return f.RemoveAll(ctx, "/root/dangling") },
			wantErr: "permission denied", // of DELE after the retry
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestFTPServer(t)
			srv.symlink("missing.txt", "/root/dangling")
			// The first MLST is the one of the link, the second the check of the listing of /root
			srv.configure(func(s *testFTPServer) {
				s.refuse["DELE /root/dangling"] = 100
				s.dropAt["MLST"] = 2
			})
			ftpFS := dialInProcess(t, srv)

			err := tc.remove(t.Context(), ftpFS)
			require.Error(t, err)
			assert.NotErrorIs(t, err, os.ErrNotExist, "the link exists")
			assert.ErrorContains(t, err, tc.wantErr)
			assert.True(t, srv.exists("/root/dangling"), "the link is kept")
		})
	}
}

// TestRemoveAll_ListingHangs verifies that the connection that RemoveAll
// dials for LIST on a server with MLST support follows the context of the
// operation and Close: a server that never answers LIST blocks RemoveAll
// only until the context is done or the file system is closed, and nothing
// is deleted.
func TestRemoveAll_ListingHangs(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		srv := newTestFTPServer(t)
		srv.writeFile("/tree/sub/file.txt", "data")
		srv.configure(func(s *testFTPServer) { s.hang = "LIST" })
		ftpFS := dialInProcess(t, srv)
		before := srv.paths()

		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		err := returnsWithin(t, 3*time.Second, func() error { return ftpFS.RemoveAll(ctx, "/tree") })
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, before, srv.paths(), "nothing must be deleted")
		require.Eventually(t, func() bool { return srv.open.Load() == 0 }, 5*time.Second, time.Millisecond,
			"%d connections are open", srv.open.Load())

		// Also the LIST for a path that MLST reports as missing,
		// which is not reported as removed then
		ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		err = returnsWithin(t, 3*time.Second, func() error { return ftpFS.RemoveAll(ctx, "/missing") })
		require.ErrorIs(t, err, context.DeadlineExceeded)

		srv.configure(func(s *testFTPServer) { s.hang = "" })
		require.NoError(t, ftpFS.RemoveAll(t.Context(), "/tree"))
		assert.Empty(t, srv.paths())
	})

	t.Run("Close", func(t *testing.T) {
		srv := newTestFTPServer(t)
		srv.writeFile("/tree/sub/file.txt", "data")
		srv.configure(func(s *testFTPServer) { s.hang = "LIST" })
		ftpFS := dialInProcess(t, srv)
		before := srv.paths()

		hung := make(chan error, 1)
		go func() { hung <- ftpFS.RemoveAll(t.Context(), "/tree") }()
		require.Eventually(t, func() bool { return srv.count("LIST") == 1 }, 5*time.Second, time.Millisecond)
		require.NoError(t, returnsWithin(t, time.Second, ftpFS.Close))
		select {
		case err := <-hung:
			assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
		case <-time.After(3 * time.Second):
			t.Fatal("RemoveAll did not return after Close")
		}
		assert.Equal(t, before, srv.paths(), "nothing must be deleted")
		require.Eventually(t, func() bool { return srv.open.Load() == 0 }, 5*time.Second, time.Millisecond,
			"%d connections are open", srv.open.Load())
	})
}

// TestListDir_EntryNamedLikeDirectory checks a directory whose only entry
// has the name of the directory, like /logs/app with the file app, which
// looks like the listing of a file or a link that the server lists itself:
// ListDir and ListDirRecursive list the entry.
func TestListDir_EntryNamedLikeDirectory(t *testing.T) {
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
			srv.configure(func(s *testFTPServer) { s.noMLST = variant.noMLST })
			srv.writeFile("/logs/app/app", "log")
			ftpFS := dialInProcess(t, srv)

			for name, list := range map[string]func(context.Context, string, []string, func(*fs.FileInfo) error) error{
				"ListDir":          ftpFS.ListDir,
				"ListDirRecursive": ftpFS.ListDirRecursive,
			} {
				var listed []*fs.FileInfo
				err := list(ctx, "/logs/app", nil, func(info *fs.FileInfo) error {
					listed = append(listed, info)
					return nil
				})
				require.NoError(t, err, name)
				assert.Equal(t, []string{"/logs/app/app"}, relPaths(ftpFS, listed), name)
				if assert.Len(t, listed, 1, name) {
					assert.True(t, listed[0].IsRegular, "%s: the entry is a file", name)
				}
			}
		})
	}
}

// TestCopyRecursive_SymbolicLinks checks fs.CopyRecursive from a server
// that lists symbolic links: a link to a file is copied as a file with the
// content of its target, and a link to a directory fails the copy with an
// error wrapping errors.ErrUnsupported before anything is written, to
// another file system and within the FTP file system, which can't create
// links. The copy used to treat a link to a directory like a file.
func TestCopyRecursive_SymbolicLinks(t *testing.T) {
	ctx := t.Context()
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.noMLST = true })
	seedLinks(srv)
	srv.writeFile("/files/file.txt", "data")
	srv.symlink("file.txt", "/files/link.txt")
	srv.symlink("/files/file.txt", "/files/sub/abs.txt")
	srv.symlink("../link.txt", "/files/sub/chain.txt")
	ftpFS := dialInProcess(t, srv)
	require.NoError(t, fs.Register(ftpFS))
	memFS, err := fs.NewMemFileSystem(nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(memFS))
	t.Cleanup(func() { _ = memFS.Close() })

	dest := memFS.RootDir().Join("copy")
	err = fs.CopyRecursive(ctx, ftpFS.JoinCleanFile("/root"), dest)
	require.ErrorIs(t, err, errors.ErrUnsupported, "CopyRecursive of a link to a directory to another file system")
	assert.ErrorContains(t, err, string(ftpFS.JoinCleanFile("/root/dirlink")), "the error names the link")
	assert.False(t, dest.Exists(), "nothing must be written")

	before := srv.paths()
	err = fs.CopyRecursive(ctx, ftpFS.JoinCleanFile("/root"), ftpFS.JoinCleanFile("/copy"))
	require.ErrorIs(t, err, errors.ErrUnsupported, "CopyRecursive of a link to a directory within the FTP file system")
	assert.Equal(t, before, srv.paths(), "nothing must be written")

	require.NoError(t, fs.CopyRecursive(ctx, ftpFS.JoinCleanFile("/files"), dest))
	for _, name := range [][]string{{"file.txt"}, {"link.txt"}, {"sub", "abs.txt"}, {"sub", "chain.txt"}} {
		copied := dest.Join(name...)
		info, err := copied.Lstat()
		if !assert.NoError(t, err, "Lstat(%s)", copied) {
			continue
		}
		assert.True(t, info.IsRegular, "%s is copied as a file", copied)
		assert.False(t, info.IsSymlink, "%s is copied as a file", copied)
		content, err := copied.ReadAllString(ctx)
		require.NoError(t, err)
		assert.Equal(t, "data", content, "%s has the content of the target", copied)
	}
}

// TestBackslashInNames checks that paths with a backslash are rejected
// unless Options.AllowBackslashInNames is set, because an FTP server
// on Windows takes `..\x` for a name in the parent directory.
func TestBackslashInNames(t *testing.T) {
	srv := newTestFTPServer(t)
	ctx := t.Context()
	srv.mkdirAll("/dir")
	srv.writeFile("/victim.txt", "keep me")
	before := srv.paths()

	ftpFS := dialInProcess(t, srv)
	noCallback := func(*fs.FileInfo) error { return nil }
	for _, name := range []string{`/dir/..\victim.txt`, `/dir\x`} {
		for op, call := range map[string]func() error{
			"Stat":             func() error { _, err := ftpFS.Stat(name); return err },
			"ListDir":          func() error { return ftpFS.ListDir(ctx, name, nil, noCallback) },
			"ListDirRecursive": func() error { return ftpFS.ListDirRecursive(ctx, name, nil, noCallback) },
			"OpenReader":       func() error { _, err := ftpFS.OpenReader(name); return err },
			"ReadAll":          func() error { _, err := ftpFS.ReadAll(ctx, name); return err },
			"WriteAll":         func() error { return ftpFS.WriteAll(ctx, name, []byte("x"), 0) },
			"Append":           func() error { return ftpFS.Append(ctx, name, []byte("x"), 0) },
			"OpenWriter":       func() error { _, err := ftpFS.OpenWriter(name, 0); return err },
			"OpenAppendWriter": func() error { _, err := ftpFS.OpenAppendWriter(name, 0); return err },
			"OpenReadWriter":   func() error { _, err := ftpFS.OpenReadWriter(name, 0); return err },
			"Touch":            func() error { return ftpFS.Touch(name, 0) },
			"MakeDir":          func() error { return ftpFS.MakeDir(name, 0) },
			"Remove":           func() error { return ftpFS.Remove(name) },
			"RemoveAll":        func() error { return ftpFS.RemoveAll(ctx, name) },
			"Move from":        func() error { return ftpFS.Move(ctx, name, "/moved.txt") },
			"Move to":          func() error { return ftpFS.Move(ctx, "/victim.txt", name) },
		} {
			assert.ErrorIs(t, call(), os.ErrInvalid, "%s(%q)", op, name)
		}
	}
	assert.Equal(t, before, srv.paths(), "no request may reach the server")
	assert.Equal(t, "keep me", string(srv.fileContent("/victim.txt")))

	// Servers on Unix store a backslash as part of a name
	allowFS := dialInProcessWith(t, srv, &Options{Password: testPassword, AllowBackslashInNames: true})
	require.NoError(t, allowFS.WriteAll(ctx, `/dir/a\b.txt`, []byte("unix name"), 0))
	assert.Equal(t, "unix name", string(srv.fileContent(`/dir/a\b.txt`)))
	data, err := allowFS.ReadAll(ctx, `/dir/a\b.txt`)
	require.NoError(t, err)
	assert.Equal(t, "unix name", string(data))

	// The target of a symbolic link that the server lists is checked like a path
	linkSrv := newTestFTPServer(t)
	linkSrv.configure(func(s *testFTPServer) { s.noMLST = true })
	linkSrv.symlink(`..\victim.txt`, "/dir/link")
	linkFS := dialInProcess(t, linkSrv)
	_, err = linkFS.Stat("/dir/link")
	assert.ErrorIs(t, err, os.ErrInvalid, "Stat of a link with a backslash in its target")
}
