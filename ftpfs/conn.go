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
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlaffaye/ftp"
)

// conn is a logged in control connection together with the network
// connections it uses, so that a done context can abort a blocked command.
//
// The FTP client library has no context support. An operation runs with
// conn.run, which sets a deadline in the past on the control connection
// and the current data connection when the context is done. The connection
// is in an unknown state after that and is never used again.
type conn struct {
	*ftp.ServerConn
	cfg *dialConfig

	fresh  bool // dialed for the current operation
	sent   bool // a command that must not run twice was sent in the current operation
	broken bool // the connection must not be used anymore
	reads  int64

	mtx      sync.Mutex
	ctx      context.Context // of the running operation, for dialing data connections
	ctrl     *ctrlConn
	data     net.Conn
	dataHost string
	aborted  bool
}

// ctrlConn is the TCP control connection below TLS.
// It counts the reads that returned data, which tells
// whether the server answered during an operation.
type ctrlConn struct {
	net.Conn
	reads atomic.Int64
}

func (c *ctrlConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.reads.Add(1)
	}
	return n, err
}

// RemoteAddr returns a *net.TCPAddr even for connections of a custom
// Options.DialContext, because the FTP client library requires one.
func (c *ctrlConn) RemoteAddr() net.Addr {
	if addr, ok := c.Conn.RemoteAddr().(*net.TCPAddr); ok {
		return addr
	}
	return &net.TCPAddr{IP: net.IPv4zero}
}

// pastDeadline aborts every blocked and following I/O of a connection.
var pastDeadline = time.Unix(1, 0)

// dial dials and logs in a new control connection. The context and
// Options.Timeout limit the TCP connect, the TLS handshake, the
// greeting and the login. The extra options are passed on to the
// FTP client library.
func dial(ctx context.Context, cfg *dialConfig, username, password string, extra ...ftp.DialOption) (*conn, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	c := &conn{cfg: cfg, ctx: ctx, fresh: true}
	controlAddr := net.JoinHostPort(cfg.hostname, cfg.port)
	options := []ftp.DialOption{
		ftp.DialWithDialFunc(c.dialFunc),
		ftp.DialWithDisabledEPSV(cfg.opts.DisableEPSV),
	}
	options = append(options, extra...)
	if cfg.opts.DebugOut != nil {
		options = append(options, ftp.DialWithDebugOutput(&debugWriter{w: cfg.opts.DebugOut}))
	}
	switch {
	case cfg.implicitTLS():
		// With a dial function the library doesn't wrap the connections
		// in TLS, but it sets up the protection of the data connections
		options = append(options, ftp.DialWithTLS(cfg.tlsConfig))
	case cfg.secure:
		options = append(options, ftp.DialWithExplicitTLS(cfg.tlsConfig))
	}

	stop := context.AfterFunc(ctx, c.abort)
	serverConn, err := ftp.Dial(controlAddr, options...)
	if err == nil {
		err = serverConn.Login(username, password)
		if err != nil {
			err = errors.Join(err, serverConn.Quit())
		}
	}
	if !stop() {
		c.closeNetConns()
		return nil, fmt.Errorf("ftpfs: dialing %s: %w", cfg.prefix, context.Cause(ctx))
	}
	if err != nil {
		c.closeNetConns()
		return nil, err
	}
	c.ServerConn = serverConn
	c.mtx.Lock()
	c.ctx = nil
	c.mtx.Unlock()
	return c, nil
}

// dialFunc dials the control connection on its first call and the data
// connections afterwards. Data connections always go to the host of the
// control connection with the port that the server sent in its EPSV or
// PASV reply, so a PASV reply can't redirect them to another host.
func (c *conn) dialFunc(network, addr string) (net.Conn, error) {
	c.mtx.Lock()
	ctx, ctrl := c.ctx, c.ctrl
	c.mtx.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if ctrl == nil {
		return c.dialControl(ctx, network)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.timeout())
	defer cancel()
	netConn, err := c.cfg.dialContext(ctx, network, net.JoinHostPort(c.dataHost, port))
	if err != nil {
		return nil, err
	}
	c.mtx.Lock()
	c.data = netConn
	if c.aborted {
		_ = netConn.SetDeadline(pastDeadline)
	}
	c.mtx.Unlock()
	if c.cfg.secure {
		return tls.Client(netConn, c.cfg.tlsConfig), nil
	}
	return netConn, nil
}

// dialControl dials the control connection.
func (c *conn) dialControl(ctx context.Context, network string) (net.Conn, error) {
	netConn, err := c.cfg.dialContext(ctx, network, net.JoinHostPort(c.cfg.hostname, c.cfg.port))
	if err != nil {
		return nil, err
	}
	ctrl := &ctrlConn{Conn: netConn}
	c.mtx.Lock()
	c.ctrl = ctrl
	// A custom dialer like a proxy needs the host name for the data
	// connections, else the address that was dialed is used, so that
	// a host name that resolves to several addresses is not resolved
	// to another server
	c.dataHost = c.cfg.hostname
	if addr, ok := netConn.RemoteAddr().(*net.TCPAddr); ok && c.cfg.opts.DialContext == nil {
		c.dataHost = addr.IP.String()
	}
	if c.aborted {
		_ = netConn.SetDeadline(pastDeadline)
	}
	c.mtx.Unlock()
	if c.cfg.implicitTLS() {
		return tls.Client(ctrl, c.cfg.tlsConfig), nil
	}
	return ctrl, nil
}

