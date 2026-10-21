package ftpfs

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// silentListener accepts connections and never answers, like a hung server.
func silentListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().String()
}

// returnsWithin calls f in a goroutine and fails the test
// if it doesn't return within d.
func returnsWithin(t *testing.T, d time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("did not return within %s", d)
		return nil
	}
}

// TestDial_Context verifies that the context limits the server greeting
// and the login, not only the TCP connect.
func TestDial_Context(t *testing.T) {
	addr := silentListener(t)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := returnsWithin(t, 3*time.Second, func() error {
		_, err := Dial(ctx, "ftp://u@"+addr, &Options{Password: "S3cret"})
		return err
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotContains(t, err.Error(), "S3cret")
}

// TestDial_Timeout verifies that Options.Timeout limits a dial
// with a context without deadline.
func TestDial_Timeout(t *testing.T) {
	addr := silentListener(t)
	err := returnsWithin(t, 3*time.Second, func() error {
		_, err := Dial(t.Context(), "ftp://u@"+addr, &Options{Timeout: 200 * time.Millisecond})
		return err
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestDial_LoginError verifies that a failing login is an error.
func TestDial_LoginError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tc := textproto.NewConn(c)
		_ = tc.PrintfLine("220 ready")
		_, _ = tc.ReadLine()
		_ = tc.PrintfLine("530 login incorrect")
		_, _ = tc.ReadLine()
	}()
	_, err = Dial(t.Context(), "ftp://u@"+ln.Addr().String(), &Options{Password: "S3cret"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "S3cret")
}

// TestDialContext verifies that Options.DialContext is used for the control
// and the data connections, and that the data connections go to the host of
// the control connection even if the PASV reply names another host.
func TestDialContext(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.pasvHost = "10,255,255,1" })
	var dialed []string
	ftpFS := dialInProcessWith(t, srv, &Options{
		Password:    testPassword,
		DisableEPSV: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	})
	data := []byte("data")
	require.NoError(t, ftpFS.WriteAll(t.Context(), "/file.txt", data, 0))
	got, err := ftpFS.ReadAll(t.Context(), "/file.txt")
	require.NoError(t, err)
	assert.Equal(t, data, got)

	host, _, _ := net.SplitHostPort(srv.addr())
	require.Len(t, dialed, 3, "control connection and two data connections")
	for _, addr := range dialed {
		dialedHost, _, err := net.SplitHostPort(addr)
		require.NoError(t, err)
		assert.Equal(t, host, dialedHost, "every connection goes to the control host")
	}
	assert.Zero(t, srv.count("EPSV"), "DisableEPSV")
	assert.Equal(t, 2, srv.count("PASV"))
}

// TestDialContext_NoTCPAddr verifies that a DialContext returning a
// connection without a *net.TCPAddr doesn't make the FTP library panic.
func TestDialContext_NoTCPAddr(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcessWith(t, srv, &Options{
		Password: testPassword,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, network, addr)
			return unixAddrConn{c}, err
		},
	})
	_, err := ftpFS.ReadAll(t.Context(), "/missing.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	srv.writeFile("/file.txt", "content")
	got, err := ftpFS.ReadAll(t.Context(), "/file.txt")
	require.NoError(t, err)
	assert.Equal(t, "content", string(got))
}

type unixAddrConn struct{ net.Conn }

func (unixAddrConn) RemoteAddr() net.Addr { return &net.UnixAddr{Name: "proxy", Net: "unix"} }

// TestEPSV verifies that EPSV is used by default, with a fallback to
// PASV for servers without EPSV, and that the data connection of a PASV
// reply with a foreign address goes to the control host.
func TestEPSV(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	require.NoError(t, ftpFS.WriteAll(t.Context(), "/a.txt", []byte("a"), 0))
	assert.Equal(t, 1, srv.count("EPSV"))
	assert.Zero(t, srv.count("PASV"))

	srv = newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) {
		s.noEPSV = true
		s.pasvHost = "10,255,255,1"
	})
	ftpFS = dialInProcess(t, srv)
	require.NoError(t, ftpFS.WriteAll(t.Context(), "/a.txt", []byte("a"), 0))
	require.NoError(t, ftpFS.WriteAll(t.Context(), "/b.txt", []byte("b"), 0))
	assert.Equal(t, 1, srv.count("EPSV"), "EPSV is not tried again after it failed")
	assert.Equal(t, 2, srv.count("PASV"))
	assert.Equal(t, "b", string(srv.fileContent("/b.txt")))
}

