package sftpfs

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

const (
	testContainerName = "sftp-test-server"
	testSFTPPort      = "2222"
	testUsername      = "testuser"
	testPassword      = "testpass"
	testDataDir       = "/home/testuser/testdata"
)

var (
	dockerSFTPAvailable bool
	testSFTPAddress     string
)

func TestMain(m *testing.M) {
	// Check if Docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		log.Println("Docker not available, skipping Docker-based SFTP tests")
		dockerSFTPAvailable = false
		os.Exit(m.Run())
		return
	}

	ctx := context.Background()

	// Stop and remove any existing container with the same name
	exec.Command("docker", "stop", testContainerName).Run()
	exec.Command("docker", "rm", testContainerName).Run()

	// Build the Docker image
	log.Println("Building Docker SFTP test server image...")
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", "Dockerfile.sftp-test",
		"-t", "sftp-test-server",
		".",
	)
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to build Docker image: %v\nOutput: %s", err, output)
		dockerSFTPAvailable = false
		os.Exit(m.Run())
		return
	}
	log.Println("Docker image built successfully")

	// Start the SFTP server container
	log.Println("Starting Docker SFTP test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", fmt.Sprintf("%s:22", testSFTPPort),
		"sftp-test-server",
	)
	output, err = runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		dockerSFTPAvailable = false
		os.Exit(m.Run())
		return
	}
	containerID := string(output)
	log.Printf("Started Docker container: %s", containerID)

	// Wait for SSH server to be ready with retry logic
	log.Println("Waiting for SFTP server to be ready...")
	time.Sleep(3 * time.Second)

	testSFTPAddress = fmt.Sprintf("localhost:%s", testSFTPPort)
	maxRetries := 10
	var testErr error
	for i := range maxRetries {
		sftpFS, err := Dial(ctx, testSFTPAddress, UsernameAndPassword(testUsername, testPassword), AcceptAnyHostKey)
		if err == nil {
			sftpFS.Close()
			dockerSFTPAvailable = true
			log.Println("SFTP server is ready")
			break
		}
		testErr = err
		if i < maxRetries-1 {
			log.Printf("Retry %d/%d: waiting for SFTP server...", i+1, maxRetries)
			time.Sleep(1 * time.Second)
		}
	}

	if !dockerSFTPAvailable {
		log.Printf("Failed to connect to SFTP server after retries: %v", testErr)
		// Cleanup before exit
		exec.Command("docker", "stop", testContainerName).Run()
		exec.Command("docker", "rm", testContainerName).Run()
		os.Exit(m.Run())
		return
	}

	// Run tests
	exitCode := m.Run()

	// Cleanup
	log.Println("Stopping and removing Docker SFTP test server...")
	stopCmd := exec.Command("docker", "stop", testContainerName)
	stopCmd.Run()
	rmCmd := exec.Command("docker", "rm", testContainerName)
	rmCmd.Run()
	log.Println("Docker container cleanup complete")

	os.Exit(exitCode)
}

func checkAndReadFile(t *testing.T, f fs.File) []byte {
	t.Helper()

	assert.True(t, f.Exists(), "Exists")
	assert.False(t, f.IsDir(), "not IsDir")
	data, err := f.ReadAll()
	assert.NoError(t, err)
	return data
}

func TestDialAndRegisterWithPublicOnlineServers(t *testing.T) {
	// https://www.sftp.net/public-online-sftp-servers
	t.Run("test.rebex.net", func(t *testing.T) {
		sftpFS, err := DialAndRegister(context.Background(), "demo@test.rebex.net:22", Password("password"), AcceptAnyHostKey)
		require.NoError(t, err, "Dial")

		require.Equal(t, "sftp://demo@test.rebex.net", sftpFS.Prefix())
		id, err := sftpFS.ID()
		require.NoError(t, err)
		require.Equal(t, "sftp://demo@test.rebex.net", id)
		require.Equal(t, "sftp://demo@test.rebex.net file system", sftpFS.String())
		require.Equal(t, "SFTP", sftpFS.Name())
		require.Equal(t, "/a/b", sftpFS.JoinCleanPath("a", "skip", "..", "/", "b", "/"))
		require.Equal(t, fs.File("sftp://demo@test.rebex.net/a/b"), sftpFS.JoinCleanFile("a", "skip", "..", "/", "b", "/"))

		f := fs.File("sftp://demo@test.rebex.net/readme.txt")
		assert.Equal(t, "readme.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		// files, err := fs.File("sftp://test.rebex.net:22/").ListDirMax(-1)
		// fmt.Println(files)
		// t.Fatal("todo")

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	})
	t.Run("demo.wftpserver.com", func(t *testing.T) {
		// http://demo.wftpserver.com/main.html
		sftpFS, err := DialAndRegister(context.Background(), "demo.wftpserver.com:2222", UsernameAndPassword("demo", "demo"), AcceptAnyHostKey)
		require.NoError(t, err, "Dial")
		require.Equal(t, "sftp://demo@demo.wftpserver.com:2222", sftpFS.Prefix())

		f := fs.File("sftp://demo@demo.wftpserver.com:2222/download/version.txt")
		assert.Equal(t, "version.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	})
}

func TestPasswordURLWithPublicOnlineServers(t *testing.T) {
	// https://www.sftp.net/public-online-sftp-servers
	t.Run("demo.wftpserver.com", func(t *testing.T) {
		// http://demo.wftpserver.com/main.html
		f := fs.File("sftp://demo:demo@demo.wftpserver.com:2222/download/version.txt")
		assert.Equal(t, "version.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)
	})
}

// Test_fileSystem tests the SFTP filesystem implementation using a dockerized SFTP server
func Test_fileSystem(t *testing.T) {
	if !dockerSFTPAvailable {
		t.Skip("Docker SFTP server not available")
	}

	ctx := context.Background()

	// Connect to the shared test SFTP server
	sftpFS, err := Dial(ctx, testSFTPAddress, UsernameAndPassword(testUsername, testPassword), AcceptAnyHostKey)
	require.NoError(t, err, "Failed to connect to SFTP server")
	defer sftpFS.Close()

	// Get expected prefix
	expectedPrefix := fmt.Sprintf("sftp://%s@localhost:%s", testUsername, testSFTPPort)

	// Run comprehensive filesystem tests
	fs.RunFileSystemTests(
		ctx,
		t,
		sftpFS,
		"SFTP",         // name
		expectedPrefix, // prefix
		testDataDir,    // testDir
	)
}
