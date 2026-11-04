package main

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/oauth2"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/azureblobfs"
	"github.com/ungerik/go-fs/dropboxfs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/ftpfs"
	_ "github.com/ungerik/go-fs/httpfs" // registers the http:// and https:// file systems
	"github.com/ungerik/go-fs/s3fs"
	"github.com/ungerik/go-fs/sftpfs"
	"github.com/ungerik/go-fs/smbfs"
	"github.com/ungerik/go-fs/webdavfs"
)

// dropboxTokenURL is the OAuth 2 endpoint that refreshes Dropbox access tokens
const dropboxTokenURL = "https://api.dropboxapi.com/oauth2/token" //#nosec G101 -- a URL, no credentials

// envVars are the environment variables that configure
// the file systems, listed by the env command.
var envVars = []struct{ scheme, name, help string }{
	{"ftp://, ftps://", "GOFS_FTP_USER", "username if the URI has none, else anonymous login"},
	{"ftp://, ftps://", "GOFS_FTP_PASSWORD", "password if the URI has none"},
	{"ftp://, ftps://", "GOFS_FTP_INSECURE_SKIP_VERIFY", "true: don't verify the FTPS server certificate"},
	{"sftp://", "GOFS_SFTP_USER", "username if the URI has none, else the local username"},
	{"sftp://", "GOFS_SFTP_PASSWORD", "password if the URI has none"},
	{"sftp://", "GOFS_SFTP_KEY_FILE", "private key file instead of ~/.ssh/id_ed25519, id_ecdsa, id_rsa"},
	{"sftp://", "GOFS_SFTP_KEY_PASSPHRASE", "passphrase of the private key files"},
	{"sftp://", "SSH_AUTH_SOCK", "socket of the SSH agent, whose keys are tried first"},
	{"sftp://", "GOFS_SFTP_KNOWN_HOSTS", "known hosts file instead of ~/.ssh/known_hosts"},
	{"sftp://", "GOFS_SFTP_INSECURE_IGNORE_HOST_KEY", "true: don't verify the host key of the server"},
	{"smb://", "GOFS_SMB_USER", "username if the URI has none, else a guest session without password"},
	{"smb://", "GOFS_SMB_PASSWORD", "password if the URI has none"},
	{"smb://", "GOFS_SMB_DOMAIN", "NTLM domain"},
	{"webdav://", "GOFS_WEBDAV_URL", "http or https URL of the server, required, webdav:///path is below it"},
	{"webdav://", "GOFS_WEBDAV_USER", "username, overrides the one of GOFS_WEBDAV_URL"},
	{"webdav://", "GOFS_WEBDAV_PASSWORD", "password for basic authentication"},
	{"webdav://", "GOFS_WEBDAV_TOKEN", "bearer token, sent instead of basic authentication"},
	{"s3://", "AWS_ACCESS_KEY_ID", "access key, or the other credentials of the AWS SDK, like AWS_PROFILE"},
	{"s3://", "AWS_SECRET_ACCESS_KEY", "secret key"},
	{"s3://", "AWS_SESSION_TOKEN", "session token of temporary credentials"},
	{"s3://", "AWS_PROFILE", "profile of ~/.aws/config and ~/.aws/credentials"},
	{"s3://", "AWS_REGION", "region of the buckets"},
	{"s3://", "AWS_ENDPOINT_URL_S3", "endpoint of other S3 services, like MinIO or Cloudflare R2"},
	{"s3://", "AWS_REQUEST_CHECKSUM_CALCULATION", "when_required: for S3 services without CRC checksums"},
	{"s3://", "AWS_RESPONSE_CHECKSUM_VALIDATION", "when_required: for S3 services without CRC checksums"},
	{"s3://", "GOFS_S3_PATH_STYLE", "true: path-style requests, needed by MinIO and buckets with dots"},
	{"azblob://", "AZURE_STORAGE_CONNECTION_STRING", "connection string, used before the other credentials"},
	{"azblob://", "AZURE_STORAGE_ACCOUNT", "storage account of azblob:///CONTAINER/path URIs"},
	{"azblob://", "AZURE_STORAGE_SAS_TOKEN", "SAS token, used before AZURE_STORAGE_KEY"},
	{"azblob://", "AZURE_STORAGE_KEY", "account key, else the credentials of azidentity, like az login"},
	{"azblob://", "AZURE_CLIENT_ID", "service principal or managed identity of azidentity"},
	{"azblob://", "AZURE_TENANT_ID", "tenant of the service principal"},
	{"azblob://", "AZURE_CLIENT_SECRET", "secret of the service principal"},
	{"dropbox://", "DROPBOX_ACCESS_TOKEN", "access token, short-lived"},
	{"dropbox://", "DROPBOX_REFRESH_TOKEN", "refresh token, used instead of DROPBOX_ACCESS_TOKEN"},
	{"dropbox://", "DROPBOX_APP_KEY", "app key that DROPBOX_REFRESH_TOKEN was issued to"},
	{"dropbox://", "DROPBOX_APP_SECRET", "app secret, not needed for tokens issued with PKCE"},
}

