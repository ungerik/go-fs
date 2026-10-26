package sftpfs

import (
	"context"
	"net"
	"net/url"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ungerik/go-fs"
)

// Defaults for the Options fields that are zero.
const (
	// DefaultConnectRetries is the number of retries
	// of a failed connection attempt, see Options.ConnectRetries.
	DefaultConnectRetries = 3

	// DefaultRetryBackoff is the wait before the first retry
	// of a failed connection attempt, see Options.RetryBackoff.
	DefaultRetryBackoff = 100 * time.Millisecond

	// DefaultTimeout limits every connection attempt, see Options.Timeout.
	DefaultTimeout = 30 * time.Second

	// DefaultKeepAlive is the interval of the SSH keep-alive requests,
	// see Options.KeepAlive.
	DefaultKeepAlive = 30 * time.Second
)

// KeepAliveCountMax is the number of keep-alive intervals in a row
// that may end without an answer of the server before the connection
// is closed, like ServerAliveCountMax of OpenSSH. See Options.KeepAlive.
const KeepAliveCountMax = 3

// Options configure the connection of a file system
// dialed by Dial, DialAndRegister or EnsureRegistered.
//
// HostKeyCallback is required, so a nil *Options,
// which stands for the zero value, is not enough to dial.
type Options struct {
	// Username is the SSH login name.
	// If empty, the username of the address is used.
	// The username is part of the URI prefix of the file system.
	Username string

	// Password is used for SSH password authentication if not empty.
	// If empty, the password of the address is used.
	Password string

	// Credentials, if not nil, is called with the parsed address by every
	// Dial, DialAndRegister and EnsureRegistered call, because the username
	// is part of the URI prefix, and again before every reconnect,
	// so it can return rotated credentials.
	// A non-empty username or password it returns
	// is used instead of Username or Password.
	// The username should be the same for every call,
	// because the URI prefix of the file system is made from
	// the username of the first call.
	Credentials CredentialsCallback

	// Auth are SSH authentication methods that are tried before the password,
	// like ssh.PublicKeys, ssh.PublicKeysCallback with the signers of an SSH agent
	// (package golang.org/x/crypto/ssh/agent) or ssh.KeyboardInteractive.
	Auth []ssh.AuthMethod

	// HostKeyCallback verifies the host key of the server.
	// It is required, dialing fails if it is nil.
	// Use the callback of knownhosts.New (package golang.org/x/crypto/ssh/knownhosts)
	// or ssh.FixedHostKey. AcceptAnyHostKey disables the verification
	// and is only meant for tests.
	HostKeyCallback ssh.HostKeyCallback

	// HostKeyAlgorithms are the host key algorithms that the client accepts,
	// in the order of preference, like ssh.KeyAlgoED25519,
	// see ssh.ClientConfig.HostKeyAlgorithms.
	// If empty, the defaults of golang.org/x/crypto/ssh are used,
	// which prefer ECDSA and RSA keys to ED25519 keys.
	//
	// Set it if HostKeyCallback only knows some keys of the server,
	// like a known_hosts file with only the ED25519 key of a server that
	// also has an ECDSA key: else the server presents its ECDSA key and the
	// dial fails with a key mismatch (golang/go#29286). The callback of
	// knownhosts.New has no list of the algorithms of a host, the
	// HostKeyAlgorithms method of github.com/skeema/knownhosts has one.
	HostKeyAlgorithms []string

	// Logger, if not nil, logs connection events like dialing, retries,
	// lost connections and reconnects. Passwords are never logged.
	Logger fs.Logger

	// ConnectRetries is the number of retries of a failed connection attempt,
	// so a dial makes up to 1+ConnectRetries attempts.
	// Only network errors and timeouts are retried,
	// authentication and host key errors are not.
	// Zero means DefaultConnectRetries, a negative value disables retries.
	ConnectRetries int

	// RetryBackoff is the wait before the first retry of a failed
	// connection attempt, it doubles for every further retry.
	// Zero means DefaultRetryBackoff, a negative value retries without waiting.
	RetryBackoff time.Duration

	// Timeout limits every connection attempt: the network connection,
	// the SSH handshake with the authentication and the start of the SFTP session.
	// The context passed to Dial, DialAndRegister or EnsureRegistered
	// limits the whole dial in addition.
	// Zero means DefaultTimeout, a negative value disables the limit.
	Timeout time.Duration

	// KeepAlive is the interval of SSH keep-alive requests that detect a
	// connection that is dead without the network connection knowing it,
	// for example after a NAT router or a firewall dropped it.
	// At most one request is outstanding. A connection that leaves
	// KeepAliveCountMax intervals in a row without an answer is closed,
	// and the next operation reconnects. Slower answers keep the
	// connection, like on a slow link busy with a download.
	// Zero means DefaultKeepAlive, a negative value disables keep-alives.
	KeepAlive time.Duration

	// DialContext, if not nil, opens the network connection to the server,
	// for example through a proxy. It is called with the network "tcp"
	// and the address of the server as "host:port".
	// If nil, a net.Dialer is used.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// AllowBackslashInNames lets paths contain backslashes, which are
	// an error wrapping os.ErrInvalid by default, also in the target
	// of a symbolic link. For the separator "/" of SFTP a backslash is
	// part of a name, but an SFTP server on Windows, like OpenSSH for
	// Windows, takes it for a separator: a name like `..\x` would reach
	// a file outside of the directory that it is meant to be in, also for
	// fs.SubFileSystem and fs.CopyRecursive. Set it only for servers
	// that store backslashes in names, like servers on Unix.
	AllowBackslashInNames bool
}

// effective returns the effective value of the Options field v
// with the default def: def for zero, zero for a negative v.
func effective[T int | time.Duration](v, def T) T {
	switch {
	case v == 0:
		return def
	case v < 0:
		return 0
	}
	return v
}

// connectRetries returns the effective Options.ConnectRetries.
func (o *Options) connectRetries() int { return effective(o.ConnectRetries, DefaultConnectRetries) }

// retryBackoff returns the effective Options.RetryBackoff.
func (o *Options) retryBackoff() time.Duration { return effective(o.RetryBackoff, DefaultRetryBackoff) }

// timeout returns the effective Options.Timeout, zero for no timeout.
func (o *Options) timeout() time.Duration { return effective(o.Timeout, DefaultTimeout) }

// keepAlive returns the effective Options.KeepAlive, zero for no keep-alives.
func (o *Options) keepAlive() time.Duration { return effective(o.KeepAlive, DefaultKeepAlive) }

// dialContext returns the effective Options.DialContext.
func (o *Options) dialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if o.DialContext != nil {
		return o.DialContext
	}
	var d net.Dialer
	return d.DialContext
}

// CredentialsCallback returns the username and password for an SFTP
// connection to the server of the passed address, see Options.Credentials.
type CredentialsCallback func(address *url.URL) (username, password string, err error)

// Password returns a CredentialsCallback that returns the username
// of the address that is passed to the callback together with password.
func Password(password string) CredentialsCallback {
	return func(address *url.URL) (string, string, error) {
		return address.User.Username(), password, nil
	}
}

// UsernameAndPassword returns a CredentialsCallback
// that always returns username and password.
func UsernameAndPassword(username, password string) CredentialsCallback {
	return func(*url.URL) (string, string, error) {
		return username, password, nil
	}
}

// AcceptAnyHostKey is an ssh.HostKeyCallback that accepts
// any host key, which disables the verification of the server.
// Only use it for tests, see Options.HostKeyCallback.
func AcceptAnyHostKey(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return nil
}
