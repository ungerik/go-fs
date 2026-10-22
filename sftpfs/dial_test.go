package sftpfs

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ungerik/go-fs"
)

func TestParseAddress(t *testing.T) {
	for _, test := range []struct {
		address, hostPort, prefixHost string
		defaultPort                   bool
	}{
		{"host", "host:22", "host", true},
		{"user@host:2222", "host:2222", "host:2222", false},
		{"sftp://user@host", "host:22", "host", true},
		{"sftp://user@host:22/some/path?q#f", "host:22", "host", true},
		{"sftp://user:secret@host:", "host:22", "host", true},
		{"[::1]", "[::1]:22", "[::1]", true},
		{"sftp://user@[::1]:22", "[::1]:22", "[::1]", true},
		{"sftp://user@[::1]:2222", "[::1]:2222", "[::1]:2222", false},
		{"sftp://user@[fe80::1%25eth0]", "[fe80::1%eth0]:22", "[fe80::1%25eth0]", true},
		// url.Parse ends the user info at the last '@' before the host
		{"sftp://user:p@ss@host", "host:22", "host", true},
		{"sftp://user@host/%40.txt", "host:22", "host", true},
	} {
		t.Run(test.address, func(t *testing.T) {
			target, err := parseAddress(test.address)
			require.NoError(t, err)
			assert.Equal(t, test.hostPort, target.hostPort, "hostPort")
			assert.Equal(t, test.prefixHost, target.prefixHost, "prefixHost")
			assert.Equal(t, test.defaultPort, target.defaultPort, "defaultPort")
		})
	}

	for _, test := range []struct {
		address string
		secrets []string // parts of the password
	}{
		{"ftp://host", nil},
		{"sftp://", nil},
		{"sftp://user:secret@", []string{"secret"}},
		{"sftp://user:secret@ho st", []string{"secret"}},
		{"user:secret@[::1", []string{"secret"}},
		// url.Parse repeats an invalid escape of the password in its error
		{"sftp://user:sec%zzret@host", []string{"sec", "%zz"}},
		// An unencoded '/', '?' or '#' of a password ends the user info
		// for url.Parse, which then fails on the port "ab" or takes "12"
		// for the port and the rest of the password for the path,
		// query or fragment. The password is "ab/cd", "12/34" and so on.
		{"sftp://bob:ab/cd@host", []string{"ab", "cd"}},
		{"sftp://bob:12/34@127.0.0.1", []string{"12/", "/34", "34@"}},
		{"sftp://bob:ab?cd@host", []string{"ab", "cd"}},
		{"sftp://bob:12?34@host", []string{"12", "34"}},
		{"sftp://bob:ab#cd@host", []string{"ab", "cd"}},
		{"sftp://bob:12#34@host", []string{"12", "34"}},
		{"bob:12/34@host:2222", []string{"12/", "/34", "34@"}},
		// An unencoded '@' and '/' of a password
		{"sftp://bob:a@b/cd@host", []string{"a@b", "b/cd", "cd@"}},
	} {
		t.Run(test.address, func(t *testing.T) {
			_, err := parseAddress(test.address)
			require.Error(t, err)
			for _, secret := range test.secrets {
				assert.NotContains(t, err.Error(), secret, "the error must not contain the password")
			}
		})
	}
}

func TestRedactAddress(t *testing.T) {
	for address, want := range map[string]string{
		"sftp://user:secret@host":          "sftp://user:xxxxx@host",
		"sftp://user@host":                 "sftp://user@host",
		"sftp://user@host/path":            "sftp://user@host/path",
		"sftp://user:secret@ho st/path":    "sftp://user:xxxxx@ho st/path",
		"sftp://user:secret@[::1":          "sftp://user:xxxxx@[::1",
		"sftp://ho st":                     "sftp://ho st",
		"user:secret@host":                 "user:xxxxx@host",
		"sftp://:secret@host":              "sftp://:xxxxx@host",
		"sftp://bob:ab/cd@host":            "sftp://bob:xxxxx@host",
		"sftp://bob:12/34@127.0.0.1":       "sftp://bob:xxxxx@127.0.0.1",
		"sftp://bob:12?34@host":            "sftp://bob:xxxxx@host",
		"sftp://bob:ab#cd@host":            "sftp://bob:xxxxx@host",
		"sftp://bob:a@b/c@host":            "sftp://bob:xxxxx@host",
		"sftp://bob:ab/cd@host:22/path":    "sftp://bob:xxxxx@host:22/path",
		"sftp://ali/ce:secret@host":        "sftp://xxxxx@host", // no telling the username apart
		"sftp://host/dir/name@example.com": "sftp://xxxxx@example.com",
	} {
		assert.Equal(t, want, redactAddress(address), address)
	}
}

