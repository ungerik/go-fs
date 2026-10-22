package ftpfs

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testFTPServer is a minimal in-process FTP(S) server for tests. It keeps
// a directory tree in memory and implements enough of the protocol for the
// conformance suite: login, EPSV and PASV data connections, STOR/APPE/RETR,
// MLST and MLSD (RFC 3659) or LIST listings, SIZE, MDTM, MFMT, MKD, RMD,
// DELE, RNFR/RNTO, and explicit TLS via AUTH TLS, PBSZ and PROT with a
// self-signed certificate. It exercises the ftpfs client without Docker
// or a real server on every platform.
//
// The hook fields simulate failures and server variants for the
// regression tests; set them with configure.
type testFTPServer struct {
	listener net.Listener
	tlsCfg   *tls.Config
	logins   atomic.Int64

	mu       sync.Mutex
	nodes    map[string]*testFTPNode // clean paths, "/" is the root
	commands map[string]int          // number of received commands per verb
	lastUser string
	lastPass string

	// Hooks, guarded by mu
	failListContains string         // LIST and MLSD of a path containing it fail with 550
	dropBefore       map[string]int // the next commands with the verb close the connection without executing them
	dropReplyOf      map[string]int // the next commands with the verb are executed, then the connection closes instead of the final reply
	hang             string         // commands with this verb are never answered
	silent           bool           // new connections get no greeting
	noMLST           bool           // MLST and MLSD are not supported, listings use LIST
	noEPSV           bool           // EPSV is not implemented
	pasvHost         string         // host part of PASV replies like "10,1,2,3" instead of the real address
	rntoNoReplace    bool           // RNTO onto an existing path fails like on servers that don't replace files
	requireTLSReuse  bool           // TLS data connections must resume the TLS session of the control connection
}

type testFTPNode struct {
	isDir    bool
	data     []byte
	modified time.Time
}

// newTestFTPServer starts a server on a random loopback port and stops it
// when the test finishes.
func newTestFTPServer(t *testing.T) *testFTPServer {
	t.Helper()
	return newTestFTPServerOn(t, "127.0.0.1:0")
}

// newTestFTPServerOn starts a server listening on address.
func newTestFTPServerOn(t *testing.T, address string) *testFTPServer {
	t.Helper()

	ln, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testFTPServer{
		listener: ln,
		tlsCfg:   testTLSConfig(t),
		nodes:    map[string]*testFTPNode{"/": {isDir: true, modified: time.Now()}},
		commands: make(map[string]int),

		dropBefore:  make(map[string]int),
		dropReplyOf: make(map[string]int),
	}
	go srv.acceptLoop()
	t.Cleanup(func() { ln.Close() })
	return srv
}

// testTLSConfig returns a server TLS config with a self-signed certificate.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
}

// addr returns the host:port the server is listening on.
func (s *testFTPServer) addr() string {
	return s.listener.Addr().String()
}

// configure changes hooks of the server under its lock.
func (s *testFTPServer) configure(change func(s *testFTPServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(s)
}

// count returns the number of received commands with verb.
func (s *testFTPServer) count(verb string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commands[verb]
}

// credentials returns the arguments of the last USER and PASS commands.
func (s *testFTPServer) credentials() (user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUser, s.lastPass
}

// fileContent returns a copy of the stored content for path,
// or nil if the path does not exist.
func (s *testFTPServer) fileContent(p string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[path.Clean(p)]
	if !ok {
		return nil
	}
	return append([]byte(nil), node.data...)
}

// exists reports whether the path exists.
func (s *testFTPServer) exists(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.nodes[path.Clean(p)]
	return ok
}

// mkdirAll creates a directory and its parents for seeding tests.
func (s *testFTPServer) mkdirAll(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p = path.Clean(p)
	for p != "/" {
		if _, ok := s.nodes[p]; !ok {
			s.nodes[p] = &testFTPNode{isDir: true, modified: time.Now()}
		}
		p = path.Dir(p)
	}
}

// writeFile stores a file for seeding tests, creating its parents.
func (s *testFTPServer) writeFile(p, content string) {
	s.mkdirAll(path.Dir(p))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[path.Clean(p)] = &testFTPNode{data: []byte(content), modified: time.Now()}
}

func (s *testFTPServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handle(conn)
	}
}

