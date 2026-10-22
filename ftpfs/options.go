package ftpfs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultPort     = "21"
	implicitTLSPort = "990"

	// defaultTimeout is used for Options.Timeout if it is zero
	defaultTimeout = 30 * time.Second

	// anonymousUser is the username for logins without a username,
	// as defined for ftp URLs by RFC 1738
	anonymousUser = "anonymous"
)

// CredentialsCallback returns the username and password for a login to the
// server of the passed URL, which is the parsed address passed to Dial,
// DialAndRegister or EnsureRegistered including its user info.
type CredentialsCallback func(*url.URL) (username, password string, err error)

// Password returns a CredentialsCallback that returns the username
// of the URL that is passed to the callback together with password.
func Password(password string) CredentialsCallback {
	return func(u *url.URL) (string, string, error) {
		return u.User.Username(), password, nil
	}
}

// UsernameAndPassword returns a CredentialsCallback that always returns
// the passed username and password.
func UsernameAndPassword(username, password string) CredentialsCallback {
	return func(*url.URL) (string, string, error) {
		return username, password, nil
	}
}

// Options for dialing FTP(S) connections. A nil *Options uses the defaults.
type Options struct {
	// Username for the login. It overrides the username of the address.
	// Without a username from the address, Username or Credentials,
	// the login is anonymous as defined for ftp URLs by RFC 1738:
	// the username "anonymous" with Password, or "anonymous" if
	// there is no password either. The URI prefix of an anonymous
	// file system has no user, for example "ftp://example.com".
	Username string

	// Password for the login. It overrides the password of the address.
	// Passwords are never part of URIs, error messages or the DebugOut log.
	Password string

	// Credentials, if not nil, is called to get the username and password
	// instead of using Username, Password and the user info of the address.
	// It is called by every Dial, DialAndRegister and EnsureRegistered call,
	// because the username is part of the URI prefix, for every reconnect
	// after a connection loss, and for the dedicated connection of every
	// OpenReader, so it should be cheap or cache its result.
	Credentials CredentialsCallback

	// TLSConfig is used for FTPS connections. If nil, a configuration
	// that verifies the server certificate for the dialed host is used.
	// A ServerName is filled in from the dialed host if empty, and a
	// ClientSessionCache is added if missing, because many servers
	// require the data connections to resume the TLS session of the
	// control connection.
	TLSConfig *tls.Config

	// InsecureSkipVerify disables the verification of the server
	// certificate, also for a non-nil TLSConfig.
	InsecureSkipVerify bool

	// Timeout limits dialing a connection: the TCP connect, the TLS
	// handshake, the server greeting and the login, and also the TCP
	// connect of every data connection. Zero means 30 seconds.
	// The context passed to Dial or to an operation can only shorten it.
	Timeout time.Duration

	// DialContext dials the control and data connections.
	// If nil, a net.Dialer is used.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// DisableEPSV disables the EPSV command (RFC 2428) for data
	// connections, so that PASV is used right away for servers that
	// mishandle EPSV. By default EPSV is tried first and PASV is used
	// if the server rejects it. Either way the data connections go to
	// the host of the control connection, never to the address of a
	// PASV reply, which could point to another or an internal host.
	DisableEPSV bool

	// DebugOut receives the FTP protocol log if not nil.
	// The argument of the PASS command is replaced with "****".
	DebugOut io.Writer
}

// dialConfig is the parsed address of a file system
// together with its options, used for every dial.
type dialConfig struct {
	opts     Options
	url      *url.URL // the address with its user info, passed to Options.Credentials
	hostname string   // without brackets for IPv6
	port     string
	secure   bool
	prefix   string // URI prefix like "ftp://user@host:port", never with a password

	// username and password of the first dial
	username string
	password string

	tlsConfig *tls.Config // shared by all connections for TLS session resumption, nil for FTP
}

