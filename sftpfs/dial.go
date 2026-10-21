package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// posixRenameExtension is the name of the OpenSSH extension
// for a rename that replaces an existing file atomically.
const posixRenameExtension = "posix-rename@openssh.com"

// Dial connects to the SFTP server at address
// and returns the file system without registering it.
//
// The address is a host name or IP address with an optional username
// and port, with or without the scheme "sftp://": "host", "user@host:2222",
// "sftp://user@host". IPv6 addresses are written in brackets: "[::1]",
// "sftp://user@[::1]:2222". The port defaults to 22. A username or
// password in the address is used if opts has none, see Options.
// A path, query or fragment of the address is ignored.
//
// The URI prefix of the file system is "sftp://user@host",
// with ":port" if the port is not 22. URIs with ":22" resolve too.
//
// opts.HostKeyCallback is required. ctx limits the dial with its retries,
// reconnects after a lost connection are limited by opts.Timeout.
// Errors and log messages never contain the password.
func Dial(ctx context.Context, address string, opts *Options) (fs.FileSystem, error) {
	f, creds, err := newFileSystem(address, opts)
	if err != nil {
		return nil, err
	}
	err = f.dial(ctx, creds)
	if err != nil {
		f.cancel()
		return nil, err
	}
	return f, nil
}

// DialAndRegister dials like Dial and registers the file system,
// so that File URIs with its prefix use it.
//
// It returns an error wrapping fs.ErrAlreadyRegistered if another
// file system is registered with the prefix. To close the file system
// later, get it from the registry, for example with
// fs.GetFileSystemByPrefixOrNil("sftp://user@host").Close().
// Use EnsureRegistered to share a connection between independent callers.
func DialAndRegister(ctx context.Context, address string, opts *Options) error {
	fileSystem, err := Dial(ctx, address, opts)
	if err != nil {
		return err
	}
	err = fs.Register(fileSystem)
	if err != nil {
		return errors.Join(err, fileSystem.Close())
	}
	return nil
}

// EnsureRegistered makes sure that a file system with the URI prefix
// of address is registered, dialing it like Dial if necessary,
// and returns a function to free it again.
//
// Concurrent calls for the same prefix share one dial.
// Every successful call must be paired with a call of free; the free
// call that releases the last reference closes the file system if it
// was dialed by EnsureRegistered. A file system that was registered
// otherwise, for example by DialAndRegister, is returned as it is,
// with the options it was dialed with, and never closed by free.
// free is safe to call more than once and never nil, also on error.
//
// URIs with credentials, like "sftp://user:password@host/path",
// can be passed as address. The password is used to dial,
// but it is not part of the prefix "sftp://user@host" of the file system.
func EnsureRegistered(ctx context.Context, address string, opts *Options) (free func() error, err error) {
	f, creds, err := newFileSystem(address, opts)
	if err != nil {
		return nop, err
	}
	dialed := false // create runs in this goroutine
	_, free, err = fs.EnsureRegistered(ctx, f.Prefix(), func(ctx context.Context) (fs.FileSystem, error) {
		dialed = true
		if err := f.dial(ctx, creds); err != nil {
			return nil, err
		}
		return f, nil
	})
	if err != nil || !dialed {
		// f is not used: the dial failed or the prefix was registered already
		f.cancel()
	}
	if err != nil {
		return nop, err
	}
	return free, nil
}

func nop() error { return nil }

// target is the parsed address of a file system.
type target struct {
	url         *url.URL // the address as URL with the scheme "sftp"
	hostPort    string   // the address to dial, with port
	prefixHost  string   // host for the URI prefix, without the default port
	defaultPort bool     // the port is 22
}

// parseAddress parses the address argument of Dial.
func parseAddress(address string) (*target, error) {
	if !strings.Contains(address, "://") {
		address = Prefix + address
	}
	u, err := url.Parse(address)
	if err != nil {
		// A *url.Error repeats the address with the password
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("invalid SFTP address %s: %w", redactAddress(address), err)
	}
	if u.Scheme != "sftp" {
		return nil, fmt.Errorf("not an SFTP address: %s", u.Redacted())
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("no host in SFTP address: %s", u.Redacted())
	}
	t := &target{url: u, prefixHost: u.Host}
	port := u.Port()
	switch port {
	case "":
		port = "22"
		t.prefixHost = strings.TrimSuffix(u.Host, ":") // "host:" has an empty port
		t.defaultPort = true
	case "22":
		t.prefixHost = strings.TrimSuffix(u.Host, ":22")
		t.defaultPort = true
	}
	// u.Hostname has no brackets and no escapes, JoinHostPort adds the brackets
	t.hostPort = net.JoinHostPort(u.Hostname(), port)
	// url.Parse unescaped the zone of an IPv6 address
	// like "[fe80::1%25eth0]", the URI prefix needs the escape
	t.prefixHost = strings.ReplaceAll(t.prefixHost, "%", "%25")
	return t, nil
}