// TestPasswordWithSpecialCharactersNotInErrors dials addresses whose
// password contains an unencoded '/', '?' or '#': the errors of Dial
// and EnsureRegistered must not contain any part of the password,
// and nothing must be dialed with a part of the password as host.
func TestPasswordWithSpecialCharactersNotInErrors(t *testing.T) {
	var dialed atomic.Int64
	opts := &Options{
		HostKeyCallback: AcceptAnyHostKey,
		ConnectRetries:  -1,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.Add(1)
			return nil, errors.New("must not dial " + addr)
		},
	}
	for address, secrets := range map[string][]string{
		"sftp://bob:ab/cd@host":      {"ab", "cd"},
		"sftp://bob:12/34@127.0.0.1": {"12/", "/34", "34@"},
		"sftp://bob:12?34@127.0.0.1": {"12?", "?34", "34@"},
		"sftp://bob:12#34@127.0.0.1": {"12#", "#34", "34@"},
	} {
		_, err := Dial(t.Context(), address, opts)
		require.Error(t, err, address)
		free, ensureErr := EnsureRegistered(t.Context(), address, opts)
		require.Error(t, ensureErr, address)
		require.NoError(t, free())
		for _, secret := range secrets {
			assert.NotContains(t, err.Error(), secret, "Dial error of %s", address)
			assert.NotContains(t, ensureErr.Error(), secret, "EnsureRegistered error of %s", address)
		}
	}
	assert.Zero(t, dialed.Load(), "an address with a misplaced password must not be dialed")
}

func TestPrefixAndNames(t *testing.T) {
	for _, test := range []struct {
		address string
		opts    Options
		prefix  string
		aliases []string
	}{
		{"user@host", Options{}, "sftp://user@host", []string{"sftp://user@host:22"}},
		{"sftp://user:secret@host:22/path", Options{}, "sftp://user@host", []string{"sftp://user@host:22"}},
		{"host:2222", Options{Username: "erik"}, "sftp://erik@host:2222", nil},
		{"[::1]", Options{Username: "erik"}, "sftp://erik@[::1]", []string{"sftp://erik@[::1]:22"}},
		{"host", Options{Username: "first.last@example.com"}, "sftp://first.last%40example.com@host", []string{"sftp://first.last%40example.com@host:22"}},
	} {
		t.Run(test.address, func(t *testing.T) {
			test.opts.HostKeyCallback = AcceptAnyHostKey
			f, _, err := newFileSystem(test.address, &test.opts)
			require.NoError(t, err)
			defer f.Close()
			assert.Equal(t, test.prefix, f.Prefix())
			assert.Equal(t, test.prefix, f.ID())
			assert.Equal(t, test.aliases, f.PrefixAliases())
			assert.Equal(t, "SFTP", f.Name())
			assert.Equal(t, "SFTP "+test.prefix, f.String())
			assert.Equal(t, fs.File(test.prefix+"/"), f.RootDir())
			assert.Equal(t, fs.File(test.prefix+"/a/b"), f.JoinCleanFile("a", "skip", "..", "b"))
			assert.NotContains(t, f.String(), "secret")
		})
	}
}

