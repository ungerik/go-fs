package sftpfs

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptionsDefaults(t *testing.T) {
	var zero Options
	assert.Equal(t, DefaultConnectRetries, zero.connectRetries())
	assert.Equal(t, DefaultRetryBackoff, zero.retryBackoff())
	assert.Equal(t, DefaultTimeout, zero.timeout())
	assert.Equal(t, DefaultKeepAlive, zero.keepAlive())
	assert.NotNil(t, zero.dialContext())

	negative := Options{ConnectRetries: -1, RetryBackoff: -1, Timeout: -1, KeepAlive: -1}
	assert.Equal(t, 0, negative.connectRetries())
	assert.Equal(t, time.Duration(0), negative.retryBackoff())
	assert.Equal(t, time.Duration(0), negative.timeout())
	assert.Equal(t, time.Duration(0), negative.keepAlive())

	set := Options{ConnectRetries: 7, RetryBackoff: time.Second, Timeout: time.Minute, KeepAlive: time.Hour}
	assert.Equal(t, 7, set.connectRetries())
	assert.Equal(t, time.Second, set.retryBackoff())
	assert.Equal(t, time.Minute, set.timeout())
	assert.Equal(t, time.Hour, set.keepAlive())
}

// TestPassword checks that Password returns the unescaped username
// of the address, not the escaped user info with a password.
func TestPassword(t *testing.T) {
	for address, want := range map[string]string{
		"sftp://first.last%40example.com@host": "first.last@example.com",
		"sftp://user:secret@host":              "user",
		"sftp://host":                          "",
	} {
		u, err := url.Parse(address)
		require.NoError(t, err)
		username, password, err := Password("pw")(u)
		require.NoError(t, err)
		assert.Equal(t, want, username, address)
		assert.Equal(t, "pw", password, address)
	}
}

func TestUsernameAndPassword(t *testing.T) {
	u, err := url.Parse("sftp://other:secret@host")
	require.NoError(t, err)
	username, password, err := UsernameAndPassword("user", "pw")(u)
	require.NoError(t, err)
	assert.Equal(t, "user", username)
	assert.Equal(t, "pw", password)
}

func TestAcceptAnyHostKey(t *testing.T) {
	assert.NoError(t, AcceptAnyHostKey("host:22", nil, nil))
}