// redactAddress returns address for errors and log messages
// with the password replaced by "xxxxx", even if address is
// not a valid URL; then the whole user info is replaced.
func redactAddress(address string) string {
	if u, err := url.Parse(address); err == nil {
		return u.Redacted()
	}
	scheme, rest, found := strings.Cut(address, "://")
	if !found {
		scheme, rest = "", address
	}
	authorityLen := strings.IndexByte(rest, '/')
	if authorityLen < 0 {
		authorityLen = len(rest)
	}
	at := strings.LastIndexByte(rest[:authorityLen], '@')
	if at < 0 {
		return address
	}
	rest = "xxxxx" + rest[at:]
	if found {
		return scheme + "://" + rest
	}
	return rest
}

// credentials are the username and password for a dial.
type credentials struct {
	username, password string
}

// credentials returns the username and password to dial with:
// a non-empty value of the Credentials callback, of the Options,
// or of the address, in that order.
func (f *fileSystem) credentials() (creds credentials, err error) {
	creds.username, creds.password = f.opts.Username, f.opts.Password
	if creds.username == "" {
		creds.username = f.target.url.User.Username()
	}
	if creds.password == "" {
		creds.password, _ = f.target.url.User.Password()
	}
	if f.opts.Credentials != nil {
		u := *f.target.url // a copy the callback can't modify for later calls
		username, password, err := f.opts.Credentials(&u)
		if err != nil {
			return credentials{}, fmt.Errorf("SFTP credentials for %s: %w", f.target.url.Redacted(), err)
		}
		if username != "" {
			creds.username = username
		}
		if password != "" {
			creds.password = password
		}
	}
	if creds.username == "" {
		return credentials{}, fmt.Errorf("no SFTP username for %s: set Options.Username or put it in the address", f.target.url.Redacted())
	}
	return creds, nil
}

// newFileSystem returns a file system for address that has
// no connection yet, together with the credentials to dial it.
func newFileSystem(address string, opts *Options) (*fileSystem, credentials, error) {
	t, err := parseAddress(address)
	if err != nil {
		return nil, credentials{}, err
	}
	f := &fileSystem{target: t}
	if opts != nil {
		f.opts = *opts
		f.opts.Auth = slices.Clone(opts.Auth)
	}
	if f.opts.HostKeyCallback == nil {
		return nil, credentials{}, fmt.Errorf("sftpfs: Options.HostKeyCallback is required to dial %s, see AcceptAnyHostKey for tests", t.url.Redacted())
	}
	creds, err := f.credentials()
	if err != nil {
		return nil, credentials{}, err
	}
	prefix := Prefix + url.User(creds.username).String() + "@" + t.prefixHost
	f.PathHelper = fsimpl.PathHelper{URIPrefix: prefix, Rooted: true}
	if t.defaultPort {
		// URIs with the default port resolve too
		f.AltPrefixes = []string{prefix + ":22"}
	}
	f.life, f.cancel = context.WithCancel(context.Background())
	return f, creds, nil
}

// dial connects f for the first time.
func (f *fileSystem) dial(ctx context.Context, creds credentials) error {
	c, err := f.connect(ctx, creds)
	if err != nil {
		return err
	}
	f.mtx.Lock()
	f.conn = c
	f.mtx.Unlock()
	f.watch(c)
	return nil
}

// connect dials a connection with up to 1+ConnectRetries attempts
// and exponential backoff between them, as long as ctx is not done.
func (f *fileSystem) connect(ctx context.Context, creds credentials) (*conn, error) {
	retries := f.opts.connectRetries()
	backoff := f.opts.retryBackoff()
	for attempt := 0; ; attempt++ {
		c, err := f.dialOnce(ctx, creds)
		if err == nil {
			f.logf("Dialed SFTP connection to %s", f.Prefix())
			return c, nil
		}
		if attempt >= retries || ctx.Err() != nil || !isConnectionError(err) {
			return nil, fmt.Errorf("SFTP connection to %s: %w", f.Prefix(), err)
		}
		f.logf("Waiting %s before retry %d of %d of SFTP connection to %s: %s", backoff, attempt+1, retries, f.Prefix(), err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("SFTP connection to %s: %w, last attempt: %w", f.Prefix(), ctx.Err(), err)
		case <-timer.C:
		}
		backoff *= 2
	}
}

