package smbfs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudsoda/go-smb2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
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

// TestDial_Context verifies that the context and Options.Timeout
// limit the SMB negotiation and authentication, not only the TCP connect.
func TestDial_Context(t *testing.T) {
	addr := silentListener(t)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := returnsWithin(t, 3*time.Second, func() error {
		_, err := Dial(ctx, "smb://u:S3cret@"+addr+"/share", nil)
		return err
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotContains(t, err.Error(), "S3cret")

	err = returnsWithin(t, 3*time.Second, func() error {
		_, err := Dial(t.Context(), "smb://u@"+addr+"/share", &Options{Password: "S3cret", Timeout: 200 * time.Millisecond})
		return err
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestDialContext_Error verifies that an error of Options.DialContext is returned.
func TestDialContext_Error(t *testing.T) {
	dialErr := errors.New("no route")
	_, err := Dial(t.Context(), "smb://host/share", &Options{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, dialErr },
	})
	require.ErrorIs(t, err, dialErr)
}

// Test_errorClassification checks which errors are connection losses
// and expired sessions.
func Test_errorClassification(t *testing.T) {
	assert.False(t, isConnectionError(nil))
	assert.False(t, isConnectionError(context.Canceled))
	assert.False(t, isConnectionError(&os.PathError{Op: "open", Path: "x", Err: context.DeadlineExceeded}))
	assert.False(t, isConnectionError(os.ErrNotExist))
	assert.True(t, isConnectionError(&os.PathError{Op: "open", Path: "x", Err: &smb2.TransportError{Err: io.EOF}}))
	assert.True(t, isConnectionError(net.ErrClosed))
	assert.True(t, isConnectionError(io.ErrUnexpectedEOF))

	assert.True(t, isSessionExpired(&smb2.ResponseError{Code: statusNetworkSessionExpired}))
	assert.True(t, isSessionExpired(fmt.Errorf("stat: %w", &smb2.ResponseError{Code: statusUserSessionDeleted})))
	assert.False(t, isSessionExpired(&smb2.ResponseError{Code: statusDirectoryNotEmpty}))
	assert.False(t, isSessionExpired(errors.New("other")))
	assert.Zero(t, ntStatus(errors.New("other")))
}

// testDialer is an Options.DialContext that records the connections
// it dials and can break them like a server or network would.
type testDialer struct {
	dials atomic.Int64
	mu    sync.Mutex
	conns []*testConn
	block atomic.Bool // block dials until their context is done, like a hung server

	// afterDial is called after a TCP connection was dialed,
	// before the SMB session is set up on it
	afterDial func()
}

// smb2SetInfo is the SMB2 command code of SET_INFO requests, which rename files.
const smb2SetInfo = 0x11

type testConn struct {
	net.Conn
	closeAfterWrite   atomic.Bool
	closeAfterCommand atomic.Uint32 // an SMB2 command code, 0 (NEGOTIATE) for none
}

// Write closes the connection after the write if closeAfterWrite is set,
// so the request is not sent completely, or after the write of an SMB2
// request with the command closeAfterCommand, so the request reaches the
// server but its response never arrives. go-smb2 writes the length prefix
// of a request and the request itself with two writes.
func (c *testConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if c.closeAfterWrite.CompareAndSwap(true, false) || c.wroteCommand(b) {
		_ = c.Conn.Close()
	}
	return n, err
}

// wroteCommand reports whether b is an SMB2 request with the command
// closeAfterCommand and resets closeAfterCommand if it is.
// The command code follows the protocol ID and 8 bytes of the header.
func (c *testConn) wroteCommand(b []byte) bool {
	command := c.closeAfterCommand.Load()
	if command == 0 || len(b) < 14 || string(b[:4]) != "\xfeSMB" || uint32(binary.LittleEndian.Uint16(b[12:14])) != command {
		return false
	}
	return c.closeAfterCommand.CompareAndSwap(command, 0)
}

func (d *testDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.dials.Add(1)
	if d.block.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var dialer net.Dialer
	netConn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c := &testConn{Conn: netConn}
	d.mu.Lock()
	d.conns = append(d.conns, c)
	d.mu.Unlock()
	if d.afterDial != nil {
		d.afterDial()
	}
	return c, nil
}

// last returns the last dialed connection.
func (d *testDialer) last() *testConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[len(d.conns)-1]
}

// dialTestFS dials the Samba test share with a testDialer.
func dialTestFS(t *testing.T) (*fileSystem, *testDialer) {
	t.Helper()
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	dialer := new(testDialer)
	smbFS, err := Dial(t.Context(), testSMBAddress, &Options{
		Username:    testUsername,
		Password:    testPassword,
		DialContext: dialer.DialContext,
	})
	require.NoError(t, err)
	t.Cleanup(func() { smbFS.Close() })
	return smbFS.(*fileSystem), dialer
}

// breakConnection closes the connection of the current session and waits
// until the client library noticed it, like after a server closed an
// idle session.
func breakConnection(t *testing.T, f *fileSystem, dialer *testDialer) {
	t.Helper()
	require.NoError(t, dialer.last().Conn.Close())
	f.mtx.Lock()
	s := f.sess
	f.mtx.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for !s.conn.broken.Load() {
		require.True(t, time.Now().Before(deadline), "the broken connection was not noticed")
		time.Sleep(time.Millisecond)
	}
}

// TestReconnect verifies that operations dial a new session when the
// connection of the session broke while it was idle, also operations
// that are not retried, and that concurrent operations share one reconnect.
// The registered file system failed permanently after Windows servers
// disconnected idle sessions.
func TestReconnect(t *testing.T) {
	f, dialer := dialTestFS(t)
	dir := fmt.Sprintf("/reconnect-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), dir) })

	breakConnection(t, f, dialer)
	require.NoError(t, f.MakeDir(dir, 0), "MakeDir on a new session")
	assert.Equal(t, int64(2), dialer.dials.Load())

	breakConnection(t, f, dialer)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := f.Stat(dir)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, int64(3), dialer.dials.Load(), "concurrent operations share one reconnect")
}

// TestRetry verifies that an idempotent operation whose response was lost
// is retried on a new session, and that Remove, MakeDir and Move are not
// once they sent the request that changes the share: they don't report
// os.ErrNotExist or os.ErrExist for a request that the server may have
// executed. Move is retried if the connection broke during its checks,
// which change nothing; it returned "may have been executed" for them.
func TestRetry(t *testing.T) {
	f, dialer := dialTestFS(t)
	ctx := t.Context()
	dir := fmt.Sprintf("/retry-%d", time.Now().UnixNano())
	require.NoError(t, f.MakeDir(dir, 0))
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), dir) })
	require.NoError(t, f.WriteAll(ctx, dir+"/file.txt", []byte("content"), 0))

	dialer.last().closeAfterWrite.Store(true)
	data, err := f.ReadAll(ctx, dir+"/file.txt")
	require.NoError(t, err, "ReadAll is retried")
	assert.Equal(t, "content", string(data))
	assert.Equal(t, int64(2), dialer.dials.Load())

	// The first request of Move is the Lstat of the source
	require.NoError(t, f.WriteAll(ctx, dir+"/checked-from.txt", []byte("x"), 0))
	dialer.last().closeAfterWrite.Store(true)
	require.NoError(t, f.Move(ctx, dir+"/checked-from.txt", dir+"/checked-to.txt"), "Move is retried after its checks")
	assert.Equal(t, int64(3), dialer.dials.Load())
	_, err = f.Stat(dir + "/checked-to.txt")
	require.NoError(t, err, "moved")

	for _, check := range []struct {
		name  string
		op    func() error
		setup func(c *testConn)
	}{
		{
			name:  "MakeDir",
			op:    func() error { return f.MakeDir(dir+"/sub", 0) },
			setup: func(c *testConn) { c.closeAfterWrite.Store(true) },
		},
		{
			name:  "Remove",
			op:    func() error { return f.Remove(dir + "/file.txt") },
			setup: func(c *testConn) { c.closeAfterWrite.Store(true) },
		},
		{
			// The rename is a SET_INFO request after the checks
			name:  "Move",
			op:    func() error { return f.Move(ctx, dir+"/moved-from.txt", dir+"/moved-to.txt") },
			setup: func(c *testConn) { c.closeAfterCommand.Store(smb2SetInfo) },
		},
	} {
		if check.name == "Move" {
			require.NoError(t, f.WriteAll(ctx, dir+"/moved-from.txt", []byte("x"), 0))
		}
		// Reconnect after the previous check broke the connection
		_, err := f.Stat(dir)
		require.NoError(t, err)
		dials := dialer.dials.Load()
		check.setup(dialer.last())
		err = check.op()
		require.Error(t, err, "%s whose response was lost", check.name)
		assert.Contains(t, err.Error(), "may have been executed", check.name)
		assert.NotErrorIs(t, err, os.ErrExist, check.name)
		assert.NotErrorIs(t, err, os.ErrNotExist, check.name)
		assert.Equal(t, dials, dialer.dials.Load(), "%s must not be retried", check.name)
	}
}