// children returns the sorted names of the entries of the directory dir.
// The caller must hold s.mu.
func (s *testFTPServer) children(dir string) []string {
	var names []string
	for p := range s.nodes {
		if p != "/" && path.Dir(p) == dir {
			names = append(names, path.Base(p))
		}
	}
	sort.Strings(names)
	return names
}

// mlstFact formats the RFC 3659 facts of a node.
func mlstFact(node *testFTPNode) string {
	modify := node.modified.UTC().Format("20060102150405")
	if node.isDir {
		return fmt.Sprintf("type=dir;modify=%s;", modify)
	}
	return fmt.Sprintf("type=file;size=%d;modify=%s;", len(node.data), modify)
}

// listLine formats a node like ls -l for the LIST command.
func listLine(node *testFTPNode, name string) string {
	mode := "-rw-r--r--"
	if node.isDir {
		mode = "drwxr-xr-x"
	}
	return fmt.Sprintf("%s 1 owner group %d %s %s", mode, len(node.data), node.modified.UTC().Format("Jan _2 15:04"), name)
}

// takeHook returns true and decrements the count of verb if it is positive.
// The caller must hold s.mu.
func takeHook(hook map[string]int, verb string) bool {
	if hook[verb] > 0 {
		hook[verb]--
		return true
	}
	return false
}