// dialOnce makes one connection attempt, limited by ctx and Options.Timeout.
func (f *fileSystem) dialOnce(ctx context.Context, creds credentials) (*conn, error) {
	attemptCtx := ctx
	if timeout := f.opts.timeout(); timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	netConn, err := f.opts.dialContext()(attemptCtx, "tcp", f.target.hostPort)
	if err != nil {
		return nil, f.dialErr(ctx, attemptCtx, err)
	}
	// The SSH handshake and the start of the SFTP session take no ctx,
	// closing the network connection when attemptCtx ends aborts them
	stop := context.AfterFunc(attemptCtx, func() { _ = netConn.Close() })
	c, err := f.newConn(netConn, creds)
	if !stop() {
		// attemptCtx ended and closed the network connection
		if err == nil {
			_ = c.close()
		}
		return nil, f.dialErr(ctx, attemptCtx, err)
	}
	if err != nil {
		return nil, f.dialErr(ctx, attemptCtx, err)
	}
	return c, nil
}

// dialErr returns the error of a connection attempt
// with the context ctx and its attempt context.
func (f *fileSystem) dialErr(ctx, attemptCtx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case attemptCtx.Err() != nil:
		// os.ErrDeadlineExceeded is a timeout net.Error that connect retries,
		// unlike a context error that is never taken for a connection error
		return fmt.Errorf("attempt timed out after %s: %w", f.opts.timeout(), os.ErrDeadlineExceeded)
	}
	return err
}

// newConn starts an SSH connection and an SFTP session on netConn.
// netConn is closed if newConn fails.
func (f *fileSystem) newConn(netConn net.Conn, creds credentials) (*conn, error) {
	auth := slices.Clone(f.opts.Auth)
	if creds.password != "" {
		auth = append(auth, ssh.Password(creds.password))
	}
	config := &ssh.ClientConfig{
		User:            creds.username,
		Auth:            auth,
		HostKeyCallback: f.opts.HostKeyCallback,
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, f.target.hostPort, config)
	if err != nil {
		_ = netConn.Close() // the error says more than the close error
		return nil, err
	}
	// The ssh.Client owns sshConn, closing it closes netConn
	sshClient := ssh.NewClient(sshConn, chans, reqs)
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close() // the error says more than the close error
		return nil, err
	}
	_, posixRename := sftpClient.HasExtension(posixRenameExtension)
	return &conn{
		ssh:         sshClient,
		sftp:        sftpClient,
		posixRename: posixRename,
		lost:        make(chan struct{}),
	}, nil
}

// conn is an SSH connection with an SFTP session.
type conn struct {
	ssh         *ssh.Client
	sftp        *sftp.Client
	posixRename bool          // the server supports posixRenameExtension
	lost        chan struct{} // closed when the SFTP session ended

	closeOnce sync.Once
	closeErr  error
}

// close closes the SSH connection and with it the SFTP session.
// It is safe to call more than once.
func (c *conn) close() error {
	c.closeOnce.Do(func() {
		// Closing the SSH connection first, because sftp.Client.Close
		// waits for the server to end the session, which a hung server never does
		err := c.ssh.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil // closed by the server or the network already
		}
		_ = c.sftp.Close() // can only fail because the connection is closed
		c.closeErr = err
	})
	return c.closeErr
}

// isLost reports whether the SFTP session of c ended.
func (c *conn) isLost() bool {
	select {
	case <-c.lost:
		return true
	default:
		return false
	}
}

// watch closes and forgets c when its SFTP session ends, so that the
// next operation reconnects, and sends the keep-alive requests.
func (f *fileSystem) watch(c *conn) {
	go func() {
		_ = c.sftp.Wait()
		close(c.lost)
		f.dropConn(c)
	}()
	if interval := f.opts.keepAlive(); interval > 0 {
		go f.keepAlive(c, interval)
	}
}

// keepAlive sends an SSH keep-alive request every interval and closes c
// if the server does not answer within the interval, which happens when
// the connection is dead but the network connection does not know it yet.
func (f *fileSystem) keepAlive(c *conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.lost:
			return
		case <-ticker.C:
		}
		answered := make(chan error, 1)
		go func() {
			// Servers answer requests they don't know with a failure,
			// any answer proves that the connection works
			_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
			answered <- err
		}()
		timer := time.NewTimer(interval)
		select {
		case err := <-answered:
			timer.Stop()
			if err != nil {
				_ = c.close()
				return
			}
		case <-timer.C:
			f.logf("No answer to the SSH keep-alive request from %s within %s, closing the connection", f.Prefix(), interval)
			_ = c.close()
			return
		case <-c.lost:
			timer.Stop()
			return
		}
	}
}

