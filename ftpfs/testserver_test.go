package ftpfs

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// testFTPServer is a minimal in-process FTP(S) server for tests. It keeps
// a directory tree in memory and implements enough of the protocol for the
// conformance suite: login, PASV data connections, STOR/APPE/RETR, MLST and
// MLSD (RFC 3659) listings, SIZE, MDTM, MKD, RMD, DELE, RNFR/RNTO, and
// explicit TLS via AUTH TLS, PBSZ and PROT with a self-signed certificate.
// It exercises the ftpfs client without Docker or a real server on every
// platform.
type testFTPServer struct {
	listener net.Listener
	tlsCfg   *tls.Config
	mu       sync.Mutex
	nodes    map[string]*testFTPNode // clean paths, "/" is the root
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testFTPServer{
		listener: ln,
		tlsCfg:   testTLSConfig(t),
		nodes:    map[string]*testFTPNode{"/": {isDir: true, modified: time.Now()}},
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
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
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

func (s *testFTPServer) handle(rawConn net.Conn) {
	conn := rawConn
	defer func() { conn.Close() }()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	reply := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}

	var (
		dataLn     net.Listener
		dataTLS    bool
		renameFrom string
		cwd        = "/"
	)
	// abs resolves a command argument against the working directory.
	abs := func(arg string) string {
		if !strings.HasPrefix(arg, "/") {
			arg = path.Join(cwd, arg)
		}
		return path.Clean(arg)
	}
	// openData accepts the data connection queued by the preceding PASV.
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
		// Nothing may have been written (empty listing), so make
		// sure the handshake happened before the close_notify
		_ = tlsConn.Handshake()
		_ = tlsConn.CloseWrite()
		go func() {
			_ = dc.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.Copy(io.Discard, dc)
			dc.Close()
		}()
	}

	reply("220 test FTP server ready")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
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
			reply("331 need password")
		case "PASS":
			reply("230 logged in")
		case "FEAT":
			reply("211-Features:\r\n MLST type*;size*;modify*;\r\n SIZE\r\n MDTM\r\n AUTH TLS\r\n PBSZ\r\n PROT\r\n211 End")
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
		case "PASV":
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				reply("425 cannot open data connection")
				continue
			}
			dataLn = l
			port := l.Addr().(*net.TCPAddr).Port
			reply("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256)
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
			data, _ := io.ReadAll(dc)
			dc.Close()
			s.mu.Lock()
			if strings.EqualFold(verb, "APPE") && exists {
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
			dc, err := openData()
			if err != nil {
				reply("425 %v", err)
				continue
			}
			reply("150 opening data connection")
			dc.Write(content)
			closeSent(dc)
			reply("226 Transfer complete")
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
		case "MLST":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			s.mu.Unlock()
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("250-Listing\r\n %s %s\r\n250 End", mlstFact(node), p)
		case "MLSD":
			p := abs(arg)
			s.mu.Lock()
			node, ok := s.nodes[p]
			var lines []string
			if ok && node.isDir {
				for _, name := range s.children(p) {
					lines = append(lines, fmt.Sprintf("%s %s", mlstFact(s.nodes[path.Join(p, name)]), name))
				}
			}
			s.mu.Unlock()
			if !ok || !node.isDir {
				reply("550 not a directory")
				continue
			}
			dc, err := openData()
			if err != nil {
				reply("425 %v", err)
				continue
			}
			reply("150 opening data connection")
			for _, l := range lines {
				fmt.Fprintf(dc, "%s\r\n", l)
			}
			closeSent(dc)
			reply("226 Transfer complete")
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
			for p, node := range s.nodes {
				if p == renameFrom || strings.HasPrefix(p, renameFrom+"/") {
					delete(s.nodes, p)
					s.nodes[to+strings.TrimPrefix(p, renameFrom)] = node
				}
			}
			s.mu.Unlock()
			reply("250 renamed")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 command not implemented")
		}
	}
}
