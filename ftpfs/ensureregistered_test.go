package ftpfs

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

// TestEnsureRegistered_RefCount verifies that EnsureRegistered reference-counts
// the file system: a second EnsureRegistered for the same address reuses the
// single registered connection, releasing one reference keeps the connection
// open, and only releasing the last reference closes and unregisters it.
//
// This guards the previous bug where the returned free unconditionally closed
// the connection, so the first free could close a file system another caller
// still held.
func TestEnsureRegistered_RefCount(t *testing.T) {
	srv := newTestFTPServer(t)
	address := fmt.Sprintf("ftp://testuser@%s", srv.addr())
	creds := UsernameAndPassword("testuser", "testpass")

	_, _, _, prefix, _, err := prepareDial(address, creds)
	require.NoError(t, err, "prepareDial")

	free1, err := EnsureRegistered(t.Context(), address, creds, nil)
	require.NoError(t, err, "first EnsureRegistered")
	free2, err := EnsureRegistered(t.Context(), address, creds, nil)
	require.NoError(t, err, "second EnsureRegistered")

	// The second call must reuse the single connection registered under the
	// prefix rather than dialing and registering a second instance.
	registered, ok := fs.GetFileSystemByPrefixOrNil(prefix).(*fileSystem)
	require.True(t, ok, "ftp fileSystem registered under prefix %q", prefix)
	require.False(t, registered.closed, "connection open while referenced")

	// Releasing the first reference must NOT close the still-referenced
	// connection (the bug closed it here).
	require.NoError(t, free1())
	assert.False(t, registered.closed, "connection must stay open while a reference remains")
	assert.NotNil(t, registered.conn, "connection must not be torn down while referenced")
	assert.True(t, fs.IsRegistered(registered), "still registered after releasing one of two references")

	// Releasing the last reference closes and unregisters it.
	require.NoError(t, free2())
	assert.True(t, registered.closed, "connection closed after the last reference is released")
	assert.False(t, fs.IsRegistered(registered), "unregistered after the last reference is released")
}