// TestFTPS_SessionReuse verifies that FTPS data connections resume the TLS
// session of the control connection, which servers like vsftpd and FileZilla
// Server require, also with a TLSConfig of the caller without a session cache.
func TestFTPS_SessionReuse(t *testing.T) {
	srv := newTestFTPServer(t)
	srv.configure(func(s *testFTPServer) { s.requireTLSReuse = true })
	address := fmt.Sprintf("ftps://%s@%s", testUser, srv.addr())
	for _, opts := range []*Options{
		{Password: testPassword, InsecureSkipVerify: true},
		{Password: testPassword, TLSConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}, //#nosec G402 -- test server with a self-signed certificate
	} {
		ftpFS, err := Dial(t.Context(), address, opts)
		require.NoError(t, err)
		f := ftpFS.(*fileSystem)
		data := []byte("secure data")
		require.NoError(t, f.WriteAll(t.Context(), "/secure.txt", data, 0))
		got, err := f.ReadAll(t.Context(), "/secure.txt")
		require.NoError(t, err)
		assert.Equal(t, data, got)
		if opts.TLSConfig != nil {
			assert.Nil(t, opts.TLSConfig.ClientSessionCache, "the TLSConfig of the caller must not be modified")
		}
		require.NoError(t, ftpFS.Close())
	}
}

// TestFTPS_VerifiesCertificate verifies that the self-signed certificate
// of the test server is rejected without InsecureSkipVerify.
func TestFTPS_VerifiesCertificate(t *testing.T) {
	srv := newTestFTPServer(t)
	_, err := Dial(t.Context(), fmt.Sprintf("ftps://%s@%s", testUser, srv.addr()), &Options{Password: testPassword})
	require.Error(t, err)
}

// TestDebugOut verifies that the protocol log doesn't contain the password.
func TestDebugOut(t *testing.T) {
	srv := newTestFTPServer(t)
	var log bytes.Buffer
	ftpFS := dialInProcessWith(t, srv, &Options{Password: "S3cret", DebugOut: &log})
	_, err := ftpFS.Stat("/")
	require.NoError(t, err)
	assert.Contains(t, log.String(), "USER testuser")
	assert.Contains(t, log.String(), "PASS ****")
	assert.NotContains(t, log.String(), "S3cret")
	_, pass := srv.credentials()
	assert.Equal(t, "S3cret", pass)
}

// Test_debugWriter checks the redaction of PASS commands,
// also when a command is split over several writes.
func Test_debugWriter(t *testing.T) {
	var out bytes.Buffer
	w := &debugWriter{w: &out}
	for _, p := range []string{"USER u\r\n", "pass S3", "cret\r\n", "331 password required\r\n", "PASS a\r\nNOOP\r\n"} {
		n, err := w.Write([]byte(p))
		require.NoError(t, err)
		require.Equal(t, len(p), n)
	}
	assert.Equal(t, "USER u\r\nPASS ****\r\n331 password required\r\nPASS ****\r\nNOOP\r\n", out.String())
}

// TestAppend_NotRetriedAfterSend verifies that an APPE whose reply was
// lost is not repeated on a new connection. The data was appended twice.
func TestAppend_NotRetriedAfterSend(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/log.txt", "A")
	srv.configure(func(s *testFTPServer) { s.dropReplyOf["APPE"] = 1 })

	err := ftpFS.Append(t.Context(), "/log.txt", []byte("B"), 0)
	require.Error(t, err, "the reply of APPE was lost")
	assert.Contains(t, err.Error(), "may have been executed")
	assert.Equal(t, "AB", string(srv.fileContent("/log.txt")), "APPE must not run twice")

	// OpenAppendWriter commits with the same Append
	srv.configure(func(s *testFTPServer) { s.dropReplyOf["APPE"] = 1 })
	w, err := ftpFS.OpenAppendWriter("/log.txt", 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("C"))
	require.NoError(t, err)
	require.Error(t, w.Close())
	assert.Equal(t, "ABC", string(srv.fileContent("/log.txt")))

	// The next operation reconnects
	require.NoError(t, ftpFS.Append(t.Context(), "/log.txt", []byte("D"), 0))
	assert.Equal(t, "ABCD", string(srv.fileContent("/log.txt")))
}