func TestCredentials(t *testing.T) {
	newFS := func(t *testing.T, address string, opts *Options) (*fileSystem, credentials, error) {
		t.Helper()
		opts.HostKeyCallback = AcceptAnyHostKey
		f, creds, err := newFileSystem(address, opts)
		if f != nil {
			t.Cleanup(func() { _ = f.Close() })
		}
		return f, creds, err
	}

	_, creds, err := newFS(t, "sftp://alice:fromURL@host", &Options{})
	require.NoError(t, err)
	assert.Equal(t, credentials{"alice", "fromURL"}, creds, "credentials of the address")

	_, creds, err = newFS(t, "sftp://alice:a%2Fb%3Fc%23d@e@host", &Options{})
	require.NoError(t, err)
	assert.Equal(t, credentials{"alice", "a/b?c#d@e"}, creds, "a percent-encoded password")

	_, creds, err = newFS(t, "sftp://alice:fromURL@host", &Options{Username: "bob", Password: "fromOptions"})
	require.NoError(t, err)
	assert.Equal(t, credentials{"bob", "fromOptions"}, creds, "the Options win over the address")

	calls := 0
	f, creds, err := newFS(t, "sftp://alice:fromURL@host", &Options{
		Username: "bob",
		Password: "fromOptions",
		Credentials: func(address *url.URL) (string, string, error) {
			calls++
			assert.Equal(t, "alice", address.User.Username(), "the callback gets the address")
			address.User = nil // must not change the address of later calls
			if calls == 1 {
				return "", "fromCallback", nil
			}
			return "carol", "", nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, credentials{"bob", "fromCallback"}, creds, "a non-empty value of the callback wins")
	assert.Equal(t, "sftp://bob@host", f.Prefix())
	creds, err = f.credentials()
	require.NoError(t, err)
	assert.Equal(t, credentials{"carol", "fromOptions"}, creds, "the callback is called again")
	assert.Equal(t, 2, calls)

	_, _, err = newFS(t, "sftp://alice:secret@host", &Options{
		Credentials: func(*url.URL) (string, string, error) { return "", "", errors.New("vault is sealed") },
	})
	require.ErrorContains(t, err, "vault is sealed")
	assert.NotContains(t, err.Error(), "secret")

	_, _, err = newFS(t, "sftp://host", &Options{Password: "secret"})
	require.ErrorContains(t, err, "no SFTP username")
	assert.NotContains(t, err.Error(), "secret")
}

func TestDialRequiresHostKeyCallback(t *testing.T) {
	_, err := Dial(t.Context(), "sftp://user:secret@127.0.0.1:1", nil)
	require.ErrorContains(t, err, "HostKeyCallback")
	assert.NotContains(t, err.Error(), "secret")
	err = DialAndRegister(t.Context(), "sftp://user@127.0.0.1:1", &Options{Password: "secret"})
	require.ErrorContains(t, err, "HostKeyCallback")
	free, err := EnsureRegistered(t.Context(), "sftp://user@127.0.0.1:1", &Options{Password: "secret"})
	require.ErrorContains(t, err, "HostKeyCallback")
	require.NotNil(t, free, "free is never nil")
	assert.NoError(t, free())
}

func TestDialHostKeyMismatchNotRetried(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	otherKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	otherPublicKey, err := ssh.NewPublicKey(otherKey)
	require.NoError(t, err)
	opts := s.testOptions()
	opts.HostKeyCallback = ssh.FixedHostKey(otherPublicKey)
	opts.ConnectRetries = 3

	_, err = Dial(t.Context(), s.addr, opts)
	require.ErrorContains(t, err, "host key mismatch")
	assert.Equal(t, int64(1), s.accepted.Load(), "host key errors are not retried")
}

func TestDialAuthErrorNotRetried(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	opts := s.testOptions()
	opts.Password = "wrong-secret"
	opts.ConnectRetries = 3

	_, err := Dial(t.Context(), s.addr, opts)
	require.ErrorContains(t, err, "unable to authenticate")
	assert.NotContains(t, err.Error(), "wrong-secret")
	assert.Equal(t, int64(1), s.accepted.Load(), "authentication errors are not retried")
}

// TestDialPublicKeyAuth dials with a public key and without a password,
// which the old API rejected.
func TestDialPublicKeyAuth(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	s := startTestServer(t, testServerConfig{noPassword: true, publicKey: signer.PublicKey()})

	opts := s.testOptions()
	opts.Password = ""
	opts.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	f := s.dial(t, opts)
	_, err = f.Stat("/")
	require.NoError(t, err)
}

// TestDialRetries checks that a dial makes 1+ConnectRetries attempts
// for network errors with the backoff between them.
func TestDialRetries(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	var (
		mtx      sync.Mutex
		attempts int
		logs     []string
	)
	failFirst := func(n int) func(ctx context.Context, network, addr string) (net.Conn, error) {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			mtx.Lock()
			attempts++
			attempt := attempts
			mtx.Unlock()
			if attempt <= n {
				return nil, refused
			}
			var d net.Dialer
			return d.DialContext(ctx, network, s.addr)
		}
	}
	logger := fs.LoggerFunc(func(format string, args ...any) {
		mtx.Lock()
		defer mtx.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	})

	opts := s.testOptions()
	opts.ConnectRetries = 2
	opts.Logger = logger
	opts.DialContext = failFirst(2)
	f, err := Dial(t.Context(), s.addr, opts)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, 3, attempts, "1+ConnectRetries attempts")
	require.Len(t, logs, 3)
	assert.Contains(t, logs[0], "Waiting 1ms before retry 1 of 2")
	assert.Contains(t, logs[1], "Waiting 2ms before retry 2 of 2")
	assert.Contains(t, logs[2], "Dialed SFTP connection to sftp://u@"+s.addr)

	attempts = 0
	opts.DialContext = failFirst(3)
	_, err = Dial(t.Context(), s.addr, opts)
	assert.ErrorIs(t, err, syscall.ECONNREFUSED)
	assert.Equal(t, 3, attempts, "no attempt after the last retry")

	attempts = 0
	opts.ConnectRetries = -1
	_, err = Dial(t.Context(), s.addr, opts)
	assert.ErrorIs(t, err, syscall.ECONNREFUSED)
	assert.Equal(t, 1, attempts, "a negative ConnectRetries disables retries")

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	opts.ConnectRetries = 5
	opts.RetryBackoff = time.Hour
	opts.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, refused }
	_, err = Dial(ctx, s.addr, opts)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the backoff ends with ctx")
	assert.ErrorIs(t, err, syscall.ECONNREFUSED, "with the error of the last attempt")
}

