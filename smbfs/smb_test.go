package smbfs

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
)

const (
	testUsername = "testuser"
	testPassword = "testpass"
	testShare    = "share"
)

var (
	dockerSMBAvailable bool
	// testContainerName is unique for every test run and testSMBPort
	// is the host port that Docker assigned, so that test runs in other
	// worktrees don't stop the container or take the port.
	testContainerName string
	testSMBPort       string
	testSMBAddress    string
)

// TestMain starts a Samba container (dperson/samba) with one share
// for the test user. Tests are skipped when Docker is not available and
// fail when it is available but the server can't be started.
func TestMain(m *testing.M) {
	if !dockertest.Available() {
		log.Println("Docker not available, skipping Docker-based SMB tests")
		os.Exit(m.Run())
	}
	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	testContainerName = dockertest.ContainerName("smbfs-test")

	// Start the Samba container on a host port that Docker picks
	log.Println("Starting Docker Samba test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", "127.0.0.1::445",
		"dperson/samba",
		"-p",
		"-u", fmt.Sprintf("%s;%s", testUsername, testPassword),
		"-s", fmt.Sprintf("%s;/share;no;no;no;%s", testShare, testUsername),
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		removeContainer(testContainerName)
		dockertest.SetupFailed("Samba")
		os.Exit(m.Run())
	}
	testSMBPort, err = dockerHostPort(ctx, testContainerName, "445/tcp")
	if err != nil {
		log.Printf("Failed to get the host port of the Samba container: %v", err)
		removeContainer(testContainerName)
		dockertest.SetupFailed("Samba")
		os.Exit(m.Run())
	}

	testSMBAddress = fmt.Sprintf("127.0.0.1:%s/%s", testSMBPort, testShare)
	for i := range 20 {
		smbFS, err := Dial(ctx, testSMBAddress, &Options{Username: testUsername, Password: testPassword})
		if err == nil {
			smbFS.Close()
			dockerSMBAvailable = true
			log.Println("Samba server is ready")
			break
		}
		if i == 19 {
			log.Printf("Failed to connect to the Samba server: %v", err)
		}
		time.Sleep(time.Second)
	}
	if !dockerSMBAvailable {
		removeContainer(testContainerName)
		dockertest.SetupFailed("Samba")
		os.Exit(m.Run())
	}

	exitCode := m.Run()

	log.Println("Stopping and removing Docker Samba test server...")
	removeContainer(testContainerName)
	os.Exit(exitCode)
}

// removeContainer stops and removes a container together with its
// anonymous volumes, ignoring errors for a container that doesn't exist.
func removeContainer(name string) {
	_ = exec.Command("docker", "rm", "--force", "--volumes", name).Run()
}

// dockerHostPort returns the host port that Docker assigned to the
// port of a container that was published with "-p 127.0.0.1::port".
func dockerHostPort(ctx context.Context, name, port string) (string, error) {
	output, err := exec.CommandContext(ctx, "docker", "port", name, port).Output()
	if err != nil {
		return "", fmt.Errorf("docker port %s %s: %w", name, port, err)
	}
	// The first line is the host address, like "127.0.0.1:55001"
	hostAddr, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	_, hostPort, err := net.SplitHostPort(hostAddr)
	if err != nil {
		return "", fmt.Errorf("docker port %s %s: %w", name, port, err)
	}
	return hostPort, nil
}

func Test_fileSystem(t *testing.T) {
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	smbFS, err := Dial(t.Context(), testSMBAddress, &Options{Username: testUsername, Password: testPassword})
	require.NoError(t, err, "Dial")
	defer smbFS.Close()

	expectedPrefix := fmt.Sprintf("smb://%s@127.0.0.1:%s/%s", testUsername, testSMBPort, testShare)
	require.Equal(t, expectedPrefix, smbFS.Prefix())

	fstest.RunConformance(t, smbFS, fstest.Config{
		Name:    "SMB file system",
		Prefix:  expectedPrefix,
		TestDir: "/conformance",
		// SMB only stores a read-only attribute
		PermissionMask: fs.UserWrite,
	})
}

// TestDial_AddressErrors verifies the address parsing of Dial without a
// server: an invalid address must be rejected before a TCP connection is
// attempted, so a typo fails fast instead of timing out.
func TestDial_AddressErrors(t *testing.T) {
	ctx := t.Context()
	for _, address := range []string{
		"ftp://host/share", // wrong scheme
		"smb://host",       // no share
		"smb://host/",      // empty share
		"smb:///share",     // no host
		"smb://host/a/b",   // share must not contain a path
	} {
		_, err := Dial(ctx, address, nil)
		require.Error(t, err, "Dial(%q) must fail", address)
	}
}

// TestClosedFileSystem verifies that a closed SMB file system reports
// fs.ErrFileSystemClosed from every operation instead of dereferencing
// the released share and panicking.
func TestClosedFileSystem(t *testing.T) {
	ctx := t.Context()
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: "smb://user@example.com/share", Rooted: true},
	}
	f.closed.Store(true)

	_, err := f.Stat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = f.ListDir(ctx, "/dir", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenReader("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenAppendWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenReadWriter("/file", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.ReadAll(ctx, "/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.WriteAll(ctx, "/file", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Truncate(t.Context(), "/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Touch("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeAllDirs("/dir/sub", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.SetPermissions("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(t.Context(), "/a", "/b"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Remove("/file"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.CreateSymbolicLink("/a", "/link"), fs.ErrFileSystemClosed)
	_, err = f.ReadSymbolicLink("/link")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.Lstat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	// Close on an already-closed file system is a safe no-op
	// that must not touch the released connection.
	assert.NoError(t, f.Close())
}
