package sftpfs

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// The user and password that the in-process test server accepts.
const (
	testServerUser     = "u"
	testServerPassword = "p"
)

// testServerConfig configures startTestServer.
type testServerConfig struct {
	// root makes the server serve the local file system with sftp.NewServer,
	// the tests only use paths below root. If empty, the server serves
	// an in-memory file system with sftp.NewRequestServer that is shared
	// by all connections, so it survives reconnects.
	root string

	// noPassword disables the password authentication.
	noPassword bool

	// publicKey is accepted for the public key authentication if not nil.
	publicKey ssh.PublicKey

	// ecdsaHostKey adds an ECDSA host key to the ED25519 key of
	// testServer.hostKey. x/crypto prefers ECDSA host keys by default.
	ecdsaHostKey bool

	// globalRequestDelay delays the answers to global requests
	// like the keep-alives, which are answered one after the other.
	globalRequestDelay time.Duration

	// fail, if not nil, is called before the in-memory file system executes
	// a request. A non-nil error is sent as status reply instead of executing
	// the request, see the statusFromError function of pkg/sftp.
	fail func(r *sftp.Request) error

	// after, if not nil, is called after the in-memory file system executed
	// a request, before the response is sent. Killing the connections in
	// after simulates a connection lost after the server executed a request.
	after func(s *testServer, r *sftp.Request)
}

// testServer is an in-process SSH server with an SFTP subsystem,
// so that the connection handling can be tested without Docker.
type testServer struct {
	addr    string
	hostKey ssh.PublicKey
	cfg     testServerConfig

	handlers sftp.Handlers // of the in-memory file system

	accepted       atomic.Int64 // network connections accepted
	open           atomic.Int64 // SSH connections after the handshake that are not closed
	globalRequests atomic.Int64 // global requests answered, like keep-alives

	mtx      sync.Mutex
	netConns map[net.Conn]struct{}
}

func startTestServer(t *testing.T, cfg testServerConfig) *testServer {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{}
	config.AddHostKey(signer)
	if cfg.ecdsaHostKey {
		ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaKey)
		if err != nil {
			t.Fatal(err)
		}
		config.AddHostKey(ecdsaSigner)
	}
	if !cfg.noPassword {
		config.PasswordCallback = func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() == testServerUser && string(password) == testServerPassword {
				return nil, nil
			}
			return nil, errors.New("wrong username or password")
		}
	}
	if cfg.publicKey != nil {
		config.PublicKeyCallback = func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == testServerUser && bytes.Equal(key.Marshal(), cfg.publicKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown public key")
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{
		addr:     listener.Addr().String(),
		hostKey:  signer.PublicKey(),
		cfg:      cfg,
		netConns: make(map[net.Conn]struct{}),
	}
	s.handlers = sftp.InMemHandler()
	if cfg.fail != nil || cfg.after != nil {
		s.handlers = s.hookHandlers(s.handlers)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		s.killConns()
	})
	go s.serve(listener, config)
	return s
}

func (s *testServer) serve(listener net.Listener, config *ssh.ServerConfig) {
	for {
		netConn, err := listener.Accept()
		if err != nil {
			return
		}
		s.accepted.Add(1)
		s.mtx.Lock()
		s.netConns[netConn] = struct{}{}
		s.mtx.Unlock()
		go s.handle(netConn, config)
	}
}

func (s *testServer) handle(netConn net.Conn, config *ssh.ServerConfig) {
	defer func() {
		_ = netConn.Close()
		s.mtx.Lock()
		delete(s.netConns, netConn)
		s.mtx.Unlock()
	}()
	sshConn, chans, reqs, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		return
	}
	defer sshConn.Close()
	s.open.Add(1)
	defer s.open.Add(-1)
	go s.answerGlobalRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		channel, channelReqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.session(channel, channelReqs)
	}
}

// answerGlobalRequests answers the global requests like the keep-alives
// with a failure, like OpenSSH, after s.cfg.globalRequestDelay.
func (s *testServer) answerGlobalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		time.Sleep(s.cfg.globalRequestDelay)
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		s.globalRequests.Add(1)
	}
}

