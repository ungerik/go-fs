package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	fs "github.com/ungerik/go-fs"
)

// env returns a getenv function for the variables of the map.
func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestCutScheme(t *testing.T) {
	for _, tc := range []struct {
		uri, scheme, rest string
		ok                bool
	}{
		{"s3://bucket/key", "s3", "bucket/key", true},
		{"webdav:///path", "webdav", "/path", true},
		{"dropbox://", "dropbox", "", true},
		{"/local/path", "", "", false},
		{"relative/path", "", "", false},
		{"/data/https://x", "", "", false},
		{`C:\data\https://x`, "", "", false},
		{"://x", "", "", false},
	} {
		scheme, rest, ok := cutScheme(tc.uri)
		assert.Equal(t, tc.scheme, scheme, tc.uri)
		assert.Equal(t, tc.rest, rest, tc.uri)
		assert.Equal(t, tc.ok, ok, tc.uri)
	}
}

func TestCutAuthority(t *testing.T) {
	for _, tc := range []struct {
		rest, authority, path string
	}{
		{"user:pa%2Fss@host:21/dir/file", "user:pa%2Fss@host:21", "/dir/file"},
		{"host", "host", ""},
		{"host/", "host", "/"},
		{"/path", "", "/path"},
		{"", "", ""},
	} {
		authority, path := cutAuthority(tc.rest)
		assert.Equal(t, tc.authority, authority, tc.rest)
		assert.Equal(t, tc.path, path, tc.rest)
	}
}

func TestUserInfo(t *testing.T) {
	for _, tc := range []struct {
		address              string
		hasUser, hasPassword bool
	}{
		{"ftp://host", false, false},
		{"ftp://alice@host", true, false},
		{"ftp://alice:secret@host", true, true},
		{"ftp://alice:@host", true, true},
		{"ftp://:secret@host", false, true},
		{"ftp://%zz@host", false, false},
	} {
		hasUser, hasPassword := userInfo(tc.address)
		assert.Equal(t, tc.hasUser, hasUser, tc.address)
		assert.Equal(t, tc.hasPassword, hasPassword, tc.address)
	}
}

func TestConfiguredFile(t *testing.T) {
	memFS, err := fs.NewMemFileSystem(nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(memFS))
	t.Cleanup(func() { _ = memFS.Close() })
	prefix := memFS.Prefix()

	for _, rest := range []string{"/dir/file", "/", ""} {
		file, err := configuredFile(memFS, "mem:///"+rest, rest)
		require.NoError(t, err)
		assert.Equal(t, fs.File(prefix+rest), file)
	}
	_, rest, _ := cutScheme(prefix + "/file")
	file, err := configuredFile(memFS, prefix+"/file", rest)
	require.NoError(t, err)
	assert.Equal(t, fs.File(prefix+"/file"), file)

	_, err = configuredFile(memFS, "mem://other/file", "other/file")
	assert.ErrorContains(t, err, "is not on "+prefix+", the file system configured by the environment; use mem:///path")
}

func TestFile_Unconnected(t *testing.T) {
	c := newConnector(env(nil))
	t.Cleanup(func() { assert.NoError(t, c.close()) })
	for _, uri := range []string{"/local/path", "file:///local/path", "http://example.com/x", "https://example.com/x"} {
		file, err := c.file(t.Context(), uri)
		require.NoError(t, err, uri)
		assert.Equal(t, fs.File(uri), file)
	}
	assert.Empty(t, c.connected, "nothing connected")
}

func TestFile_Errors(t *testing.T) {
	c := newConnector(env(map[string]string{"DROPBOX_REFRESH_TOKEN": "refresh"}))
	t.Cleanup(func() { assert.NoError(t, c.close()) })
	for _, tc := range []struct {
		uri, err string
	}{
		{"gopher://user:S3cret@host/x", `unsupported URI scheme "gopher"`},
		{"smb://host", "has no host or share"},
		{"smb://host/", "has no host or share"},
		{"smb:///share/x", "has no host or share"},
		{"azblob://account.blob.core.windows.net", "has no container"},
		{"azblob:///", "has no container"},
		{"azblob:///container/x", "need AZURE_STORAGE_ACCOUNT or AZURE_STORAGE_CONNECTION_STRING"},
		{"webdav:///x", "GOFS_WEBDAV_URL"},
		{"dropbox:///x", "DROPBOX_APP_KEY"},
	} {
		_, err := c.file(t.Context(), tc.uri)
		assert.ErrorContains(t, err, tc.err, tc.uri)
		if err != nil {
			assert.NotContains(t, err.Error(), "S3cret")
		}
	}

	c = newConnector(env(nil))
	_, err := c.file(t.Context(), "dropbox:///x")
	assert.ErrorContains(t, err, "dropbox:// URIs need DROPBOX_ACCESS_TOKEN")
}

