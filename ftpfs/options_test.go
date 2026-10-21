package ftpfs

import (
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPassword verifies that the Password callback returns the unescaped
// username of the URL. It returned the escaped user info, which also
// contained an embedded password.
func TestPassword(t *testing.T) {
	u, err := url.Parse("ftp://first.last%40example.com@host")
	require.NoError(t, err)
	user, pass, err := Password("pw")(u)
	require.NoError(t, err)
	assert.Equal(t, "first.last@example.com", user)
	assert.Equal(t, "pw", pass)

	u, err = url.Parse("ftp://user:secret@host")
	require.NoError(t, err)
	user, _, err = Password("pw")(u)
	require.NoError(t, err)
	assert.Equal(t, "user", user)

	user, pass, err = UsernameAndPassword("u", "p")(u)
	require.NoError(t, err)
	assert.Equal(t, "u", user)
	assert.Equal(t, "p", pass)
}

// Test_parseAddress checks the URI prefix, host, port and credentials
// that an address and the options result in.
func Test_parseAddress(t *testing.T) {
	for _, tc := range []struct {
		address  string
		opts     *Options
		prefix   string
		hostname string
		port     string
		user     string
		password string
		secure   bool
	}{
		{address: "example.com", prefix: "ftp://example.com", hostname: "example.com", port: "21", user: "anonymous", password: "anonymous"},
		{address: "ftp://example.com:21", prefix: "ftp://example.com", hostname: "example.com", port: "21", user: "anonymous", password: "anonymous"},
		{address: "ftp://example.com", opts: &Options{Password: "me@example.com"}, prefix: "ftp://example.com", hostname: "example.com", port: "21", user: "anonymous", password: "me@example.com"},
		{address: "ftp://anonymous@example.com", prefix: "ftp://anonymous@example.com", hostname: "example.com", port: "21", user: "anonymous", password: ""},
		{address: "ftp://erik:pw@example.com:2121", prefix: "ftp://erik@example.com:2121", hostname: "example.com", port: "2121", user: "erik", password: "pw"},
		{address: "ftp://erik:pw@example.com", opts: &Options{Password: "override"}, prefix: "ftp://erik@example.com", hostname: "example.com", port: "21", user: "erik", password: "override"},
		{address: "ftp://erik@example.com", opts: &Options{Username: "bob", Password: "pw"}, prefix: "ftp://bob@example.com", hostname: "example.com", port: "21", user: "bob", password: "pw"},
		{address: "ftp://first.last%40example.com@example.com", opts: &Options{Password: "pw"}, prefix: "ftp://first.last%40example.com@example.com", hostname: "example.com", port: "21", user: "first.last@example.com", password: "pw"},
		{address: "ftp://alice@example.com", opts: &Options{Credentials: UsernameAndPassword("bob", "cb")}, prefix: "ftp://bob@example.com", hostname: "example.com", port: "21", user: "bob", password: "cb"},
		{address: "ftps://erik@example.com", opts: &Options{Password: "pw"}, prefix: "ftps://erik@example.com", hostname: "example.com", port: "21", user: "erik", password: "pw", secure: true},
		{address: "ftps://erik@example.com:990", opts: &Options{Password: "pw"}, prefix: "ftps://erik@example.com:990", hostname: "example.com", port: "990", user: "erik", password: "pw", secure: true},
		// IPv6 without a port got the default port appended to the brackets: [[::1]]:21
		{address: "ftp://u@[::1]", opts: &Options{Password: "pw"}, prefix: "ftp://u@[::1]", hostname: "::1", port: "21", user: "u", password: "pw"},
		{address: "ftp://u@[::1]:21", opts: &Options{Password: "pw"}, prefix: "ftp://u@[::1]", hostname: "::1", port: "21", user: "u", password: "pw"},
		{address: "ftp://u@[fe80::1]:2121", opts: &Options{Password: "pw"}, prefix: "ftp://u@[fe80::1]:2121", hostname: "fe80::1", port: "2121", user: "u", password: "pw"},
	} {
		t.Run(tc.address, func(t *testing.T) {
			cfg, err := parseAddress(tc.address, tc.opts)
			require.NoError(t, err)
			assert.Equal(t, tc.prefix, cfg.prefix, "prefix")
			assert.Equal(t, tc.hostname, cfg.hostname, "hostname")
			assert.Equal(t, tc.port, cfg.port, "port")
			assert.Equal(t, tc.user, cfg.username, "username")
			assert.Equal(t, tc.password, cfg.password, "password")
			assert.Equal(t, tc.secure, cfg.secure, "secure")
			assert.Equal(t, tc.secure, cfg.tlsConfig != nil, "TLS config for FTPS")
			assert.Equal(t, tc.port == implicitTLSPort, cfg.implicitTLS(), "implicit TLS")
		})
	}
}

// Test_parseAddress_Errors verifies that invalid addresses and credentials
// are rejected without the password in the error message.
func Test_parseAddress_Errors(t *testing.T) {
	for _, tc := range []struct {
		address string
		opts    *Options
		target  error
	}{
		{address: "sftp://u:S3cret@host"},
		{address: "ftp://u:S3cret@bad host"},
		{address: "ftp://u:S3cret@host:port"},
		{address: "ftp://u:S3cret@/path"},
		{address: "ftp://u@host", opts: &Options{Password: "S3cret\r\nDELE /x"}, target: os.ErrInvalid},
		{address: "ftp://u@host", opts: &Options{Username: "u\r\nDELE /x", Password: "S3cret"}, target: os.ErrInvalid},
		{address: "ftp://u@host", opts: &Options{Password: "S3cret\x00"}, target: os.ErrInvalid},
		{address: "ftp://u@host", opts: &Options{Credentials: func(*url.URL) (string, string, error) {
			return "", "", errors.New("no credentials")
		}}},
	} {
		_, err := parseAddress(tc.address, tc.opts)
		require.Error(t, err, tc.address)
		assert.NotContains(t, err.Error(), "S3cret", tc.address)
		if tc.target != nil {
			assert.ErrorIs(t, err, tc.target, tc.address)
		}
	}
}

// Test_redactAddress checks the redaction of passwords
// in addresses that url.Parse may reject.
func Test_redactAddress(t *testing.T) {
	for address, want := range map[string]string{
		"ftp://u:S3cret@bad host/path": "ftp://u:xxxxx@bad host/path",
		"u:S3cret@host":                "u:xxxxx@host",
		"u:S3cret@host/a@b":            "u:xxxxx@host/a@b",
		"ftp://u@host":                 "ftp://u@host",
		"ftp://host/a:b@c":             "ftp://host/a:b@c",
		"host":                         "host",
	} {
		assert.Equal(t, want, redactAddress(address), address)
	}
}

// Test_dialConfig_timeout checks the default timeout.
func Test_dialConfig_timeout(t *testing.T) {
	assert.Equal(t, defaultTimeout, (&dialConfig{}).timeout())
	assert.Equal(t, time.Second, (&dialConfig{opts: Options{Timeout: time.Second}}).timeout())
}