func (s *testServer) session(channel ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
		_ = req.Reply(ok, nil)
		if ok {
			go s.serveSFTP(channel)
		}
	}
}

func (s *testServer) serveSFTP(channel ssh.Channel) {
	defer channel.Close()
	if s.cfg.root != "" {
		server, err := sftp.NewServer(channel)
		if err != nil {
			return
		}
		_ = server.Serve()
		_ = server.Close()
		return
	}
	server := sftp.NewRequestServer(channel, s.handlers)
	_ = server.Serve()
	_ = server.Close()
}

// killConns closes the server side of all network connections,
// which the client sees as a lost connection.
func (s *testServer) killConns() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	for netConn := range s.netConns {
		_ = netConn.Close()
	}
}

// waitOpen waits until the server has n open SSH connections
// and fails the test if that does not happen within 5 seconds.
func (s *testServer) waitOpen(t *testing.T, n int64) {
	t.Helper()
	waitFor(t, func() bool { return s.open.Load() == n }, "%d open SSH connections, have %d", n, s.open.Load())
}

// waitFor polls condition until it is true and fails
// the test if that does not happen within 5 seconds.
func waitFor(t *testing.T, condition func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for "+format, args...)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// testOptions returns Options for s with the test credentials,
// the host key of s, no keep-alives and a short retry backoff.
func (s *testServer) testOptions() *Options {
	return &Options{
		Username:        testServerUser,
		Password:        testServerPassword,
		HostKeyCallback: ssh.FixedHostKey(s.hostKey),
		RetryBackoff:    time.Millisecond,
		KeepAlive:       -1,
	}
}

// dial dials s with opts, or with s.testOptions if opts is nil,
// and closes the file system at the end of the test.
func (s *testServer) dial(t *testing.T, opts *Options) *fileSystem {
	t.Helper()
	if opts == nil {
		opts = s.testOptions()
	}
	f, err := Dial(t.Context(), "sftp://"+testServerUser+"@"+s.addr, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f.(*fileSystem)
}

// currentConn returns the connection of f or nil.
func (f *fileSystem) currentConn() *conn {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.conn
}

// hookHandlers returns handlers that call s.cfg.fail before
// and s.cfg.after after every request that the handlers execute.
func (s *testServer) hookHandlers(handlers sftp.Handlers) sftp.Handlers {
	h := hook{
		fail: func(r *sftp.Request) error {
			if s.cfg.fail == nil {
				return nil
			}
			return s.cfg.fail(r)
		},
		after: func(r *sftp.Request) {
			if s.cfg.after != nil {
				s.cfg.after(s, r)
			}
		},
	}
	return sftp.Handlers{
		FileGet:  hookGet{handlers.FileGet, h},
		FilePut:  hookPut{handlers.FilePut.(sftp.OpenFileWriter), h},
		FileCmd:  hookCmd{handlers.FileCmd.(sftp.PosixRenameFileCmder), h},
		FileList: hookList{handlers.FileList.(listLinker), h},
	}
}

// hook has the functions that the hooked handlers call around a request.
type hook struct {
	fail  func(*sftp.Request) error
	after func(*sftp.Request)
}

type hookCmd struct {
	inner sftp.PosixRenameFileCmder
	hook
}

func (h hookCmd) Filecmd(r *sftp.Request) error {
	if err := h.fail(r); err != nil {
		return err
	}
	err := h.inner.Filecmd(r)
	h.after(r)
	return err
}

func (h hookCmd) PosixRename(r *sftp.Request) error {
	if err := h.fail(r); err != nil {
		return err
	}
	err := h.inner.PosixRename(r)
	h.after(r)
	return err
}

type hookGet struct {
	inner sftp.FileReader
	hook
}

func (h hookGet) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	if err := h.fail(r); err != nil {
		return nil, err
	}
	reader, err := h.inner.Fileread(r)
	h.after(r)
	return reader, err
}

type hookPut struct {
	inner sftp.OpenFileWriter
	hook
}

func (h hookPut) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	if err := h.fail(r); err != nil {
		return nil, err
	}
	w, err := h.inner.Filewrite(r)
	h.after(r)
	return w, err
}