// closedPort returns the address of a local TCP port that refuses connections.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// TestFile_DialErrors checks that the file systems that dial a server
// report the errors and don't register anything.
func TestFile_DialErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	addr := closedPort(t)
	c := newConnector(env(map[string]string{
		"GOFS_FTP_PASSWORD":                  "S3cret",
		"GOFS_SFTP_PASSWORD":                 "S3cret",
		"GOFS_SMB_PASSWORD":                  "S3cret",
		"GOFS_SFTP_INSECURE_IGNORE_HOST_KEY": "true",
	}))
	t.Cleanup(func() { assert.NoError(t, c.close()) })
	for _, uri := range []string{
		"ftp://alice@" + addr + "/x",
		"ftps://" + addr + "/x",
		"sftp://alice:S3cret@" + addr + "/x",
		"smb://alice@" + addr + "/share/x",
	} {
		_, err := c.file(t.Context(), uri)
		require.Error(t, err, uri)
		assert.NotContains(t, err.Error(), "S3cret")
	}
	assert.Empty(t, c.connected)
	assert.Empty(t, c.closers)
}

func TestFile_S3(t *testing.T) {
	// The AWS SDK reads the environment, not the getenv of the connector
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "eu-central-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

	c := newConnector(env(map[string]string{"GOFS_S3_PATH_STYLE": "true"}))
	// s3fs.New doesn't send requests
	file, err := c.file(t.Context(), "s3://my-bucket/dir/key")
	require.NoError(t, err)
	assert.Equal(t, fs.File("s3://my-bucket/dir/key"), file)
	file, err = c.file(t.Context(), "s3://my-bucket")
	require.NoError(t, err)
	assert.Equal(t, fs.File("s3://my-bucket"), file)
	require.Len(t, c.connected, 1, "one file system for the bucket")
	fileSystem := fs.GetFileSystemByPrefixOrNil("s3://my-bucket")
	require.NotNil(t, fileSystem, "registered")

	require.NoError(t, c.close())
	assert.False(t, fs.IsRegistered(fileSystem), "close unregisters the file system")
	assert.Empty(t, c.connected)
}

// TestConnect_AlreadyRegistered checks that connect uses a file system
// registered with the prefix of the dialed one.
func TestConnect_AlreadyRegistered(t *testing.T) {
	registered, err := fs.NewMemFileSystem(nil)
	require.NoError(t, err)
	require.NoError(t, fs.Register(registered))
	t.Cleanup(func() { _ = registered.Close() })

	c := newConnector(env(nil))
	dialed, err := fs.NewMemFileSystem(&fs.MemFileSystemOptions{ID: registered.ID()})
	require.NoError(t, err)
	fileSystem, err := c.connect("key", func() (fs.FileSystem, error) { return dialed, nil })
	require.NoError(t, err)
	assert.Same(t, registered, fileSystem)
	assert.Empty(t, c.closers, "the registered file system is not closed by the connector")
	require.NoError(t, c.close())
	assert.True(t, fs.IsRegistered(registered))
}