// TestAppend_StaleConnection verifies that an APPE on a connection that
// the server closed while it was idle is sent once on a new connection.
func TestAppend_StaleConnection(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/log.txt", "A")
	// The NOOP that checks the idle connection finds it closed
	srv.configure(func(s *testFTPServer) { s.dropBefore["NOOP"] = 1 })

	require.NoError(t, ftpFS.Append(t.Context(), "/log.txt", []byte("B"), 0))
	assert.Equal(t, "AB", string(srv.fileContent("/log.txt")))
	assert.Equal(t, int64(2), srv.logins.Load(), "one reconnect")
	assert.Equal(t, 1, srv.count("APPE"))
}

// TestRetry_Idempotent verifies that idempotent operations are retried
// once on a new connection after a connection loss, and that the
// Credentials callback is called again for the reconnect.
func TestRetry_Idempotent(t *testing.T) {
	srv := newTestFTPServer(t)
	var credentialCalls atomic.Int64
	ftpFS := dialInProcessWith(t, srv, &Options{Credentials: func(*url.URL) (string, string, error) {
		credentialCalls.Add(1)
		return testUser, testPassword, nil
	}})
	assert.Equal(t, int64(1), credentialCalls.Load())
	srv.writeFile("/file.txt", "content")

	srv.configure(func(s *testFTPServer) { s.dropBefore["MLST"] = 1 })
	info, err := ftpFS.Stat("/file.txt")
	require.NoError(t, err, "Stat on a lost connection is retried")
	assert.Equal(t, int64(7), info.Size)
	assert.Equal(t, int64(2), credentialCalls.Load(), "Credentials is called for the reconnect")

	srv.configure(func(s *testFTPServer) { s.dropReplyOf["RETR"] = 1 })
	got, err := ftpFS.ReadAll(t.Context(), "/file.txt")
	require.NoError(t, err, "ReadAll whose transfer reply was lost is retried")
	assert.Equal(t, "content", string(got))
	assert.Equal(t, int64(3), srv.logins.Load())

	// A connection that breaks again on the retry is an error
	srv.configure(func(s *testFTPServer) { s.dropBefore["MLST"] = 2 })
	_, err = ftpFS.Stat("/file.txt")
	require.Error(t, err, "the retry broke too")
	assert.Equal(t, int64(4), srv.logins.Load(), "only one retry")

	// Failing credentials are a reconnect error
	failing := dialInProcessWith(t, srv, &Options{Credentials: func(*url.URL) (string, string, error) {
		if credentialCalls.Add(1) > 10 {
			return "", "", errors.New("no credentials")
		}
		return testUser, testPassword, nil
	}})
	credentialCalls.Store(100)
	srv.configure(func(s *testFTPServer) { s.dropBefore["MLST"] = 1 })
	_, err = failing.Stat("/file.txt")
	require.ErrorContains(t, err, "no credentials")
}

// TestMutation_ReplyLost verifies that Remove, MakeDir and Move don't report
// os.ErrNotExist or os.ErrExist for a command that the server executed but
// whose reply was lost: they are not retried.
func TestMutation_ReplyLost(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/remove.txt", "x")
	srv.writeFile("/move.txt", "x")

	for _, check := range []struct {
		verb string
		op   func() error
	}{
		{"DELE", func() error { return ftpFS.Remove("/remove.txt") }},
		{"MKD", func() error { return ftpFS.MakeDir("/dir", 0) }},
		{"RNTO", func() error { return ftpFS.Move(t.Context(), "/move.txt", "/moved.txt") }},
	} {
		srv.configure(func(s *testFTPServer) { s.dropReplyOf[check.verb] = 1 })
		err := check.op()
		require.Error(t, err, "the reply of %s was lost", check.verb)
		assert.NotErrorIs(t, err, os.ErrNotExist, check.verb)
		assert.NotErrorIs(t, err, os.ErrExist, check.verb)
		assert.Equal(t, 1, srv.count(check.verb), "%s must not be sent again", check.verb)
	}
	assert.False(t, srv.exists("/remove.txt"))
	assert.True(t, srv.exists("/dir"))
	assert.True(t, srv.exists("/moved.txt"))
}