// TestRetry_SessionExpired verifies that a request that the server
// rejected because the session expired is sent again on a new session,
// also after the op called noRetry: the server didn't execute it.
func TestRetry_SessionExpired(t *testing.T) {
	f, dialer := dialTestFS(t)
	for _, final := range []bool{false, true} {
		dials := dialer.dials.Load()
		calls := 0
		err := f.run(t.Context(), false, func(share *smb2.Share, noRetry func()) error {
			calls++
			if final {
				noRetry()
			}
			if calls == 1 {
				return &os.PathError{Op: "stat", Path: "x", Err: &smb2.ResponseError{Code: statusNetworkSessionExpired}}
			}
			_, err := share.Stat("")
			return err
		})
		require.NoError(t, err, "noRetry called: %t", final)
		assert.Equal(t, 2, calls, "noRetry called: %t", final)
		assert.Equal(t, dials+1, dialer.dials.Load(), "a new session, noRetry called: %t", final)
	}
	assert.Equal(t, int64(3), dialer.dials.Load())
}

// TestReconnect_DialTimeout verifies that operations waiting for the
// reconnect of another operation return its error if the dial timed out
// after Options.Timeout. They took that for the end of the context of the
// other operation and dialed again one after another, so the k-th
// operation returned after k times the timeout.
func TestReconnect_DialTimeout(t *testing.T) {
	const timeout = 300 * time.Millisecond
	dialer := new(testDialer)
	dialer.block.Store(true)
	cfg, err := parseAddress("smb://host/share", &Options{Timeout: timeout, DialContext: dialer.DialContext})
	require.NoError(t, err)
	// A file system without a session dials one for the first operation
	f := &fileSystem{PathHelper: fsimpl.PathHelper{URIPrefix: cfg.prefix, Rooted: true}, cfg: cfg}
	f.closing, f.cancel = context.WithCancelCause(t.Context())
	t.Cleanup(func() { _ = f.Close() })

	const operations = 4
	start := time.Now()
	var wg sync.WaitGroup
	for range operations {
		wg.Go(func() {
			_, err := f.Stat("/")
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	assert.Equal(t, int64(1), dialer.dials.Load(), "the operations share one dial")
	// One timeout and a generous allowance for slow test machines,
	// far less than the timeout for each of the operations
	assert.Less(t, elapsed, timeout*operations*2/3, "the operations wait for one dial")
}

// lateCtx is a context that reports that it ended once ended is set,
// without closing its Done channel, so a dial that started completes.
// It ends the context of an operation right after its dial succeeded.
type lateCtx struct {
	context.Context
	ended atomic.Bool
}

func (c *lateCtx) Err() error {
	if c.ended.Load() {
		return context.Canceled
	}
	return c.Context.Err()
}

// TestReconnect_ContextEndsAfterDial verifies that a session that was
// dialed for an operation whose context ended right after the dial
// is kept for the next operations. It was neither kept nor closed,
// which leaked its connection and the reader goroutine of go-smb2.
func TestReconnect_ContextEndsAfterDial(t *testing.T) {
	f, dialer := dialTestFS(t)
	breakConnection(t, f, dialer)
	ctx := &lateCtx{Context: t.Context()}
	dialer.afterDial = func() { ctx.ended.Store(true) }

	s, err := f.session(ctx)
	require.NoError(t, err, "the session was dialed")
	dialer.afterDial = nil
	f.mtx.Lock()
	current := f.sess
	f.mtx.Unlock()
	assert.Same(t, s, current, "the dialed session is kept")
	_, err = f.Stat("/")
	require.NoError(t, err)
	assert.Equal(t, int64(2), dialer.dials.Load(), "Stat uses the kept session")
}

// TestClose_AbortsReconnect verifies that Close aborts an operation
// that dials a new session to a server that doesn't answer.
func TestClose_AbortsReconnect(t *testing.T) {
	f, dialer := dialTestFS(t)
	breakConnection(t, f, dialer)
	dialer.block.Store(true)

	hung := make(chan error, 1)
	go func() {
		_, err := f.Stat("/")
		hung <- err
	}()
	for dialer.dials.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	require.NoError(t, returnsWithin(t, time.Second, f.Close))
	select {
	case err := <-hung:
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	case <-time.After(3 * time.Second):
		t.Fatal("Stat did not return after Close")
	}
}