func TestAzureContainerClient(t *testing.T) {
	const connStr = "DefaultEndpointsProtocol=https;AccountName=myaccount;AccountKey=a2V5;EndpointSuffix=core.windows.net"
	for _, tc := range []struct {
		name, host string
		env        map[string]string
		url, err   string
	}{
		{
			name: "connection string",
			env:  map[string]string{"AZURE_STORAGE_CONNECTION_STRING": connStr},
			url:  "https://myaccount.blob.core.windows.net/container",
		},
		{
			name: "connection string for the host",
			host: "myaccount.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_CONNECTION_STRING": connStr},
			url:  "https://myaccount.blob.core.windows.net/container",
		},
		{
			name: "connection string for another host",
			host: "other.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_CONNECTION_STRING": connStr},
			err:  "AZURE_STORAGE_CONNECTION_STRING is not for the storage account at other.blob.core.windows.net",
		},
		{
			name: "invalid connection string",
			env:  map[string]string{"AZURE_STORAGE_CONNECTION_STRING": "AccountKey=S3cret"},
			err:  "invalid AZURE_STORAGE_CONNECTION_STRING",
		},
		{
			name: "SAS token",
			env:  map[string]string{"AZURE_STORAGE_ACCOUNT": "myaccount", "AZURE_STORAGE_SAS_TOKEN": "?sv=2024&sig=x"},
			url:  "https://myaccount.blob.core.windows.net/container?sv=2024&sig=x",
		},
		{
			name: "account key",
			host: "other.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_KEY": "a2V5"},
			url:  "https://other.blob.core.windows.net/container",
		},
		{
			name: "SAS token for another host",
			host: "attacker.example",
			env:  map[string]string{"AZURE_STORAGE_SAS_TOKEN": "sv=2024&sig=S3cret"},
			err:  "only sent to the storage account host",
		},
		{
			name: "account key for a host below another domain",
			host: "myaccount.blob.core.windows.net.attacker.example",
			env:  map[string]string{"AZURE_STORAGE_KEY": "a2V5"},
			err:  "only sent to the storage account host",
		},
		{
			name: "account key for another account than AZURE_STORAGE_ACCOUNT",
			host: "other.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_ACCOUNT": "myaccount", "AZURE_STORAGE_KEY": "a2V5"},
			err:  "only sent to the storage account host",
		},
		{
			name: "default credentials for another host",
			host: "attacker.example",
			env:  map[string]string{},
			err:  "only sent to the storage account host",
		},
		{
			name: "account key for the account in another case",
			host: "MyAccount.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_ACCOUNT": "myaccount", "AZURE_STORAGE_KEY": "a2V5"},
			url:  "https://MyAccount.blob.core.windows.net/container",
		},
		{
			name: "invalid account key",
			host: "other.blob.core.windows.net",
			env:  map[string]string{"AZURE_STORAGE_KEY": "not base64"},
			err:  "invalid AZURE_STORAGE_KEY",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newConnector(env(tc.env))
			client, err := c.azureContainerClient(tc.host, "container")
			if tc.err != "" {
				assert.ErrorContains(t, err, tc.err)
				if err != nil {
					assert.NotContains(t, err.Error(), "S3cret")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.url, client.URL())
		})
	}
}

// setHome sets the home directory to a new temporary directory
// with an empty .ssh directory and returns the .ssh directory.
func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.Mkdir(sshDir, 0o700))
	return sshDir
}