func (h hookPut) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	if err := h.fail(r); err != nil {
		return nil, err
	}
	f, err := h.inner.OpenFile(r)
	h.after(r)
	return f, err
}

// listLinker is the lister of sftp.InMemHandler.
type listLinker interface {
	sftp.LstatFileLister
	sftp.ReadlinkFileLister
}

type hookList struct {
	inner listLinker
	hook
}

func (h hookList) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	if err := h.fail(r); err != nil {
		return nil, err
	}
	l, err := h.inner.Filelist(r)
	h.after(r)
	return l, err
}

func (h hookList) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	if err := h.fail(r); err != nil {
		return nil, err
	}
	l, err := h.inner.Lstat(r)
	h.after(r)
	return l, err
}

func (h hookList) Readlink(p string) (string, error) {
	return h.inner.Readlink(p)
}

// killOnce returns an after hook for testServerConfig that kills the
// connections after the first request with the method and the path suffix.
func killOnce(method, pathSuffix string) func(s *testServer, r *sftp.Request) {
	var done atomic.Bool
	return func(s *testServer, r *sftp.Request) {
		if r.Method == method && strings.HasSuffix(r.Filepath, pathSuffix) && done.CompareAndSwap(false, true) {
			s.killConns()
		}
	}
}

// testProxy forwards network connections to a backend address.
// It can freeze the forwarded connections, which makes them dead
// without closing them, and hang new connections without forwarding them.
type testProxy struct {
	addr    string
	backend string
	closed  chan struct{}

	mtx   sync.Mutex
	conns []*proxyConn
	hang  bool
}

type proxyConn struct {
	client, server net.Conn // server is nil for a hanging connection
	frozen         chan struct{}
	freezeOnce     sync.Once
}

func startTestProxy(t *testing.T, backend string) *testProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &testProxy{addr: listener.Addr().String(), backend: backend, closed: make(chan struct{})}
	t.Cleanup(func() {
		_ = listener.Close()
		close(p.closed)
		p.mtx.Lock()
		defer p.mtx.Unlock()
		for _, c := range p.conns {
			_ = c.client.Close()
			if c.server != nil {
				_ = c.server.Close()
			}
		}
	})
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.forward(client)
		}
	}()
	return p
}

func (p *testProxy) forward(client net.Conn) {
	c := &proxyConn{client: client, frozen: make(chan struct{})}
	p.mtx.Lock()
	hang := p.hang
	p.conns = append(p.conns, c)
	p.mtx.Unlock()
	if hang {
		return // accepted, but never answered
	}
	server, err := net.Dial("tcp", p.backend)
	if err != nil {
		_ = client.Close()
		return
	}
	p.mtx.Lock()
	c.server = server
	p.mtx.Unlock()
	go p.copy(c, server, client)
	go p.copy(c, client, server)
}

// copy copies from src to dst until one of them is closed.
// After c is frozen, the data is not forwarded anymore and
// nothing is closed until the proxy is closed.
func (p *testProxy) copy(c *proxyConn, dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		select {
		case <-c.frozen:
			<-p.closed
			return
		default:
		}
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			_ = dst.Close()
			return
		}
	}
}

// freeze stops forwarding the data of all current connections
// without closing them, new connections are forwarded again.
func (p *testProxy) freeze() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	for _, c := range p.conns {
		c.freezeOnce.Do(func() { close(c.frozen) })
	}
}

// setHang makes the proxy accept new connections without forwarding
// them if hang is true, like a server that does not answer.
func (p *testProxy) setHang(hang bool) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	p.hang = hang
}

// kill closes all current connections.
func (p *testProxy) kill() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	for _, c := range p.conns {
		_ = c.client.Close()
		if c.server != nil {
			_ = c.server.Close()
		}
	}
}