// abort makes every blocked and following I/O of the connection fail.
func (c *conn) abort() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.aborted = true
	if c.ctrl != nil {
		_ = c.ctrl.SetDeadline(pastDeadline)
	}
	if c.data != nil {
		_ = c.data.SetDeadline(pastDeadline)
	}
}

// isAborted reports whether abort was called.
func (c *conn) isAborted() bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.aborted
}

// closeNetConns closes the network connections without a QUIT command.
func (c *conn) closeNetConns() {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.ctrl != nil {
		_ = c.ctrl.Close()
	}
	if c.data != nil {
		_ = c.data.Close()
	}
}

// close quits a usable connection and closes a broken one.
func (c *conn) close() error {
	if c.broken {
		c.closeNetConns()
		return nil
	}
	return c.Quit()
}

// begin prepares the connection for an operation with ctx.
func (c *conn) begin(ctx context.Context) {
	c.mtx.Lock()
	c.ctx = ctx
	c.mtx.Unlock()
	c.sent = false
	c.reads = c.ctrl.reads.Load()
}

// operationContext returns the context of the running operation,
// which Close of the file system cancels.
func (c *conn) operationContext() context.Context {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// hasMLST reports whether the server supports MLST:
// IsTimePreciseInList tells that the connection uses MLSD and MLST.
func (c *conn) hasMLST() bool {
	return c.IsTimePreciseInList()
}

// run runs op on the connection. If ctx is done while op is running,
// the connection is aborted and run returns the cause of ctx.
func (c *conn) run(ctx context.Context, op func() error) error {
	c.begin(ctx)
	stop := context.AfterFunc(ctx, c.abort)
	err := op()
	if !stop() {
		c.broken = true
		if err != nil {
			return context.Cause(ctx)
		}
	}
	if isConnectionError(err) {
		c.broken = true
	}
	return err
}

// beforeMutation must be called by an operation right before it sends a
// command that must not run twice, like APPE, DELE, RMD, MKD or RNFR/RNTO.
// A connection error after it is not retried, because the server may
// have executed the command and only its reply was lost.
//
// If the server hasn't answered anything during the operation on a
// connection that was idle before, a NOOP checks that the connection
// is still alive, so that a connection that the server closed while
// it was idle is replaced before the command is sent.
func (c *conn) beforeMutation() error {
	if !c.fresh && c.ctrl.reads.Load() == c.reads {
		if err := c.NoOp(); err != nil {
			return err
		}
	}
	c.sent = true
	return nil
}

// isConnectionError returns true if the error indicates a connection loss.
// Context errors are not connection errors.
func isConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return statusCode(err) == ftp.StatusNotAvailable // 421 service not available, closing control connection
}

// statusCode returns the FTP reply code of a textproto.Error or 0.
func statusCode(err error) int {
	if e, ok := errors.AsType[*textproto.Error](err); ok {
		return e.Code
	}
	return 0
}

// fileUnavailable reports whether err is a reply that the server sends
// for a path it can't use: 550 for a missing file or directory and 553
// that some servers like vsftpd send for an upload to a missing directory.
func fileUnavailable(err error) bool {
	code := statusCode(err)
	return code == ftp.StatusFileUnavailable || code == ftp.StatusFileActionIgnored
}

// ignoreSuccessReply maps errors that carry a 1xx or 2xx reply
// code to nil. Some servers reply to a completed data transfer
// in a way the client library reports as error.
func ignoreSuccessReply(err error) error {
	if code := statusCode(err); code != 0 && code < 300 {
		return nil
	}
	return err
}

// debugWriter writes the protocol log to w with the argument
// of the PASS command replaced, so the log never contains a password.
// The client library writes every command with a single Write call;
// a PASS line that is split over several calls is swallowed until its end.
type debugWriter struct {
	w        io.Writer
	skipping bool
}

var (
	passCommand  = []byte("PASS ")
	passRedacted = []byte("PASS ****\r\n")
)

func (d *debugWriter) Write(p []byte) (int, error) {
	if !d.skipping && !bytes.Contains(bytes.ToUpper(p), passCommand) {
		return d.w.Write(p)
	}
	var out []byte
	for line := range bytes.Lines(p) {
		switch {
		case d.skipping:
			d.skipping = !bytes.HasSuffix(line, []byte("\n"))
		case len(line) >= len(passCommand) && bytes.EqualFold(line[:len(passCommand)], passCommand):
			out = append(out, passRedacted...)
			d.skipping = !bytes.HasSuffix(line, []byte("\n"))
		default:
			out = append(out, line...)
		}
	}
	if _, err := d.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