// writeKnownHosts writes a known_hosts file to dir
// with the keys for host and returns its path.
func writeKnownHosts(t *testing.T, dir, host string, keys ...ssh.PublicKey) string {
	t.Helper()
	var lines []string
	for _, key := range keys {
		lines = append(lines, knownhosts.Line([]string{host}, key))
	}
	path := filepath.Join(dir, "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

func newSigner(t *testing.T, key any) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	return signer
}

// writeSSHKey writes the private key to path, encrypted with passphrase
// if it is not empty.
func writeSSHKey(t *testing.T, path string, key any, passphrase string) {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
}

func TestSFTPOptions_Credentials(t *testing.T) {
	setHome(t)
	vars := map[string]string{"GOFS_SFTP_INSECURE_IGNORE_HOST_KEY": "true"}
	c := newConnector(env(vars))

	opts, err := c.sftpOptions("sftp://bob:pw@example.com")
	require.NoError(t, err)
	assert.Empty(t, opts.Username, "the user of the address")
	assert.Empty(t, opts.Password, "the password of the address")
	assert.Empty(t, opts.Auth, "no keys")
	require.NotNil(t, opts.HostKeyCallback)
	assert.NoError(t, opts.HostKeyCallback("example.com:22", &net.TCPAddr{}, newSigner(t, ed25519.NewKeyFromSeed(make([]byte, 32))).PublicKey()))

	vars["GOFS_SFTP_USER"] = "carol"
	vars["GOFS_SFTP_PASSWORD"] = "secret"
	opts, err = c.sftpOptions("sftp://example.com")
	require.NoError(t, err)
	assert.Equal(t, "carol", opts.Username)
	assert.Equal(t, "secret", opts.Password)

	delete(vars, "GOFS_SFTP_USER")
	opts, err = c.sftpOptions("sftp://example.com")
	require.NoError(t, err)
	assert.Equal(t, localUsername(), opts.Username, "the local user like ssh")

	_, err = c.sftpOptions("sftp://")
	assert.ErrorContains(t, err, "invalid SFTP address")
}

func TestSFTPOptions_Keys(t *testing.T) {
	sshDir := setHome(t)
	_, plainKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	writeSSHKey(t, filepath.Join(sshDir, "id_ed25519"), plainKey, "")
	writeSSHKey(t, filepath.Join(sshDir, "id_ecdsa"), ecdsaKey, "passphrase")

	vars := map[string]string{}
	c := newConnector(env(vars))
	signers, err := c.sshKeyFileSigners()
	require.NoError(t, err)
	require.Len(t, signers, 1, "the default key with a passphrase is skipped without GOFS_SFTP_KEY_PASSPHRASE")
	assert.Equal(t, ssh.KeyAlgoED25519, signers[0].PublicKey().Type())

	vars["GOFS_SFTP_KEY_PASSPHRASE"] = "passphrase"
	signers, err = c.sshKeyFileSigners()
	require.NoError(t, err)
	assert.Len(t, signers, 2)

	vars["GOFS_SFTP_KEY_PASSPHRASE"] = "another-key"
	signers, err = c.sshKeyFileSigners()
	require.NoError(t, err, "a default key with another passphrase is skipped like one without")
	assert.Len(t, signers, 1)
	vars["GOFS_SFTP_KEY_PASSPHRASE"] = "passphrase"

	vars["GOFS_SFTP_KEY_FILE"] = filepath.Join(sshDir, "id_ecdsa")
	signers, err = c.sshKeyFileSigners()
	require.NoError(t, err)
	require.Len(t, signers, 1, "only GOFS_SFTP_KEY_FILE")
	assert.Equal(t, ssh.KeyAlgoECDSA256, signers[0].PublicKey().Type())

	vars["GOFS_SFTP_KEY_PASSPHRASE"] = "wrong"
	_, err = c.sshKeyFileSigners()
	assert.ErrorContains(t, err, "SSH key "+vars["GOFS_SFTP_KEY_FILE"])
	delete(vars, "GOFS_SFTP_KEY_PASSPHRASE")
	_, err = c.sshKeyFileSigners()
	assert.Error(t, err, "GOFS_SFTP_KEY_FILE without its passphrase")

	vars["GOFS_SFTP_KEY_FILE"] = filepath.Join(sshDir, "missing")
	_, err = c.sshKeyFileSigners()
	assert.ErrorIs(t, err, os.ErrNotExist)

	delete(vars, "GOFS_SFTP_KEY_FILE")
	vars["GOFS_SFTP_INSECURE_IGNORE_HOST_KEY"] = "1"
	opts, err := c.sftpOptions("sftp://alice@example.com")
	require.NoError(t, err)
	require.Len(t, opts.Auth, 1, "one public key method for all keys")
}

func TestSFTPOptions_KnownHosts(t *testing.T) {
	sshDir := setHome(t)
	ed25519Key := newSigner(t, ed25519.NewKeyFromSeed(make([]byte, 32))).PublicKey()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaPub := newSigner(t, rsaKey).PublicKey()
	writeKnownHosts(t, sshDir, "example.com", rsaPub, ed25519Key)

	c := newConnector(env(nil))
	opts, err := c.sftpOptions("sftp://alice@example.com")
	require.NoError(t, err, "~/.ssh/known_hosts")
	assert.Equal(t, []string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}, opts.HostKeyAlgorithms)
	remote := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 22}
	assert.NoError(t, opts.HostKeyCallback("example.com:22", remote, ed25519Key))
	assert.Error(t, opts.HostKeyCallback("example.com:22", remote, newSigner(t, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))).PublicKey()), "another key")

	opts, err = c.sftpOptions("sftp://alice@unknown.example.com:2222")
	require.NoError(t, err)
	assert.Nil(t, opts.HostKeyAlgorithms, "the defaults for an unknown host")
	err = opts.HostKeyCallback("[unknown.example.com]:2222", remote, ed25519Key)
	assert.ErrorContains(t, err, "the host key of [unknown.example.com]:2222 is not in "+filepath.Join(sshDir, "known_hosts"))

	otherDir := t.TempDir()
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	writeKnownHosts(t, otherDir, "[example.com]:2222", newSigner(t, ecdsaKey).PublicKey())
	c = newConnector(env(map[string]string{"GOFS_SFTP_KNOWN_HOSTS": filepath.Join(otherDir, "known_hosts")}))
	opts, err = c.sftpOptions("sftp://alice@example.com:2222")
	require.NoError(t, err)
	assert.Equal(t, []string{ssh.KeyAlgoECDSA384}, opts.HostKeyAlgorithms)

	c = newConnector(env(map[string]string{"GOFS_SFTP_KNOWN_HOSTS": filepath.Join(otherDir, "missing")}))
	_, err = c.sftpOptions("sftp://alice@example.com")
	assert.ErrorContains(t, err, "GOFS_SFTP_INSECURE_IGNORE_HOST_KEY")
}

func TestLocalUsername(t *testing.T) {
	name := localUsername()
	assert.NotEmpty(t, name)
	assert.NotContains(t, name, `\`)
	_, err := url.Parse("sftp://" + url.User(name).String() + "@host")
	assert.NoError(t, err)
}
