package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"

	"github.com/cloudsoda/go-smb2"

	"github.com/ungerik/go-fs"
)

// NTSTATUS codes of SMB responses, see [MS-ERREF]
const (
	statusFileIsADirectory      = 0xC00000BA
	statusDirectoryNotEmpty     = 0xC0000101
	statusNotADirectory         = 0xC0000103
	statusUserSessionDeleted    = 0xC0000203
	statusNetworkSessionExpired = 0xC000035C
)

// session is a mounted share on its own TCP connection.
type session struct {
	conn    *trackedConn
	session *smb2.Session
	share   *smb2.Share
}

// trackedConn is the TCP connection of a session. It records when a read
// or write failed: the client library reads all the time in the background,
// so a connection that the server closed, for example after the idle timeout
// of Windows servers, is known to be broken before the next request is sent.
type trackedConn struct {
	net.Conn
	broken atomic.Bool
}

func (c *trackedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.broken.Store(true)
	}
	return n, err
}

func (c *trackedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err != nil {
		c.broken.Store(true)
	}
	return n, err
}

// dial dials a TCP connection, sets up an SMB session and mounts the share.
// The context and Options.Timeout limit all of it.
func dial(ctx context.Context, cfg *dialConfig) (*session, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	netConn, err := cfg.dialContext(ctx, "tcp", cfg.hostPort)
	if err != nil {
		return nil, fmt.Errorf("smbfs: dialing %s: %w", cfg.prefix, err)
	}
	conn := &trackedConn{Conn: netConn}
	smbSession, err := (&smb2.Dialer{Initiator: cfg.initiator()}).DialConn(ctx, conn, cfg.hostPort)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("smbfs: session setup for %s: %w", cfg.prefix, err), conn.Close())
	}
	share, err := smbSession.WithContext(ctx).Mount(cfg.share)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("smbfs: mounting %s: %w", cfg.prefix, err), conn.Close())
	}
	return &session{conn: conn, session: smbSession, share: share}, nil
}

// close unmounts the share, logs off and closes the connection.
// A broken connection is only closed.
func (s *session) close(ctx context.Context) error {
	if s.conn.broken.Load() {
		_ = s.conn.Close() // may be closed already
		return nil
	}
	err := errors.Join(s.share.WithContext(ctx).Umount(), s.session.WithContext(ctx).Logoff())
	_ = s.conn.Close() // Logoff already closes the connection
	return err
}

// isConnectionError reports whether err indicates a connection loss.
// Context errors are not connection errors.
func isConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[*smb2.TransportError](err); ok {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	_, ok := errors.AsType[net.Error](err)
	return ok
}

// isSessionExpired reports whether the server rejected a request because
// the session expired or was deleted. The server didn't execute the
// request then, so it can be sent again on a new session.
func isSessionExpired(err error) bool {
	code := ntStatus(err)
	return code == statusNetworkSessionExpired || code == statusUserSessionDeleted
}

// ntStatus returns the NTSTATUS code of a response error or 0.
func ntStatus(err error) uint32 {
	if e, ok := errors.AsType[*smb2.ResponseError](err); ok {
		return e.Code
	}
	return 0
}

// dialCall is a reconnect in progress that other operations wait for.
type dialCall struct {
	done chan struct{}
	err  error
}

// session returns the current session, after dialing a new one if the
// connection of the current session broke. Concurrent callers share one
// reconnect; a caller waiting for it honours its own context.
func (f *fileSystem) session(ctx context.Context) (*session, error) {
	for {
		f.mtx.Lock()
		if f.closed.Load() {
			f.mtx.Unlock()
			return nil, fs.ErrFileSystemClosed
		}
		current := f.sess
		if current != nil && !current.conn.broken.Load() {
			f.mtx.Unlock()
			return current, nil
		}
		call := f.dialing
		if call == nil {
			call = &dialCall{done: make(chan struct{})}
			f.dialing = call
			f.mtx.Unlock()
			return f.reconnect(ctx, call, current)
		}
		f.mtx.Unlock()

		select {
		case <-call.done:
			// Another caller's context ended its dial, try again with this one
			if call.err != nil && !errors.Is(call.err, context.Canceled) && !errors.Is(call.err, context.DeadlineExceeded) {
				return nil, call.err
			}
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// reconnect replaces the broken session old with a new one.
func (f *fileSystem) reconnect(ctx context.Context, call *dialCall, old *session) (s *session, err error) {
	if old != nil {
		_ = old.conn.Close()
	}
	s, err = dial(ctx, f.cfg)
	if ctx.Err() != nil {
		err = errors.Join(err, context.Cause(ctx))
	}

	f.mtx.Lock()
	f.dialing = nil
	if err == nil {
		if f.closed.Load() {
			_ = s.conn.Close()
			s, err = nil, fs.ErrFileSystemClosed
		} else {
			f.sess = s
		}
	}
	call.err = err
	close(call.done)
	f.mtx.Unlock()
	return s, err
}

// drop marks the connection of the session broken,
// so that the next operation dials a new session.
func (f *fileSystem) drop(s *session) {
	s.conn.broken.Store(true)
	_ = s.conn.Close()
}

// do runs op with the share of the current session and the context of the
// operation. If the session's connection is broken before op is called, a
// new session is dialed first. If op fails because the connection broke,
// it is retried once on a new session if it is idempotent, so that an
// operation like Remove or Move whose request may have been executed
// doesn't run twice. A request that the server rejected because the
// session expired is always retried.
func (f *fileSystem) do(ctx context.Context, idempotent bool, op func(share *smb2.Share) error) error {
	return f.run(ctx, idempotent, false, op)
}

// doHandle is like do for an op that opens a file handle. The share
// passed to op has the context of the file system instead of the context
// of the operation, because the handle uses it for all its requests,
// so that Close of the file system aborts them.
func (f *fileSystem) doHandle(idempotent bool, op func(share *smb2.Share) error) error {
	return f.run(context.Background(), idempotent, true, op)
}

func (f *fileSystem) run(ctx context.Context, idempotent, handle bool, op func(share *smb2.Share) error) error {
	if f.closed.Load() {
		return fs.ErrFileSystemClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := f.opContext(ctx)
	defer cancel()

	for attempt := 0; ; attempt++ {
		s, err := f.session(ctx)
		if err != nil {
			return err
		}
		shareCtx := ctx
		if handle {
			shareCtx = f.closing
		}
		err = op(s.share.WithContext(shareCtx))
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return f.ctxErr(ctx, err)
		case isSessionExpired(err):
			f.drop(s)
		case isConnectionError(err):
			f.drop(s)
			if !idempotent {
				return fmt.Errorf("smbfs: the connection to %s broke after a request was sent, it may have been executed: %w", f.URIPrefix, err)
			}
		default:
			return err
		}
		if attempt > 0 {
			return err
		}
	}
}

// ctxErr returns the cause of the done ctx of an operation that failed
// with err: fs.ErrFileSystemClosed after Close, else the context error.
func (f *fileSystem) ctxErr(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if errors.Is(err, cause) {
		return err
	}
	return fmt.Errorf("%w: %w", cause, err)
}