// startHungServer returns the address of a server that accepts
// connections and never answers, and a counter of the connections
// that were closed by the client.
func startHungServer(t *testing.T) (addr string, closedByClient *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	closedByClient = new(atomic.Int64)
	go func() {
		for {
			netConn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = netConn.Close() })
			go func() {
				// Read the SSH version of the client until the client closes
				buf := make([]byte, 1024)
				for {
					if _, err := netConn.Read(buf); err != nil {
						break
					}
				}
				closedByClient.Add(1)
			}()
		}
	}()
	return listener.Addr().String(), closedByClient
}

// TestDialContextLimitsHandshake checks that ctx ends a dial to a server
// that accepts the connection but never does the SSH handshake.
func TestDialContextLimitsHandshake(t *testing.T) {
	addr, closedByClient := startHungServer(t)
	opts := &Options{Username: "u", Password: "p", HostKeyCallback: AcceptAnyHostKey}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := dialWithin(t, 3*time.Second, func() error {
		_, err := Dial(ctx, addr, opts)
		return err
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	waitFor(t, func() bool { return closedByClient.Load() == 1 }, "the client to close the network connection")
}

// dialWithin calls dial and returns its error,
// failing the test if dial does not return within timeout.
func dialWithin(t *testing.T, timeout time.Duration, dial func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- dial() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("the dial did not return within %s", timeout)
		return nil
	}
}

// TestDialTimeout checks that Options.Timeout limits every connection attempt
// and that a timed out attempt is retried.
func TestDialTimeout(t *testing.T) {
	addr, closedByClient := startHungServer(t)
	opts := &Options{
		Username:        "u",
		Password:        "p",
		HostKeyCallback: AcceptAnyHostKey,
		Timeout:         100 * time.Millisecond,
		ConnectRetries:  1,
		RetryBackoff:    time.Millisecond,
	}
	err := dialWithin(t, 3*time.Second, func() error {
		_, err := Dial(t.Context(), addr, opts)
		return err
	})
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.NotErrorIs(t, err, context.DeadlineExceeded, "the dial ctx did not end")
	waitFor(t, func() bool { return closedByClient.Load() == 2 }, "two timed out attempts")
}

// TestDialContextOption dials through Options.DialContext and checks
// that an IPv6 address without a port is dialed at port 22.
func TestDialContextOption(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	var dialed []string
	opts := s.testOptions()
	opts.HostKeyCallback = AcceptAnyHostKey
	opts.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, network+" "+addr)
		var d net.Dialer
		return d.DialContext(ctx, network, s.addr)
	}
	f, err := Dial(t.Context(), "sftp://u@[::1]", opts)
	require.NoError(t, err)
	defer f.Close()
	assert.Equal(t, []string{"tcp [::1]:22"}, dialed)
	assert.Equal(t, "sftp://u@[::1]", f.Prefix())

	// Reconnects use DialContext too
	first := f.(*fileSystem).currentConn()
	s.killConns()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	_, err = f.Stat("/")
	require.NoError(t, err)
	assert.Equal(t, []string{"tcp [::1]:22", "tcp [::1]:22"}, dialed)
}

