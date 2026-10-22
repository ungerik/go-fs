package smbfs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/cloudsoda/go-smb2"

	"github.com/ungerik/go-fs/fsimpl"
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
	// Zero or a negative value means 30 seconds. The context passed to Dial or to
	// an operation that reconnects can only shorten it.
	Timeout time.Duration

	// DialContext dials the TCP connection. If nil, a net.Dialer is used.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// dialConfig is the parsed address of a file system
// together with its options, used for every dial.
type dialConfig struct {
	opts     Options
	hostPort string // host:port to dial
	share    string
	username string
	password string
	prefix   string   // URI prefix like "smb://user@host/share", never with a password
	aliases  []string // the prefix with the default port
}

// parseAddress parses an address of the form [smb://][user[:password]@]host[:port]/share.
// Error messages never contain the password.
func parseAddress(address string, opts *Options) (*dialConfig, error) {
	if !strings.Contains(address, "://") {
		address = Prefix + address
	}
	// url.Parse ends the user info at the first '/', '?' or '#'. An '@' after
	// it means that the password contains one of them unencoded, and url.Parse
	// would take parts of the password for the port, share, query or fragment.
	_, rest, _ := strings.Cut(address, "://")
	if end := strings.IndexAny(rest, "/?#"); end >= 0 && strings.Contains(rest[end:], "@") {
		return nil, fmt.Errorf("smbfs: invalid address %s: '@' after the host, percent-encode a '/', '?' or '#' of the username or password and an '@' of the share", fsimpl.RedactAddress(address))
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, invalidAddressErr(address)
	}
	if u.Scheme != "smb" {
		return nil, fmt.Errorf("smbfs: not an smb:// address: %s", fsimpl.RedactAddress(address))
	}
	share := strings.Trim(u.Path, "/")
	if u.Hostname() == "" || share == "" || strings.Contains(share, "/") {
		return nil, fmt.Errorf("smbfs: address must have the form [smb://][user[:password]@]host[:port]/share: %s", fsimpl.RedactAddress(address))
	}
	cfg := &dialConfig{share: share}
	if opts != nil {
		cfg.opts = *opts
	}
	cfg.username = cmp.Or(cfg.opts.Username, u.User.Username())
	cfg.password = cfg.opts.Password
	if cfg.password == "" {
		cfg.password, _ = u.User.Password()
	}

	port := cmp.Or(u.Port(), defaultPort)
	// u.Hostname has no brackets and no escapes, JoinHostPort adds the brackets
	cfg.hostPort = net.JoinHostPort(u.Hostname(), port)

	host := u.Host
	switch u.Port() {
	case "":
		host = strings.TrimSuffix(host, ":") // "host:" has an empty port
	case defaultPort:
		host = strings.TrimSuffix(host, ":"+defaultPort)
	}
	// url.Parse unescaped the zone of an IPv6 address
	// like "[fe80::1%25eth0]", the URI prefix needs the escape
	host = strings.ReplaceAll(host, "%", "%25")
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

// invalidAddressErr returns the error for an address that url.Parse rejects.
// The errors of url.Parse repeat parts of the address, which can be parts
// of the password, so the reason is the error of parsing the address with
// the redacted password. If that one is valid, the user info is invalid.
func invalidAddressErr(address string) error {
	redacted := fsimpl.RedactAddress(address)
	_, reason := url.Parse(redacted)
	if urlErr, ok := errors.AsType[*url.Error](reason); ok {
		reason = urlErr.Err // without the repeated address
	}
	if reason == nil {
		reason = errors.New("invalid username or password, special characters must be percent-encoded")
	}
	return fmt.Errorf("smbfs: invalid address %s: %w", redacted, reason)
}
