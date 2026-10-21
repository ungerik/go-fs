package smbfs

import (
	"context"
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
}

type testConn struct {
	net.Conn
	closeAfterWrite atomic.Bool
}

// Write closes the connection after the write if closeAfterWrite is set,
// so a request reaches the server but its response never arrives.
func (c *testConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if c.closeAfterWrite.CompareAndSwap(true, false) {
		_ = c.Conn.Close()
	}
	return n, err
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
// is retried on a new session, and that Remove, MakeDir and Move are not:
// they don't report os.ErrNotExist or os.ErrExist for a request that the
// server may have executed.
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

	for _, check := range []struct {
		name string
		op   func() error
	}{
		{"MakeDir", func() error { return f.MakeDir(dir+"/sub", 0) }},
		{"Remove", func() error { return f.Remove(dir + "/file.txt") }},
		{"Move", func() error { return f.Move(ctx, dir+"/moved-from.txt", dir+"/moved-to.txt") }},
	} {
		if check.name == "Move" {
			require.NoError(t, f.WriteAll(ctx, dir+"/moved-from.txt", []byte("x"), 0))
		}
		// Reconnect after the previous check broke the connection
		_, err := f.Stat(dir)
		require.NoError(t, err)
		dials := dialer.dials.Load()
		dialer.last().closeAfterWrite.Store(true)
		err = check.op()
		require.Error(t, err, "%s whose response was lost", check.name)
		assert.Contains(t, err.Error(), "may have been executed", check.name)
		assert.NotErrorIs(t, err, os.ErrExist, check.name)
		assert.NotErrorIs(t, err, os.ErrNotExist, check.name)
		assert.Equal(t, dials, dialer.dials.Load(), "%s must not be retried", check.name)
	}
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