// connector returns the files of the URIs of the command line arguments.
// It connects the file system of a remote URI on first use with the
// configuration of the environment, see envVars, and registers it,
// so that the files of all file systems work together, like for
// a copy from one file system to another.
type connector struct {
	getenv func(string) string

	// connected are the file systems of the connector
	// by the key that connect was called with
	connected map[string]fs.FileSystem

	// closers are closed by close in reverse order:
	// the file systems that the connector registered
	// and the connection to the SSH agent
	closers []io.Closer
}

func newConnector(getenv func(string) string) *connector {
	return &connector{
		getenv:    getenv,
		connected: make(map[string]fs.FileSystem),
	}
}

// close closes the file systems that the connector connected,
// which unregisters them.
func (c *connector) close() error {
	var errs []error
	for _, closer := range slices.Backward(c.closers) {
		errs = append(errs, closer.Close())
	}
	c.closers = nil
	clear(c.connected)
	return errors.Join(errs...)
}

// envBool returns the boolean value of the environment variable name,
// false if it is not set or not a boolean, see strconv.ParseBool.
func (c *connector) envBool(name string) bool {
	b, _ := strconv.ParseBool(c.getenv(name))
	return b
}

// cutScheme returns the scheme of uri and the rest after its "://".
// ok is false for a local path, which has no "://" or a slash or
// backslash before it, like "/data/https://x", see fs.ParseRawURI.
func cutScheme(uri string) (scheme, rest string, ok bool) {
	scheme, rest, ok = strings.Cut(uri, fs.PrefixSeparator)
	if !ok || scheme == "" || strings.ContainsAny(scheme, `/\`) {
		return "", "", false
	}
	return scheme, rest, true
}

// cutAuthority splits the rest of a URI after its "://" at the first slash
// into the authority, like "user@host:port", and the path with the slash.
// The file systems that dial a server require a '/' in the user info
// to be percent-encoded, so the first slash ends the authority.
func cutAuthority(rest string) (authority, path string) {
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i:]
	}
	return rest, ""
}

// userInfo reports if address has a username and a password.
// An address that is no valid URL has neither,
// the dial of the address reports the error.
func userInfo(address string) (hasUser, hasPassword bool) {
	u, err := url.Parse(address)
	if err != nil || u.User == nil {
		return false, false
	}
	_, hasPassword = u.User.Password()
	return u.User.Username() != "", hasPassword
}

// file returns the file of uri, connecting its file system if necessary.
func (c *connector) file(ctx context.Context, uri string) (fs.File, error) {
	scheme, rest, ok := cutScheme(uri)
	if !ok {
		return fs.File(uri), nil // local path
	}
	switch scheme {
	case "file", "http", "https":
		// Registered by the fs and the httpfs package
		return fs.File(uri), nil
	case "ftp", "ftps":
		return c.ftpFile(ctx, scheme, rest)
	case "sftp":
		return c.sftpFile(ctx, rest)
	case "smb":
		return c.smbFile(ctx, uri, rest)
	case "webdav":
		return c.webdavFile(ctx, uri, rest)
	case "s3":
		return c.s3File(ctx, rest)
	case "azblob":
		return c.azblobFile(ctx, uri, rest)
	case "dropbox":
		return c.dropboxFile(ctx, uri, rest)
	}
	return "", fmt.Errorf("unsupported URI scheme %q of %s, see gofs help", scheme, fsimpl.RedactURI(uri))
}

// connect returns the file system that the connector connected for key,
// or connects it with dial and registers it.
func (c *connector) connect(key string, dial func() (fs.FileSystem, error)) (fs.FileSystem, error) {
	if fileSystem, ok := c.connected[key]; ok {
		return fileSystem, nil
	}
	fileSystem, err := dial()
	if err != nil {
		return nil, err
	}
	err = fs.Register(fileSystem)
	registered := fs.GetFileSystemByPrefixOrNil(fileSystem.Prefix())
	switch {
	case err == nil:
		c.closers = append(c.closers, fileSystem)
	case errors.Is(err, fs.ErrAlreadyRegistered) && registered != nil:
		// Another key for the same file system, like
		// the address of a server with and without its default port
		err = fileSystem.Close()
		if err != nil {
			return nil, err
		}
		fileSystem = registered
	default:
		return nil, errors.Join(err, fileSystem.Close())
	}
	c.connected[key] = fileSystem
	return fileSystem, nil
}

// configuredFile returns the file of uri on fileSystem, the one file system
// that the environment configures for the scheme of uri: a URI with an empty
// authority, like "dropbox:///path", is the path in fileSystem,
// other URIs must begin with the prefix of fileSystem.
func configuredFile(fileSystem fs.FileSystem, uri, rest string) (fs.File, error) {
	if rest == "" || rest[0] == '/' {
		return fs.File(fileSystem.Prefix() + rest), nil
	}
	if fs.GetFileSystem(uri) != fileSystem {
		return "", fmt.Errorf("%s is not on %s, the file system configured by the environment; use %s:///path", fsimpl.RedactURI(uri), fileSystem.Prefix(), uri[:strings.Index(uri, fs.PrefixSeparator)])
	}
	return fs.File(uri), nil
}

func (c *connector) ftpFile(ctx context.Context, scheme, rest string) (fs.File, error) {
	authority, filePath := cutAuthority(rest)
	address := scheme + fs.PrefixSeparator + authority
	opts := &ftpfs.Options{InsecureSkipVerify: c.envBool("GOFS_FTP_INSECURE_SKIP_VERIFY")}
	hasUser, hasPassword := userInfo(address)
	if !hasUser {
		opts.Username = c.getenv("GOFS_FTP_USER")
	}
	if !hasPassword {
		opts.Password = c.getenv("GOFS_FTP_PASSWORD")
	}
	fileSystem, err := c.connect(address, func() (fs.FileSystem, error) {
		return ftpfs.Dial(ctx, address, opts)
	})
	if err != nil {
		return "", err
	}
	return fs.File(fileSystem.Prefix() + filePath), nil
}

func (c *connector) sftpFile(ctx context.Context, rest string) (fs.File, error) {
	authority, filePath := cutAuthority(rest)
	address := sftpfs.Prefix + authority
	fileSystem, err := c.connect(address, func() (fs.FileSystem, error) {
		opts, err := c.sftpOptions(address)
		if err != nil {
			return nil, err
		}
		return sftpfs.Dial(ctx, address, opts)
	})
	if err != nil {
		return "", err
	}
	return fs.File(fileSystem.Prefix() + filePath), nil
}

// sftpOptions returns the options to dial address like ssh does: with the
// local username, the keys of the SSH agent and of ~/.ssh, and the host
// keys of ~/.ssh/known_hosts, unless the environment configures others.
func (c *connector) sftpOptions(address string) (*sftpfs.Options, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid SFTP address %s", fsimpl.RedactAddress(address))
	}
	opts := new(sftpfs.Options)
	if u.User.Username() == "" {
		opts.Username = cmp.Or(c.getenv("GOFS_SFTP_USER"), localUsername())
	}
	if _, ok := u.User.Password(); !ok {
		opts.Password = c.getenv("GOFS_SFTP_PASSWORD")
	}
	signers, err := c.sshKeyFileSigners()
	if err != nil {
		return nil, err
	}
	agentClient := c.sshAgent()
	if agentClient != nil || len(signers) > 0 {
		allSigners := func() ([]ssh.Signer, error) {
			if agentClient == nil {
				return signers, nil
			}
			agentSigners, err := agentClient.Signers()
			if err != nil {
				return signers, nil // the keys of the files can still authenticate
			}
			return append(agentSigners, signers...), nil
		}
		// One method for all keys: the SSH client tries
		// every authentication method only once
		opts.Auth = append(opts.Auth, ssh.PublicKeysCallback(allSigners))
	}
	if c.envBool("GOFS_SFTP_INSECURE_IGNORE_HOST_KEY") {
		opts.HostKeyCallback = sftpfs.AcceptAnyHostKey
		return opts, nil
	}
	knownHostsFile := c.getenv("GOFS_SFTP_KNOWN_HOSTS")
	if knownHostsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("SSH known hosts: %w, set GOFS_SFTP_KNOWN_HOSTS", err)
		}
		knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	}
	callback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("SSH known hosts: %w, set GOFS_SFTP_KNOWN_HOSTS or GOFS_SFTP_INSECURE_IGNORE_HOST_KEY", err)
	}
	opts.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := callback(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			return fmt.Errorf("%w: the host key of %s is not in %s, add it with ssh-keyscan or connect once with ssh", err, hostname, knownHostsFile)
		}
		return err
	}
	opts.HostKeyAlgorithms = knownHostKeyAlgorithms(callback, net.JoinHostPort(u.Hostname(), cmp.Or(u.Port(), "22")))
	return opts, nil
}

// localUsername returns the name of the user running the command
// like ssh uses it, or an empty string if it is unknown.
func localUsername() string {
	u, err := user.Current()
	if err != nil {
		return cmp.Or(os.Getenv("USER"), os.Getenv("USERNAME"))
	}
	// The Windows name has the form DOMAIN\user
	_, name, found := strings.Cut(u.Username, `\`)
	if found {
		return name
	}
	return u.Username
}

// sshKeyFileSigners returns the signers of the private key files:
// GOFS_SFTP_KEY_FILE, or the default key files of ~/.ssh that exist.
// A default key file with a passphrase is skipped if
// GOFS_SFTP_KEY_PASSPHRASE is not set or not its passphrase,
// the agent may have its key.
func (c *connector) sshKeyFileSigners() ([]ssh.Signer, error) {
	passphrase := c.getenv("GOFS_SFTP_KEY_PASSPHRASE")
	if keyFile := c.getenv("GOFS_SFTP_KEY_FILE"); keyFile != "" {
		signer, err := readSSHKey(keyFile, passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.Signer{signer}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil // no default key files without a home directory
	}
	var signers []ssh.Signer
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		signer, err := readSSHKey(filepath.Join(home, ".ssh", name), passphrase)
		var missingPassphrase *ssh.PassphraseMissingError
		switch {
		case errors.Is(err, os.ErrNotExist), errors.As(err, &missingPassphrase),
			// GOFS_SFTP_KEY_PASSPHRASE may be the one of another key,
			// the agent or the other keys can still authenticate
			errors.Is(err, x509.IncorrectPasswordError):
			continue
		case err != nil:
			return nil, err
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

// readSSHKey returns the signer of the private key file,
// decrypting it with passphrase if the key has one.
func readSSHKey(keyFile, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(keyFile) //#nosec G304 -- the user chooses the key file
	if err != nil {
		return nil, fmt.Errorf("SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	var missingPassphrase *ssh.PassphraseMissingError
	if errors.As(err, &missingPassphrase) && passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("SSH key %s: %w", keyFile, err)
	}
	return signer, nil
}

// sshAgent returns a client of the SSH agent of SSH_AUTH_SOCK,
// or nil if there is none.
func (c *connector) sshAgent() agent.ExtendedAgent {
	socket := c.getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}
	c.closers = append(c.closers, conn)
	return agent.NewClient(conn)
}

// knownHostKeyAlgorithms returns the host key algorithms of the keys that
// callback knows for hostPort, most secure first, or nil if it knows none.
// The SSH client accepts only the key that the server presents first,
// so without them a dial fails for a server whose preferred key is not
// in known_hosts, but another one is (golang/go#29286).
func knownHostKeyAlgorithms(callback ssh.HostKeyCallback, hostPort string) []string {
	// The error for a key that callback doesn't know lists the keys it knows
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(callback(hostPort, &net.TCPAddr{IP: net.IPv4zero}, probe), &keyErr) {
		return nil
	}
	known := make(map[string]bool)
	for _, key := range keyErr.Want {
		known[key.Key.Type()] = true
	}
	var algorithms []string
	for _, keyType := range []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoSKECDSA256,
		ssh.KeyAlgoRSA,
	} {
		if !known[keyType] {
			continue
		}
		if keyType == ssh.KeyAlgoRSA {
			// An RSA key signs with one of these algorithms
			algorithms = append(algorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		}
		algorithms = append(algorithms, keyType)
	}
	return algorithms
}

func (c *connector) smbFile(ctx context.Context, uri, rest string) (fs.File, error) {
	authority, sharePath := cutAuthority(rest)
	share, filePath := cutAuthority(strings.TrimPrefix(sharePath, "/"))
	if authority == "" || share == "" {
		return "", fmt.Errorf("the SMB URI %s has no host or share, use smb://[user@]host/share/path", fsimpl.RedactURI(uri))
	}
	address := smbfs.Prefix + authority + "/" + share
	opts := &smbfs.Options{Domain: c.getenv("GOFS_SMB_DOMAIN")}
	hasUser, hasPassword := userInfo(address)
	if !hasUser {
		opts.Username = c.getenv("GOFS_SMB_USER")
	}
	if !hasPassword {
		opts.Password = c.getenv("GOFS_SMB_PASSWORD")
	}
	fileSystem, err := c.connect(address, func() (fs.FileSystem, error) {
		return smbfs.Dial(ctx, address, opts)
	})
	if err != nil {
		return "", err
	}
	return fs.File(fileSystem.Prefix() + filePath), nil
}

func (c *connector) webdavFile(ctx context.Context, uri, rest string) (fs.File, error) {
	fileSystem, err := c.connect(webdavfs.Prefix, func() (fs.FileSystem, error) {
		baseURL := c.getenv("GOFS_WEBDAV_URL")
		if baseURL == "" {
			return nil, errors.New("webdav:// URIs need the http or https URL of the server in GOFS_WEBDAV_URL")
		}
		opts := &webdavfs.Options{
			Username: c.getenv("GOFS_WEBDAV_USER"),
			Password: c.getenv("GOFS_WEBDAV_PASSWORD"),
		}
		if opts.Username == "" && opts.Password != "" {
			// webdavfs uses Options.Password only together with Options.Username
			u, err := url.Parse(baseURL)
			if err == nil {
				opts.Username = u.User.Username()
			}
		}
		if token := c.getenv("GOFS_WEBDAV_TOKEN"); token != "" {
			opts.Header = http.Header{"Authorization": {"Bearer " + token}}
		}
		return webdavfs.New(ctx, baseURL, opts)
	})
	if err != nil {
		return "", err
	}
	return configuredFile(fileSystem, uri, rest)
}

func (c *connector) s3File(ctx context.Context, rest string) (fs.File, error) {
	bucket, filePath := cutAuthority(rest)
	fileSystem, err := c.connect(s3fs.Prefix+bucket, func() (fs.FileSystem, error) {
		// The AWS SDK reads the credentials, the region and endpoint
		// from the environment and the files in ~/.aws
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, err
		}
		client := s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.UsePathStyle = c.envBool("GOFS_S3_PATH_STYLE")
		})
		return s3fs.New(client, bucket, nil)
	})
	if err != nil {
		return "", err
	}
	return fs.File(fileSystem.Prefix() + filePath), nil
}

func (c *connector) azblobFile(ctx context.Context, uri, rest string) (fs.File, error) {
	host, containerPath := cutAuthority(rest)
	containerName, filePath := cutAuthority(strings.TrimPrefix(containerPath, "/"))
	if containerName == "" {
		return "", fmt.Errorf("the Azure Blob Storage URI %s has no container, use azblob://ACCOUNT.blob.core.windows.net/CONTAINER/path or azblob:///CONTAINER/path", fsimpl.RedactURI(uri))
	}
	fileSystem, err := c.connect(azureblobfs.Prefix+host+"/"+containerName, func() (fs.FileSystem, error) {
		client, err := c.azureContainerClient(host, containerName)
		if err != nil {
			return nil, err
		}
		return azureblobfs.New(ctx, client, nil)
	})
	if err != nil {
		return "", err
	}
	return fs.File(fileSystem.Prefix() + filePath), nil
}

// azureContainerClient returns the client for the container of the storage
// account at host, the account of the environment if host is empty, with the
// first credentials of the environment: a connection string, a SAS token,
// an account key, else the default Azure credential chain of azidentity,
// which supports service principals, managed identities and the Azure CLI.
func (c *connector) azureContainerClient(host, containerName string) (*container.Client, error) {
	if connStr := c.getenv("AZURE_STORAGE_CONNECTION_STRING"); connStr != "" {
		client, err := container.NewClientFromConnectionString(connStr, containerName, nil)
		if err != nil {
			// The error can contain the account key
			return nil, errors.New("invalid AZURE_STORAGE_CONNECTION_STRING")
		}
		u, err := url.Parse(client.URL())
		if err != nil || (host != "" && !strings.EqualFold(u.Host, host)) {
			return nil, fmt.Errorf("AZURE_STORAGE_CONNECTION_STRING is not for the storage account at %s, use azblob:///%s/path", host, containerName)
		}
		return client, nil
	}
	account := c.getenv("AZURE_STORAGE_ACCOUNT")
	if host == "" {
		if account == "" {
			return nil, errors.New("azblob:///CONTAINER URIs need AZURE_STORAGE_ACCOUNT or AZURE_STORAGE_CONNECTION_STRING")
		}
		host = account + ".blob.core.windows.net"
	}
	containerURL := "https://" + host + "/" + containerName
	if sas := c.getenv("AZURE_STORAGE_SAS_TOKEN"); sas != "" {
		return container.NewClientWithNoCredential(containerURL+"?"+strings.TrimPrefix(sas, "?"), nil)
	}
	if key := c.getenv("AZURE_STORAGE_KEY"); key != "" {
		if account == "" {
			account, _, _ = strings.Cut(host, ".")
		}
		cred, err := container.NewSharedKeyCredential(account, key)
		if err != nil {
			return nil, fmt.Errorf("invalid AZURE_STORAGE_KEY: %w", err)
		}
		return container.NewClientWithSharedKeyCredential(containerURL, cred, nil)
	}
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}
	return container.NewClient(containerURL, cred, nil)
}

func (c *connector) dropboxFile(ctx context.Context, uri, rest string) (fs.File, error) {
	fileSystem, err := c.connect(dropboxfs.Prefix, func() (fs.FileSystem, error) {
		opts := new(dropboxfs.Options)
		token := c.getenv("DROPBOX_ACCESS_TOKEN")
		if refreshToken := c.getenv("DROPBOX_REFRESH_TOKEN"); refreshToken != "" {
			appKey := c.getenv("DROPBOX_APP_KEY")
			if appKey == "" {
				return nil, errors.New("DROPBOX_REFRESH_TOKEN needs the DROPBOX_APP_KEY of the app that it was issued to")
			}
			oauthConfig := &oauth2.Config{
				ClientID:     appKey,
				ClientSecret: c.getenv("DROPBOX_APP_SECRET"),
				Endpoint:     oauth2.Endpoint{TokenURL: dropboxTokenURL, AuthStyle: oauth2.AuthStyleInParams},
			}
			// The client refreshes the token with the context it was
			// created with, which must not end with the dial
			opts.HTTPClient = oauthConfig.Client(context.WithoutCancel(ctx), &oauth2.Token{RefreshToken: refreshToken})
		} else if token == "" {
			return nil, errors.New("dropbox:// URIs need DROPBOX_ACCESS_TOKEN, or DROPBOX_REFRESH_TOKEN with DROPBOX_APP_KEY")
		}
		return dropboxfs.New(ctx, token, opts)
	})
	if err != nil {
		return "", err
	}
	return configuredFile(fileSystem, uri, rest)
}
