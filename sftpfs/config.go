package sftpfs

import "time"

var (
	// MaxConnectRetries is the maximum number of connection attempts
	// to an SFTP server before giving up.
	MaxConnectRetries = 3

	// InitialRetryBackoff is the initial duration to wait before retrying
	// a failed SFTP connection attempt. The backoff may increase
	// exponentially for subsequent retries.
	InitialRetryBackoff = 100 * time.Millisecond
)