func (s *testFTPServer) handle(rawConn net.Conn) {
	conn := rawConn
	defer func() { conn.Close() }()

	s.mu.Lock()
	silent := s.silent
	s.mu.Unlock()
	if silent {
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	// dropReply is set for a command whose final reply is not sent:
	// the connection is closed instead
	var dropReply, dropped bool
	reply := func(format string, args ...any) {
		if dropReply {
			if code, _ := strconv.Atoi(format[:3]); code >= 200 {
				dropped = true
				rawConn.Close()
				return
			}
		}
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}

	var (
		dataLn     net.Listener
		dataTLS    bool
		renameFrom string
		cwd        = "/"
	)
	localHost, _, _ := net.SplitHostPort(rawConn.LocalAddr().String())
	// abs resolves a command argument against the working directory.
	abs := func(arg string) string {
		if !strings.HasPrefix(arg, "/") {
			arg = path.Join(cwd, arg)
		}
		return path.Clean(arg)
	}
	// listenData opens the listener for the next data connection.
	listenData := func() (port int, err error) {
		l, err := net.Listen("tcp", net.JoinHostPort(localHost, "0"))
		if err != nil {
			return 0, err
		}
		if dataLn != nil {
			dataLn.Close()
		}
		dataLn = l
		return l.Addr().(*net.TCPAddr).Port, nil
	}
	// openData accepts the data connection queued by the preceding EPSV or PASV.
	openData := func() (net.Conn, error) {
		if dataLn == nil {
			return nil, fmt.Errorf("no PASV before data command")
		}
		if l, ok := dataLn.(*net.TCPListener); ok {
			l.SetDeadline(time.Now().Add(5 * time.Second))
		}
		dc, err := dataLn.Accept()
		dataLn.Close()
		dataLn = nil
		if err != nil {
			return nil, err
		}
		if dataTLS {
			dc = tls.Server(dc, s.tlsCfg)
		}
		return dc, nil
	}
	// handshake completes the TLS handshake of a data connection after
	// the 150 reply, which the client waits for before it starts the
	// handshake, and checks the session reuse if required.
	handshake := func(dc net.Conn) error {
		tlsConn, ok := dc.(*tls.Conn)
		if !ok {
			return nil
		}
		_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := tlsConn.Handshake(); err != nil {
			return err
		}
		_ = tlsConn.SetDeadline(time.Time{})
		s.mu.Lock()
		requireReuse := s.requireTLSReuse
		s.mu.Unlock()
		if requireReuse && !tlsConn.ConnectionState().DidResume {
			return errors.New("session reuse required")
		}
		return nil
	}

	// closeSent closes a data connection after the server sent data on it.
	// A TLS connection is shut down for writing first and drained until the
	// client's close_notify, otherwise closing with unread data resets the
	// connection (an error on Windows). The draining happens in the
	// background because the client closes its side only after it received
	// the transfer complete reply.
	closeSent := func(dc net.Conn) {
		tlsConn, ok := dc.(*tls.Conn)
		if !ok {
			dc.Close()
			return
		}
		_ = tlsConn.CloseWrite()
		go func() {
			_ = dc.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.Copy(io.Discard, dc)
			dc.Close()
		}()
	}
	// sendData sends a listing or file content on a new data connection.
	sendData := func(data []byte) {
		dc, err := openData()
		if err != nil {
			reply("425 %v", err)
			return
		}
		reply("150 opening data connection")
		if err := handshake(dc); err != nil {
			dc.Close()
			reply("522 SSL connection failed: %v", err)
			return
		}
		dc.Write(data)
		closeSent(dc)
		reply("226 Transfer complete")
	}

	reply("220 test FTP server ready")

	for !dropped {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		verb = strings.ToUpper(verb)

		s.mu.Lock()
		s.commands[verb]++
		dropBefore := takeHook(s.dropBefore, verb)
		dropReply = takeHook(s.dropReplyOf, verb)
		hang := s.hang == verb
		noMLST, noEPSV, pasvHost := s.noMLST, s.noEPSV, s.pasvHost
		failList := s.failListContains
		rntoNoReplace := s.rntoNoReplace
		s.mu.Unlock()

		if dropBefore {
			return
		}
		if hang {
			// Never answer, wait until the client closes the connection
			_, _ = io.Copy(io.Discard, r)
			return
		}

		switch verb {
		case "AUTH":
			if !strings.EqualFold(arg, "TLS") {
				reply("504 only AUTH TLS is supported")
				continue
			}
			reply("234 proceed with TLS")
			conn = tls.Server(rawConn, s.tlsCfg)
			w = bufio.NewWriter(conn)
			r = bufio.NewReader(conn)
		case "PBSZ":
			reply("200 PBSZ=0")
		case "PROT":
			dataTLS = strings.EqualFold(arg, "P")
			reply("200 protection level set")
		case "USER":
			s.mu.Lock()
			s.lastUser = arg
			s.mu.Unlock()
			reply("331 need password")
		case "PASS":
			s.mu.Lock()
			s.lastPass = arg
			s.mu.Unlock()
			s.logins.Add(1)
			reply("230 logged in")
		case "FEAT":
			features := " SIZE\r\n MDTM\r\n MFMT\r\n AUTH TLS\r\n PBSZ\r\n PROT\r\n"
			if !noMLST {
				features = " MLST type*;size*;modify*;\r\n" + features
			}
			reply("211-Features:\r\n%s211 End", features)
		case "TYPE", "OPTS", "NOOP":
			reply("200 ok")
		case "SYST":
			reply("215 UNIX Type: L8")
		case "PWD":
			reply(`257 "%s"`, cwd)
		case "CDUP":
			cwd = path.Dir(cwd)
			reply("250 ok")
		case "CWD":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			s.mu.Unlock()
			if !ok || !node.isDir {
				reply("550 no such directory")
				continue
			}
			cwd = p
			reply("250 ok")
		case "EPSV":
			if noEPSV {
				reply("502 EPSV not implemented")
				continue
			}
			port, err := listenData()
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			reply("229 Entering Extended Passive Mode (|||%d|)", port)
		case "PASV":
			port, err := listenData()
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			host := strings.ReplaceAll(localHost, ".", ",")
			if pasvHost != "" {
				host = pasvHost
			}
			reply("227 Entering Passive Mode (%s,%d,%d)", host, port/256, port%256)
		case "STOR", "APPE":
			p := abs(arg)
			s.mu.Lock()
			parent, parentOK := s.nodes[path.Dir(p)]
			existing, exists := s.nodes[p]
			s.mu.Unlock()
			if !parentOK || !parent.isDir || (exists && existing.isDir) {
				reply("550 cannot store here")
				continue
			}
			dc, err := openData()
			if err != nil {
				reply("425 %v", err)
				continue
			}
			reply("150 ok to send data")
			if err := handshake(dc); err != nil {
				dc.Close()
				reply("522 SSL connection failed: %v", err)
				continue
			}
			data, _ := io.ReadAll(dc)
			dc.Close()
			s.mu.Lock()
			if verb == "APPE" && exists {
				existing.data = append(existing.data, data...)
				existing.modified = time.Now()
			} else {
				s.nodes[p] = &testFTPNode{data: data, modified: time.Now()}
			}
			s.mu.Unlock()
			reply("226 Transfer complete")
		case "RETR":
			s.mu.Lock()
			node, ok := s.nodes[abs(arg)]
			var content []byte
			if ok {
				content = append([]byte(nil), node.data...)
			}
			s.mu.Unlock()
			if !ok || node.isDir {
				reply("550 file not found")
				continue
			}
			sendData(content)
		case "SIZE":
			s.mu.Lock()
			node, ok := s.nodes[abs(arg)]
			s.mu.Unlock()
			if !ok || node.isDir {
				reply("550 file not found")
				continue
			}
			reply("213 %d", len(node.data))
		case "MDTM":
			s.mu.Lock()
			node, ok := s.nodes[abs(arg)]
			s.mu.Unlock()
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("213 %s", node.modified.UTC().Format("20060102150405"))
		case "MFMT":
			timeval, p, _ := strings.Cut(arg, " ")
			modified, err := time.Parse("20060102150405", timeval)
			s.mu.Lock()
			node, ok := s.nodes[abs(p)]
			if ok && err == nil {
				node.modified = modified
			}
			s.mu.Unlock()
			if !ok || err != nil {
				reply("550 cannot set time")
				continue
			}
			reply("213 Modify=%s; %s", timeval, abs(p))
		case "MLST":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			var fact string
			if ok {
				fact = mlstFact(node)
			}
			s.mu.Unlock()
			if noMLST {
				reply("500 unknown command")
				continue
			}
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("250-Listing\r\n %s %s\r\n250 End", fact, p)
		case "MLSD", "LIST":
			p := abs(strings.TrimPrefix(arg, "-a "))
			if arg == "" || arg == "-a" {
				p = cwd
			}
			if verb == "MLSD" && noMLST {
				reply("500 unknown command")
				continue
			}
			if failList != "" && strings.Contains(p, failList) {
				reply("550 permission denied")
				continue
			}
			s.mu.Lock()
			node, ok := s.nodes[p]
			var lines []string
			if ok && node.isDir {
				for _, name := range s.children(p) {
					child := s.nodes[path.Join(p, name)]
					if verb == "MLSD" {
						lines = append(lines, fmt.Sprintf("%s %s", mlstFact(child), name))
					} else {
						lines = append(lines, listLine(child, name))
					}
				}
			}
			s.mu.Unlock()
			if !ok || !node.isDir {
				reply("550 not a directory")
				continue
			}
			var data []byte
			for _, l := range lines {
				data = fmt.Appendf(data, "%s\r\n", l)
			}
			sendData(data)
		case "MKD":
			p := abs(arg)
			s.mu.Lock()
			parent, parentOK := s.nodes[path.Dir(p)]
			_, exists := s.nodes[p]
			if parentOK && parent.isDir && !exists {
				s.nodes[p] = &testFTPNode{isDir: true, modified: time.Now()}
			}
			s.mu.Unlock()
			if !parentOK || !parent.isDir || exists {
				reply("550 cannot create directory")
				continue
			}
			reply(`257 "%s" created`, p)
		case "RMD":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			empty := ok && node.isDir && len(s.children(p)) == 0
			if empty {
				delete(s.nodes, p)
			}
			s.mu.Unlock()
			if !empty {
				reply("550 cannot remove directory")
				continue
			}
			reply("250 removed")
		case "DELE":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			if ok && !node.isDir {
				delete(s.nodes, p)
			}
			s.mu.Unlock()
			if !ok || node.isDir {
				reply("550 file not found")
				continue
			}
			reply("250 deleted")
		case "RNFR":
			renameFrom = abs(arg)
			s.mu.Lock()
			_, ok := s.nodes[renameFrom]
			s.mu.Unlock()
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("350 ready for destination")
		case "RNTO":
			to := abs(arg)
			s.mu.Lock()
			_, destExists := s.nodes[to]
			if !destExists || !rntoNoReplace {
				// Like rename(2) on Unix: replaces a file and merges into
				// a directory, which ftpfs must prevent with its checks
				for p, node := range s.nodes {
					if p == renameFrom || strings.HasPrefix(p, renameFrom+"/") {
						delete(s.nodes, p)
						s.nodes[to+strings.TrimPrefix(p, renameFrom)] = node
					}
				}
			}
			s.mu.Unlock()
			if destExists && rntoNoReplace {
				reply("550 destination exists")
				continue
			}
			reply("250 renamed")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 command not implemented")
		}
	}
}