// parseAddress parses an address of the form [ftp[s]://][user[:password]@]host[:port]
// and resolves the credentials of the first dial.
// Error messages never contain the password.
func parseAddress(address string, opts *Options) (*dialConfig, error) {
	if !strings.HasPrefix(address, Prefix) && !strings.HasPrefix(address, PrefixTLS) {
		if strings.Contains(address, "://") {
			return nil, fmt.Errorf("ftpfs: not an ftp:// or ftps:// address: %s", redactAddress(address))
		}
		address = Prefix + address
	}
	u, err := url.Parse(address)
	if err != nil {
		// The url.Error repeats the address with the password
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("ftpfs: invalid address %s: %w", redactAddress(address), err)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("ftpfs: address without host: %s", redactAddress(address))
	}
	cfg := &dialConfig{
		url:      u,
		hostname: u.Hostname(),
		port:     u.Port(),
		secure:   u.Scheme == "ftps",
	}
	if opts != nil {
		cfg.opts = *opts
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	var anonymous bool
	cfg.username, cfg.password, anonymous, err = cfg.credentials()
	if err != nil {
		return nil, err
	}

	host := u.Host
	if u.Port() == defaultPort {
		host = strings.TrimSuffix(host, ":"+defaultPort)
	}
	cfg.prefix = u.Scheme + "://"
	if !anonymous {
		cfg.prefix += url.User(cfg.username).String() + "@"
	}
	cfg.prefix += host

	if cfg.secure {
		cfg.tlsConfig = cfg.newTLSConfig()
	}
	return cfg, nil
}

// credentials returns the username and password for a login, from the
// Credentials callback if set, else from the options and the address.
// Without a username it returns the anonymous login and anonymous true.
// Both are checked for characters that would corrupt FTP commands.
func (cfg *dialConfig) credentials() (username, password string, anonymous bool, err error) {
	if cfg.opts.Credentials != nil {
		username, password, err = cfg.opts.Credentials(cfg.url)
		if err != nil {
			return "", "", false, err
		}
	} else {
		username = cfg.opts.Username
		if username == "" {
			username = cfg.url.User.Username()
		}
		password = cfg.opts.Password
		if password == "" {
			password, _ = cfg.url.User.Password()
		}
	}
	if username == "" {
		username, anonymous = anonymousUser, true
		if password == "" {
			password = anonymousUser
		}
	}
	if err = checkCommandArg("username", username, false); err != nil {
		return "", "", false, err
	}
	if err = checkCommandArg("password", password, true); err != nil {
		return "", "", false, err
	}
	return username, password, anonymous, nil
}

// newTLSConfig returns the TLS configuration for all connections of the
// file system. The connections share its ClientSessionCache, so that data
// connections can resume the TLS session of the control connection.
func (cfg *dialConfig) newTLSConfig() *tls.Config {
	tlsConfig := cfg.opts.TLSConfig.Clone()
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = cfg.hostname
	}
	if tlsConfig.ClientSessionCache == nil {
		tlsConfig.ClientSessionCache = tls.NewLRUClientSessionCache(0)
	}
	if cfg.opts.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true //#nosec G402 -- explicitly requested via Options.InsecureSkipVerify
	}
	return tlsConfig
}

// implicitTLS reports whether the connection uses TLS from the start
// instead of upgrading the control connection with AUTH TLS.
func (cfg *dialConfig) implicitTLS() bool {
	return cfg.secure && cfg.port == implicitTLSPort
}

// timeout returns Options.Timeout or the default.
func (cfg *dialConfig) timeout() time.Duration {
	if cfg.opts.Timeout > 0 {
		return cfg.opts.Timeout
	}
	return defaultTimeout
}

// dialContext dials a TCP connection with Options.DialContext or a net.Dialer.
func (cfg *dialConfig) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if cfg.opts.DialContext != nil {
		return cfg.opts.DialContext(ctx, network, addr)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, addr)
}

// checkCommandArg returns an error wrapping os.ErrInvalid if s contains
// a CR, LF or NUL character. Such a character would end the FTP command
// that s is an argument of and inject the rest as another command.
// The value is not part of the error message if it is secret.
func checkCommandArg(what, s string, secret bool) error {
	if !strings.ContainsAny(s, "\r\n\x00") {
		return nil
	}
	if secret {
		return fmt.Errorf("%w: ftpfs %s contains a CR, LF or NUL character", os.ErrInvalid, what)
	}
	return fmt.Errorf("%w: ftpfs %s %q contains a CR, LF or NUL character", os.ErrInvalid, what, s)
}

// redactAddress returns address with the password of its user info
// replaced by "xxxxx". It works on addresses that url.Parse rejects.
func redactAddress(address string) string {
	authority := address
	scheme, rest, found := strings.Cut(address, "://")
	if found {
		authority = rest
	}
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return address
	}
	user, _, hasPassword := strings.Cut(authority[:at], ":")
	if !hasPassword {
		return address
	}
	redacted := user + ":xxxxx" + authority[at:]
	if found {
		return scheme + "://" + redacted + rest[len(authority):]
	}
	return redacted + address[len(authority):]
}