// TestCloseClosesSSHConnection checks that Close closes the SSH connection,
// not only the SFTP session.
func TestCloseClosesSSHConnection(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	_, err := f.Stat("/")
	require.NoError(t, err)
	s.waitOpen(t, 1)

	require.NoError(t, f.Close())
	s.waitOpen(t, 0)
	require.NoError(t, f.Close(), "Close is idempotent")
	_, err = f.Stat("/")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
}

// TestReconnect loses the connection: the next operation reconnects,
// calling the credentials callback again, and the old connection is closed.
func TestReconnect(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	var (
		calls atomic.Int64
		mtx   sync.Mutex
		logs  []string
	)
	opts := s.testOptions()
	opts.Credentials = func(*url.URL) (string, string, error) {
		calls.Add(1)
		return testServerUser, testServerPassword, nil
	}
	opts.Logger = fs.LoggerFunc(func(format string, args ...any) {
		mtx.Lock()
		defer mtx.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	f := s.dial(t, opts)
	first := f.currentConn()

	s.killConns()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	_, err := f.Stat("/")
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load(), "credentials callback calls")
	assert.Equal(t, int64(2), s.accepted.Load(), "dials")
	s.waitOpen(t, 1)

	mtx.Lock()
	defer mtx.Unlock()
	prefix := "sftp://u@" + s.addr
	// "Lost" is logged by the goroutine watching the connection
	assert.ElementsMatch(t, []string{
		"Dialed SFTP connection to " + prefix,
		"Lost SFTP connection to " + prefix,
		"Reconnecting SFTP to " + prefix,
		"Dialed SFTP connection to " + prefix,
	}, logs)
}

// TestReconnectFailure checks the error of a failed reconnect.
func TestReconnectFailure(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	proxy := startTestProxy(t, s.addr)
	opts := s.testOptions()
	opts.ConnectRetries = -1
	opts.Timeout = 100 * time.Millisecond
	f, err := Dial(t.Context(), "sftp://u:secret@"+proxy.addr, opts)
	require.NoError(t, err)
	defer f.Close()

	first := f.(*fileSystem).currentConn()
	proxy.setHang(true)
	proxy.kill()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	_, err = f.Stat("/")
	assert.ErrorContains(t, err, "SFTP reconnect failed")
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.NotContains(t, err.Error(), "secret")

	proxy.setHang(false)
	_, err = f.Stat("/")
	assert.NoError(t, err, "the next operation reconnects again")
}

// TestReconnectDoesNotBlock checks that a reconnect to a server that does
// not answer blocks neither Close nor operations with a ctx.
func TestReconnectDoesNotBlock(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	proxy := startTestProxy(t, s.addr)
	opts := s.testOptions()
	opts.Timeout = time.Minute
	f, err := Dial(t.Context(), proxy.addr, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	first := f.(*fileSystem).currentConn()
	proxy.setHang(true)
	proxy.kill()
	waitFor(t, first.isLost, "the client to notice the lost connection")

	statErr := make(chan error, 1)
	go func() {
		_, err := f.Stat("/") // waits for the reconnect without a ctx
		statErr <- err
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = f.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second, "ListDir must stop waiting for the reconnect with its ctx")

	start = time.Now()
	require.NoError(t, f.Close())
	assert.Less(t, time.Since(start), time.Second, "Close must not wait for the reconnect")
	select {
	case err := <-statErr:
		assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	case <-time.After(2 * time.Second):
		t.Fatal("Close must end the reconnect")
	}
}

// TestKeepAlive freezes the connection without closing it, so only the
// keep-alive requests can detect that it is dead.
func TestKeepAlive(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	proxy := startTestProxy(t, s.addr)
	opts := s.testOptions()
	opts.KeepAlive = 200 * time.Millisecond
	f, err := Dial(t.Context(), proxy.addr, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	first := f.(*fileSystem).currentConn()

	// Waiting for answers instead of a fixed time, a slow test runner only
	// closes the connection if it delays an answer by KeepAliveCountMax intervals
	waitFor(t, func() bool { return s.globalRequests.Load() >= 3 }, "three answered keep-alives")
	require.False(t, first.isLost(), "answered keep-alives keep the connection")

	proxy.freeze()
	waitFor(t, first.isLost, "the keep-alive to detect the dead connection")
	_, err = f.Stat("/")
	require.NoError(t, err, "the next operation reconnects")
	assert.NotSame(t, first, f.(*fileSystem).currentConn())
}

// TestKeepAliveSlowAnswer answers every keep-alive request after one and
// a half intervals, like a server whose answer queues behind the data
// of a download on a slow link: that must not close the connection.
func TestKeepAliveSlowAnswer(t *testing.T) {
	const interval = 200 * time.Millisecond
	s := startTestServer(t, testServerConfig{globalRequestDelay: interval * 3 / 2})
	opts := s.testOptions()
	opts.KeepAlive = interval
	f := s.dial(t, opts)
	first := f.currentConn()

	waitFor(t, func() bool { return s.globalRequests.Load() >= 3 || first.isLost() }, "three answered keep-alives")
	require.False(t, first.isLost(), "slow answers must not close the connection")
	_, err := f.Stat("/")
	require.NoError(t, err)
	assert.Same(t, first, f.currentConn())
	assert.Equal(t, int64(1), s.accepted.Load(), "no reconnect")
}

// TestStatusErrorKeepsConnection checks that a status reply of the server
// is never taken for a lost connection, also if its message reads like one
// or pkg/sftp makes io.EOF of it: the reply proves that the connection works,
// and dropping it would fail every open reader and writer of the file system.
func TestStatusErrorKeepsConnection(t *testing.T) {
	failures := map[string]error{
		"/reset.txt": errors.New("write: connection reset by peer"),
		"/pipe.txt":  errors.New("write: broken pipe"),
		"/eof.txt":   io.EOF, // sent as SSH_FX_EOF status, which pkg/sftp returns as io.EOF
	}
	s := startTestServer(t, testServerConfig{fail: func(r *sftp.Request) error {
		if r.Method == "Lstat" {
			return failures[r.Filepath]
		}
		return nil
	}})
	f := s.dial(t, nil)
	writeTestFile(t, f, "/file.txt", "content")
	reader, err := f.OpenReader("/file.txt")
	require.NoError(t, err)
	defer reader.Close()
	first := f.currentConn()

	for name, failure := range failures {
		_, err := f.Stat(name)
		require.Error(t, err, name)
		if failure == io.EOF {
			assert.ErrorIs(t, err, io.EOF, name)
		} else {
			assert.ErrorContains(t, err, failure.Error(), name)
		}
	}
	assert.Same(t, first, f.currentConn(), "a status reply must not drop the connection")
	assert.Equal(t, int64(1), s.accepted.Load(), "no reconnect")
	data, err := io.ReadAll(reader)
	require.NoError(t, err, "the open reader must still work")
	assert.Equal(t, "content", string(data))
}

// TestEOFOfLostConnection checks that io.EOF, which is only a hint for a
// lost connection, still drops a connection that is lost and retries the operation.
func TestEOFOfLostConnection(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	first := f.currentConn()
	calls := 0
	err := f.run(t.Context(), func(c *conn, _ func()) error {
		calls++
		if calls == 1 {
			// The server is gone, like a channel closed under a request
			s.killConns()
			waitFor(t, c.isLost, "the client to notice the lost connection")
			return io.EOF
		}
		_, err := f.stat(c.sftp, "/")
		return err
	})
	require.NoError(t, err, "retried with a new connection")
	assert.Equal(t, 2, calls)
	assert.NotSame(t, first, f.currentConn())
}

// TestDialHostKeyAlgorithms dials a server with an ECDSA and an ED25519
// host key with a HostKeyCallback that only knows the ED25519 key, like a
// known_hosts file with only that key: x/crypto negotiates ECDSA first and
// fails with a key mismatch unless Options.HostKeyAlgorithms asks for
// ED25519 (golang/go#29286).
func TestDialHostKeyAlgorithms(t *testing.T) {
	s := startTestServer(t, testServerConfig{ecdsaHostKey: true})
	opts := s.testOptions()
	opts.ConnectRetries = -1
	_, err := Dial(t.Context(), s.addr, opts)
	require.ErrorContains(t, err, "host key mismatch", "x/crypto prefers the ECDSA key of the server")

	opts.HostKeyAlgorithms = []string{ssh.KeyAlgoED25519}
	f, err := Dial(t.Context(), s.addr, opts)
	require.NoError(t, err, "HostKeyAlgorithms must reach the ssh.ClientConfig")
	opts.HostKeyAlgorithms[0] = ssh.KeyAlgoECDSA256 // must not change the reconnects of f
	first := f.(*fileSystem).currentConn()
	s.killConns()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	_, err = f.Stat("/")
	require.NoError(t, err, "the reconnect uses the HostKeyAlgorithms of the dial")
	require.NoError(t, f.Close())
}

// TestPasswordNotInErrorsAndLogs dials an address with a password,
// which must never appear in errors, logs or File URIs.
func TestPasswordNotInErrorsAndLogs(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	var (
		mtx  sync.Mutex
		logs strings.Builder
	)
	opts := &Options{
		HostKeyCallback: ssh.FixedHostKey(s.hostKey),
		RetryBackoff:    time.Millisecond,
		KeepAlive:       -1,
		Logger: fs.LoggerFunc(func(format string, args ...any) {
			mtx.Lock()
			defer mtx.Unlock()
			fmt.Fprintf(&logs, format+"\n", args...)
		}),
	}
	address := "sftp://u:" + testServerPassword + "@" + s.addr
	f, err := Dial(t.Context(), address, opts)
	require.NoError(t, err, "the password of the address is used")
	defer f.Close()
	writeTestFile(t, f.(*fileSystem), "/file.txt", "content")

	info, err := f.Stat("/file.txt")
	require.NoError(t, err)
	_, missingErr := f.Stat("/missing.txt")
	require.ErrorIs(t, missingErr, os.ErrNotExist)
	var listed []string
	require.NoError(t, f.ListDir(t.Context(), "/", nil, func(info *fs.FileInfo) error {
		listed = append(listed, string(info.File))
		return nil
	}))

	first := f.(*fileSystem).currentConn()
	s.killConns()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	_, err = f.Stat("/")
	require.NoError(t, err)

	// A refused dial
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	refusedAddr := listener.Addr().String()
	require.NoError(t, listener.Close())
	opts.ConnectRetries = 1
	_, dialErr := Dial(t.Context(), "sftp://u:"+testServerPassword+"@"+refusedAddr, opts)
	require.Error(t, dialErr)

	mtx.Lock()
	defer mtx.Unlock()
	for what, text := range map[string]string{
		"String":            f.String(),
		"Prefix":            f.Prefix(),
		"FileInfo.File":     string(info.File),
		"ListDir":           strings.Join(listed, " "),
		"error":             missingErr.Error(),
		"dial error":        dialErr.Error(),
		"log":               logs.String(),
		"RootDir":           string(f.RootDir()),
		"JoinCleanFile":     string(f.(*fileSystem).JoinCleanFile("x")),
		"registered prefix": fmt.Sprint(f.(*fileSystem).PrefixAliases()),
	} {
		assert.NotContains(t, text, ":"+testServerPassword+"@", "%s must not contain the password: %s", what, text)
	}
	assert.Contains(t, logs.String(), "Lost SFTP connection to sftp://u@"+s.addr)
}

func TestDialAndRegister(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	prefix := "sftp://u@" + s.addr
	require.NoError(t, DialAndRegister(t.Context(), prefix, s.testOptions()))
	registered := fs.GetFileSystemByPrefixOrNil(prefix)
	require.NotNil(t, registered)
	t.Cleanup(func() { _ = registered.Close() })

	data := []byte("content")
	require.NoError(t, fs.File(prefix+"/file.txt").WriteAll(t.Context(), data))
	read, err := fs.File(prefix + "/file.txt").ReadAll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, data, read)

	err = DialAndRegister(t.Context(), prefix, s.testOptions())
	assert.ErrorIs(t, err, fs.ErrAlreadyRegistered)
	s.waitOpen(t, 1) // the second connection is closed

	require.NoError(t, registered.Close())
	assert.False(t, fs.IsRegistered(registered), "Close unregisters")
	s.waitOpen(t, 0)
}

func TestEnsureRegistered(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	prefix := "sftp://u@" + s.addr
	opts := &Options{HostKeyCallback: ssh.FixedHostKey(s.hostKey), KeepAlive: -1}

	// An address with credentials registers the prefix without the password
	free1, err := EnsureRegistered(t.Context(), "sftp://u:"+testServerPassword+"@"+s.addr+"/some/file.txt", opts)
	require.NoError(t, err)
	free2, err := EnsureRegistered(t.Context(), prefix, s.testOptions())
	require.NoError(t, err)
	assert.Equal(t, int64(1), s.accepted.Load(), "one shared connection")

	registered := fs.GetFileSystemByPrefixOrNil(prefix)
	require.NotNil(t, registered)
	assert.Equal(t, "SFTP "+prefix, registered.String())
	require.NoError(t, fs.File(prefix+"/file.txt").WriteAll(t.Context(), []byte("content")))

	require.NoError(t, free1())
	require.NoError(t, free1(), "free is idempotent")
	assert.True(t, fs.IsRegistered(registered), "still referenced")
	require.NoError(t, free2())
	assert.False(t, fs.IsRegistered(registered), "the last free unregisters")
	s.waitOpen(t, 0)
	_, err = registered.Stat("/")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed, "the last free closes")

	// A file system registered by DialAndRegister is never closed by free
	require.NoError(t, DialAndRegister(t.Context(), prefix, s.testOptions()))
	registered = fs.GetFileSystemByPrefixOrNil(prefix)
	t.Cleanup(func() { _ = registered.Close() })
	free3, err := EnsureRegistered(t.Context(), prefix, s.testOptions())
	require.NoError(t, err)
	require.NoError(t, free3())
	assert.True(t, fs.IsRegistered(registered))
	_, err = registered.Stat("/")
	assert.NoError(t, err)

	// A failed dial
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	refusedAddr := listener.Addr().String()
	require.NoError(t, listener.Close())
	free4, err := EnsureRegistered(t.Context(), "sftp://u@"+refusedAddr, s.testOptions())
	require.Error(t, err)
	assert.Nil(t, fs.GetFileSystemByPrefixOrNil("sftp://u@"+refusedAddr))
	assert.NoError(t, free4())
}

// TestEnsureRegisteredConcurrent races EnsureRegistered and free:
// a file system that a caller got from EnsureRegistered must not be
// closed before the caller freed it, and the last free unregisters it.
func TestEnsureRegisteredConcurrent(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	prefix := "sftp://u@" + s.addr
	var (
		wg        sync.WaitGroup
		closedErr atomic.Int64
	)
	for range 8 {
		wg.Go(func() {
			for range 25 {
				free, err := EnsureRegistered(t.Context(), prefix, s.testOptions())
				if !assert.NoError(t, err) {
					return
				}
				if f := fs.GetFileSystemByPrefixOrNil(prefix); f != nil {
					if _, err := f.Stat("/"); errors.Is(err, fs.ErrFileSystemClosed) {
						closedErr.Add(1)
					}
				}
				assert.NoError(t, free())
			}
		})
	}
	wg.Wait()
	assert.Zero(t, closedErr.Load(), "file systems closed while referenced")
	assert.Nil(t, fs.GetFileSystemByPrefixOrNil(prefix), "registered after all frees")
	s.waitOpen(t, 0)
}

func TestIsConnectionError(t *testing.T) {
	assert.False(t, isConnectionError(nil))
	assert.False(t, isConnectionError(context.Canceled))
	assert.False(t, isConnectionError(context.DeadlineExceeded), "context.DeadlineExceeded is a net.Error, but no connection error")
	assert.False(t, isConnectionError(fmt.Errorf("listing: %w", context.DeadlineExceeded)))
	assert.False(t, isConnectionError(callbackError{&net.OpError{Op: "write", Err: errors.New("broken pipe")}}))
	assert.False(t, isConnectionError(os.ErrNotExist))
	assert.False(t, isConnectionError(fs.ErrFileSystemClosed))
	assert.True(t, isConnectionError(os.ErrDeadlineExceeded), "a timed out connection attempt")
	assert.True(t, isConnectionError(fmt.Errorf("read: %w", net.ErrClosed)))
	assert.True(t, isConnectionError(errors.New("write: connection reset by peer")))
	assert.True(t, isConnectionError(sftp.ErrSSHFxConnectionLost))
	assert.False(t, isConnectionError(fmt.Errorf("write: connection reset by peer: %w", &sftp.StatusError{Code: uint32(sftp.ErrSSHFxFailure)})),
		"a status reply of the server is no connection error, whatever its message says")
}