// TestContext_AbortsOperation verifies that a done context aborts an
// operation that waits for a reply, and that the file system reconnects
// for the next operation.
func TestContext_AbortsOperation(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.configure(func(s *testFTPServer) { s.hang = "MLSD" })

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := returnsWithin(t, 3*time.Second, func() error {
		return ftpFS.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	srv.configure(func(s *testFTPServer) { s.hang = "" })
	require.NoError(t, ftpFS.ListDir(t.Context(), "/", nil, func(*fs.FileInfo) error { return nil }))
	assert.Equal(t, int64(2), srv.logins.Load(), "the aborted connection is replaced")

	// A canceled context fails before anything is sent
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, ftpFS.Move(canceled, "/a", "/b"), context.Canceled)
	assert.ErrorIs(t, ftpFS.Move(canceled, "/a", "/a"), context.Canceled)
	assert.Zero(t, srv.count("RNFR"))
}

// TestClose_AbortsOperation verifies that Close doesn't wait for a
// running operation on a hung server and that the operation returns
// fs.ErrFileSystemClosed, and that an operation that waits for the
// connection meanwhile returns too.
func TestClose_AbortsOperation(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/file.txt", "x")
	srv.configure(func(s *testFTPServer) { s.hang = "MLST" })

	hung := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := ftpFS.Stat("/file.txt")
			hung <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)

	// Operations that don't need the connection don't wait for it
	err := returnsWithin(t, time.Second, func() error {
		w, err := ftpFS.OpenWriter("/new.txt", 0)
		if err == nil {
			err = w.(fs.WriteAborter).Abort()
		}
		return err
	})
	require.NoError(t, err)

	require.NoError(t, returnsWithin(t, time.Second, ftpFS.Close))
	for range 2 {
		select {
		case err := <-hung:
			assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
		case <-time.After(3 * time.Second):
			t.Fatal("Stat did not return after Close")
		}
	}
}

// TestClose_AbortsReconnect verifies that Close aborts the reconnect
// of an operation to a server that doesn't answer.
func TestClose_AbortsReconnect(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcessWith(t, srv, &Options{Password: testPassword, Timeout: time.Minute})
	srv.configure(func(s *testFTPServer) {
		s.dropBefore["MLST"] = 1
		s.silent = true
	})
	hung := make(chan error, 1)
	go func() {
		_, err := ftpFS.Stat("/file.txt")
		hung <- err
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, returnsWithin(t, time.Second, ftpFS.Close))
	select {
	case err := <-hung:
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	case <-time.After(3 * time.Second):
		t.Fatal("Stat did not return after Close")
	}
}

// TestClose_AbortsReader verifies that Close aborts an open reader.
func TestClose_AbortsReader(t *testing.T) {
	srv := newTestFTPServer(t)
	ftpFS := dialInProcess(t, srv)
	srv.writeFile("/file.txt", "content")
	r, err := ftpFS.OpenReader("/file.txt")
	require.NoError(t, err)
	require.NoError(t, ftpFS.Close())
	// The abort runs in the background
	for !r.(*fileReader).conn.isAborted() {
		time.Sleep(time.Millisecond)
	}
	_, err = io.ReadAll(r)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "reading after Close of the file system")
	assert.NoError(t, r.Close())

	srv = newTestFTPServer(t)
	ftpFS = dialInProcess(t, srv)
	srv.mkdirAll("/dir")
	_, err = ftpFS.OpenReader("/dir")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	_, err = ftpFS.OpenReader("/missing.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// Test_isConnectionError checks the classification of errors.
func Test_isConnectionError(t *testing.T) {
	assert.False(t, isConnectionError(nil))
	assert.False(t, isConnectionError(context.Canceled))
	assert.False(t, isConnectionError(fmt.Errorf("dial: %w", context.DeadlineExceeded)))
	assert.False(t, isConnectionError(&textproto.Error{Code: 550, Msg: "not found"}))
	assert.True(t, isConnectionError(&textproto.Error{Code: 421, Msg: "timeout"}))
	assert.True(t, isConnectionError(io.EOF))
	assert.True(t, isConnectionError(fmt.Errorf("read: %w", net.ErrClosed)))
	assert.True(t, isConnectionError(os.ErrDeadlineExceeded))
}
