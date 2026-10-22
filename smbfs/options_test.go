package smbfs

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_parseAddress checks the URI prefix, the alias with the default
// port, the dialed address and the credentials of an address and options.
func Test_parseAddress(t *testing.T) {
	for _, tc := range []struct {
		address  string
		opts     *Options
		prefix   string
		aliases  []string
		hostPort string
		share    string
		user     string
		password string
	}{
		{
			address: "nas/share", prefix: "smb://nas/share", aliases: []string{"smb://nas:445/share"},
			hostPort: "nas:445", share: "share",
		},
		{
			address: "smb://nas:445/share/", prefix: "smb://nas/share", aliases: []string{"smb://nas:445/share"},
			hostPort: "nas:445", share: "share",
		},
		{
			address: "smb://alice:secret@nas:1445/share", prefix: "smb://alice@nas:1445/share",
			hostPort: "nas:1445", share: "share", user: "alice", password: "secret",
		},
		// The password of the options was ignored for a username from the address
		{
			address: "smb://alice@nas.local/documents", opts: &Options{Password: "secret", Domain: "WORKGROUP"},
			prefix: "smb://alice@nas.local/documents", aliases: []string{"smb://alice@nas.local:445/documents"},
			hostPort: "nas.local:445", share: "documents", user: "alice", password: "secret",
		},
		{
			address: "smb://alice:old@nas/share", opts: &Options{Username: "bob", Password: "new"},
			prefix: "smb://bob@nas/share", aliases: []string{"smb://bob@nas:445/share"},
			hostPort: "nas:445", share: "share", user: "bob", password: "new",
		},
		{
			address: "smb://first.last%40corp.com@nas/share", opts: &Options{Password: "pw"},
			prefix: "smb://first.last%40corp.com@nas/share", aliases: []string{"smb://first.last%40corp.com@nas:445/share"},
			hostPort: "nas:445", share: "share", user: "first.last@corp.com", password: "pw",
		},
		{
			address: "smb://Guest@nas/public", prefix: "smb://Guest@nas/public", aliases: []string{"smb://Guest@nas:445/public"},
			hostPort: "nas:445", share: "public", user: "Guest",
		},
		{
			address: "smb://u@[::1]/share", opts: &Options{Password: "pw"}, prefix: "smb://u@[::1]/share", aliases: []string{"smb://u@[::1]:445/share"},
			hostPort: "[::1]:445", share: "share", user: "u", password: "pw",
		},
		// The empty port of "host:" was kept in the prefix, the alias was "smb://nas::445/share"
		{
			address: "smb://nas:/share", prefix: "smb://nas/share", aliases: []string{"smb://nas:445/share"},
			hostPort: "nas:445", share: "share",
		},
		{
			address: "smb://[::1]:/share", prefix: "smb://[::1]/share", aliases: []string{"smb://[::1]:445/share"},
			hostPort: "[::1]:445", share: "share",
		},
		// url.Parse unescapes the zone of an IPv6 address, the prefix
		// had the invalid host "[fe80::1%eth0]"
		{
			address: "smb://[fe80::1%25eth0]/share", prefix: "smb://[fe80::1%25eth0]/share", aliases: []string{"smb://[fe80::1%25eth0]:445/share"},
			hostPort: "[fe80::1%eth0]:445", share: "share",
		},
		{
			address: "smb://u@[fe80::1%25eth0]:1445/share", opts: &Options{Password: "pw"}, prefix: "smb://u@[fe80::1%25eth0]:1445/share",
			hostPort: "[fe80::1%eth0]:1445", share: "share", user: "u", password: "pw",
		},
	} {
		t.Run(tc.address, func(t *testing.T) {
			cfg, err := parseAddress(tc.address, tc.opts)
			require.NoError(t, err)
			assert.Equal(t, tc.prefix, cfg.prefix, "prefix")
			_, err = url.Parse(cfg.prefix)
			assert.NoError(t, err, "the prefix is a valid URL")
			assert.Equal(t, tc.aliases, cfg.aliases, "aliases")
			assert.Equal(t, tc.hostPort, cfg.hostPort, "hostPort")
			assert.Equal(t, tc.share, cfg.share, "share")
			assert.Equal(t, tc.user, cfg.username, "username")
			assert.Equal(t, tc.password, cfg.password, "password")
			initiator := cfg.initiator()
			assert.Equal(t, tc.user, initiator.User)
			assert.Equal(t, tc.password, initiator.Password)
			if tc.opts != nil {
				assert.Equal(t, tc.opts.Domain, initiator.Domain)
			}
		})
	}
}

// Test_parseAddress_Errors verifies that an invalid address is rejected
// before a connection is attempted, without the password in the error.
func Test_parseAddress_Errors(t *testing.T) {
	for _, address := range []string{
		"ftp://alice:S3cret@host/share", // wrong scheme
		"smb://alice:S3cret@host",       // no share
		"smb://alice:S3cret@host/",      // empty share
		"smb://alice:S3cret@/share",     // no host
		"smb://alice:S3cret@host/a/b",   // share must not contain a path
		"smb://alice:S3cret@bad host/share",
		"smb://alice:S3cret@host:port/share",
	} {
		_, err := parseAddress(address, nil)
		require.Error(t, err, address)
		assert.NotContains(t, err.Error(), "S3cret", address)
	}
}

// Test_redactAddress checks the redaction of passwords
// in addresses that url.Parse may reject.
func Test_redactAddress(t *testing.T) {
	for address, want := range map[string]string{
		"smb://u:S3cret@bad host/share": "smb://u:xxxxx@bad host/share",
		"u:S3cret@host/share":           "u:xxxxx@host/share",
		"smb://u@host/share":            "smb://u@host/share",
		"smb://host/a:b@c":              "smb://host/a:b@c",
	} {
		assert.Equal(t, want, redactAddress(address), address)
	}
}

// Test_dialConfig_timeout checks the default timeout.
func Test_dialConfig_timeout(t *testing.T) {
	assert.Equal(t, defaultTimeout, (&dialConfig{}).timeout())
	assert.Equal(t, time.Second, (&dialConfig{opts: Options{Timeout: time.Second}}).timeout())
}
