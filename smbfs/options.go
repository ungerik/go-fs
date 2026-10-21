package smbfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"
)

const (
	defaultPort = "445"

	// defaultTimeout is used for Options.Timeout if it is zero
	defaultTimeout = 30 * time.Second
)

// Options for dialing an SMB share. A nil *Options uses the defaults.
type Options struct {
	// Username for the NTLMv2 authentication.
	// It overrides the username of the address.
	// Without a username from the address or the options and without
	// a password, the session is anonymous (an NTLM null session),
	// which servers allow only for shares with guest access.
	// Some servers need the username "Guest" instead.
	Username string

	// Password for the NTLMv2 authentication. It overrides the password
	// of the address and is also used with a username from the address.
	// Passwords are never part of URIs or error messages.
	Password string

	// Domain and Workstation are optional NTLM parameters.
	Domain      string
	Workstation string

	// Timeout limits dialing a session: the TCP connect, the SMB
	// negotiation, the authentication and mounting the share.
	// Zero means 30 seconds. The context passed to Dial or to
	// an operation that reconnects can only shorten it.
	Timeout time.Duration

	// DialContext dials the TCP connection. If nil, a net.Dialer is used.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// dialConfig is the parsed address of a file system
// together with its options, used for every dial.
type dialConfig struct {
	opts      Options
	hostPort  string // host:port to dial
	share     string
	username  string
	password  string
	prefix    string   // URI prefix like "smb://user@host/share", never with a password
	aliases   []string // the prefix with the default port
	anonymous bool
}

// parseAddress parses an address of the form [smb://][user[:password]@]host[:port]/share.
// Error messages never contain the password.
func parseAddress(address string, opts *Options) (*dialConfig, error) {
	if !strings.Contains(address, "://") {
		address = Prefix + address
	}
	u, err := url.Parse(address)
	if err != nil {
		// The url.Error repeats the address with the password
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("smbfs: invalid address %s: %w", redactAddress(address), err)
	}
	if u.Scheme != "smb" {
		return nil, fmt.Errorf("smbfs: not an smb:// address: %s", redactAddress(address))
	}
	share := strings.Trim(u.Path, "/")
	if u.Hostname() == "" || share == "" || strings.Contains(share, "/") {
		return nil, fmt.Errorf("smbfs: address must have the form [smb://][user[:password]@]host[:port]/share: %s", redactAddress(address))
	}
	cfg := &dialConfig{share: share}
	if opts != nil {
		cfg.opts = *opts
	}
	cfg.username = cfg.opts.Username
	if cfg.username == "" {
		cfg.username = u.User.Username()
	}
	cfg.password = cfg.opts.Password
	if cfg.password == "" {
		cfg.password, _ = u.User.Password()
	}
	cfg.anonymous = cfg.username == "" && cfg.password == ""

	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	cfg.hostPort = net.JoinHostPort(u.Hostname(), port)

	host := u.Host
	if u.Port() == defaultPort {
		host = strings.TrimSuffix(host, ":"+defaultPort)
	}
	userPrefix := Prefix
	if cfg.username != "" {
		userPrefix += url.User(cfg.username).String() + "@"
	}
	cfg.prefix = userPrefix + host + "/" + share
	if port == defaultPort {
		cfg.aliases = []string{userPrefix + host + ":" + defaultPort + "/" + share}
	}
	return cfg, nil
}

// initiator returns the NTLM initiator for a session.
func (cfg *dialConfig) initiator() *smb2.NTLMInitiator {
	return &smb2.NTLMInitiator{
		User:        cfg.username,
		Password:    cfg.password,
		Domain:      cfg.opts.Domain,
		Workstation: cfg.opts.Workstation,
	}
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
