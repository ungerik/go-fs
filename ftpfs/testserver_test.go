package ftpfs

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// testFTPServer is a minimal in-process FTP server for tests. It implements
// just enough of the protocol (login handshake, PASV data connections and the
// STOR/RETR/APPE/SIZE/DELE/RNFR/RNTO commands) to exercise the ftpfs client
// without Docker or a real server. Files are kept in memory.
type testFTPServer struct {
	listener net.Listener
	mu       sync.Mutex
	files    map[string][]byte
}

// newTestFTPServer starts a server on a random loopback port and stops it
// when the test finishes.
func newTestFTPServer(t *testing.T) *testFTPServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testFTPServer{listener: ln, files: make(map[string][]byte)}
	go srv.acceptLoop()
	t.Cleanup(func() { ln.Close() })
	return srv
}

// addr returns the host:port the server is listening on.
func (s *testFTPServer) addr() string {
	return s.listener.Addr().String()
}

// fileContent returns a copy of the stored content for path,
// or nil if the path does not exist.
func (s *testFTPServer) fileContent(path string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[path]
	if !ok {
		return nil
	}
	return append([]byte(nil), data...)
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

func (s *testFTPServer) handle(conn net.Conn) {
	defer conn.Close()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	reply := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}

	var (
		dataLn     net.Listener
		renameFrom string
	)
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
		return dc, err
	}

	reply("220 test FTP server ready")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
		case "USER":
			reply("331 need password")
		case "PASS":
			reply("230 logged in")
		case "FEAT":
			reply("211 End") // no extra features advertised
		case "TYPE", "OPTS", "NOOP":
			reply("200 ok")
		case "SYST":
			reply("215 UNIX Type: L8")
		case "PWD":
			reply(`257 "/"`)
		case "CWD":
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
			dc, err := openData()
			if err != nil {
				reply("425 %v", err)
				continue
			}
			reply("150 ok to send data")
			data, _ := io.ReadAll(dc)
			dc.Close()
			s.mu.Lock()
			if strings.EqualFold(verb, "APPE") {
				s.files[arg] = append(s.files[arg], data...)
			} else {
				s.files[arg] = data
			}
			s.mu.Unlock()
			reply("226 Transfer complete")
		case "RETR":
			s.mu.Lock()
			content, ok := s.files[arg]
			s.mu.Unlock()
			if !ok {
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
			dc.Close()
			reply("226 Transfer complete")
		case "SIZE":
			s.mu.Lock()
			content, ok := s.files[arg]
			s.mu.Unlock()
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("213 %d", len(content))
		case "DELE":
			s.mu.Lock()
			_, ok := s.files[arg]
			delete(s.files, arg)
			s.mu.Unlock()
			if !ok {
				reply("550 file not found")
				continue
			}
			reply("250 deleted")
		case "RNFR":
			renameFrom = arg
			reply("350 ready for destination")
		case "RNTO":
			s.mu.Lock()
			s.files[arg] = s.files[renameFrom]
			delete(s.files, renameFrom)
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
