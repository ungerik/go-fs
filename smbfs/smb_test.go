package smbfs

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

const (
	testContainerName = "smb-test-server"
	testSMBPort       = "1445"
	testUsername      = "testuser"
	testPassword      = "testpass"
	testShare         = "share"
)

var (
	dockerSMBAvailable bool
	testSMBAddress     string
)

// TestMain starts a Samba container (dperson/samba) with one share
// for the test user. Tests are skipped when Docker is not installed and
// fail when it is installed but the server can't be started.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		log.Println("Docker not available, skipping Docker-based SMB tests")
		os.Exit(m.Run())
	}
	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	exec.Command("docker", "stop", testContainerName).Run()
	exec.Command("docker", "rm", testContainerName).Run()

	log.Println("Starting Docker Samba test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", fmt.Sprintf("%s:445", testSMBPort),
		"dperson/samba",
		"-p",
		"-u", fmt.Sprintf("%s;%s", testUsername, testPassword),
		"-s", fmt.Sprintf("%s;/share;no;no;no;%s", testShare, testUsername),
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		fstest.DockerSetupFailed("Samba")
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
		exec.Command("docker", "stop", testContainerName).Run()
		exec.Command("docker", "rm", testContainerName).Run()
		fstest.DockerSetupFailed("Samba")
		os.Exit(m.Run())
	}

	exitCode := m.Run()

	log.Println("Stopping and removing Docker Samba test server...")
	exec.Command("docker", "stop", testContainerName).Run()
	exec.Command("docker", "rm", testContainerName).Run()
	os.Exit(exitCode)
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