// dropConn closes c and forgets it if it is the connection of f,
// so that the next operation reconnects.
func (f *fileSystem) dropConn(c *conn) {
	f.mtx.Lock()
	current := f.conn == c
	if current {
		f.conn = nil
	}
	closed := f.closed
	f.mtx.Unlock()
	if current && !closed {
		f.logf("Lost SFTP connection to %s", f.Prefix())
	}
	_ = c.close()
}

// dialCall is a reconnect in progress that operations wait for.
type dialCall struct {
	done chan struct{}
	conn *conn
	err  error
}

// getConn returns the connection of f, reconnecting if it was lost.
//
// A reconnect runs in its own goroutine and without holding the mutex,
// so that a server that does not answer doesn't block Close or the
// operations, which stop waiting when their ctx ends. Concurrent
// operations share one reconnect. It is limited by Options.Timeout
// per attempt and ended by Close.
func (f *fileSystem) getConn(ctx context.Context) (*conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mtx.Lock()
	if f.closed {
		f.mtx.Unlock()
		return nil, fs.ErrFileSystemClosed
	}
	if c := f.conn; c != nil && !c.isLost() {
		f.mtx.Unlock()
		return c, nil
	}
	call := f.redial
	if call == nil {
		call = &dialCall{done: make(chan struct{})}
		f.redial = call
		go f.reconnect(call)
	}
	f.mtx.Unlock()

	select {
	case <-call.done:
		return call.conn, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// reconnect dials a new connection for f and completes call.
func (f *fileSystem) reconnect(call *dialCall) {
	f.logf("Reconnecting SFTP to %s", f.Prefix())
	c, err := f.reconnectConn()
	f.mtx.Lock()
	f.redial = nil
	closed := f.closed
	if err == nil && !closed {
		f.conn = c
	}
	f.mtx.Unlock()
	switch {
	case closed:
		if c != nil {
			_ = c.close()
		}
		c, err = nil, fs.ErrFileSystemClosed
	case err != nil:
		err = fmt.Errorf("SFTP reconnect failed: %w", err)
	default:
		f.watch(c)
	}
	call.conn, call.err = c, err
	close(call.done)
}

// reconnectConn gets the credentials again and connects
// with the context of f that Close cancels.
func (f *fileSystem) reconnectConn() (*conn, error) {
	creds, err := f.credentials()
	if err != nil {
		return nil, err
	}
	return f.connect(f.life, creds)
}

// run runs op with the connection of f, dialing a new one if there is none.
//
// If op fails because the connection was lost, the connection is closed,
// and op runs once more with a new connection unless op called noRetry
// before. An op calls noRetry before it sends a request that changes the
// server in a way that must not be repeated, or before it calls a callback
// of the caller. After that the error of the lost connection is returned,
// although the server may have executed the request. An op that never
// calls noRetry must be idempotent.
//
// Errors of a canceled ctx and errors returned by callbacks
// (wrapped in callbackError) are never taken for a lost connection.
func (f *fileSystem) run(ctx context.Context, op func(c *conn, noRetry func()) error) error {
	for retried := false; ; retried = true {
		c, err := f.getConn(ctx)
		if err != nil {
			return err
		}
		final := false
		err = op(c, func() { final = true })
		if !isConnectionError(err) {
			return err
		}
		f.dropConn(c)
		if final || retried {
			return err
		}
	}
}

// callbackError marks an error returned by a callback of the caller,
// so that it is never taken for a connection error.
type callbackError struct{ err error }

func (e callbackError) Error() string { return e.err.Error() }
func (e callbackError) Unwrap() error { return e.err }

// isConnectionError reports whether err indicates a lost connection.
//
// A canceled or timed out context is not a lost connection,
// although context.DeadlineExceeded is a net.Error,
// and neither is an error of a callback of the caller.
func isConnectionError(err error) bool {
	switch {
	case err == nil,
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.As(err, new(callbackError)):
		return false
	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, sftp.ErrSSHFxConnectionLost),
		errors.Is(err, sftp.ErrSSHFxNoConnection):
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection closed") ||
		strings.Contains(errStr, "connection lost") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "ssh_msg_disconnect")
}

// logf logs a connection event with Options.Logger if it is set.
func (f *fileSystem) logf(format string, args ...any) {
	if f.opts.Logger != nil {
		f.opts.Logger.Printf(format, args...)
	}
}
