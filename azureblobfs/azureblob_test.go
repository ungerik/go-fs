package azureblobfs

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
)

const (
	testContainerName = "azurite-test-server"
	testBlobPort      = "10000"
	testContainer     = "testcontainer"

	// The well-known Azurite development storage account
	testConnectionString = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=http://127.0.0.1:" + testBlobPort + "/devstoreaccount1;"
)

var dockerAzuriteAvailable bool

// TestMain starts an Azurite container (the Azure Storage emulator).
// Tests are skipped when Docker is not installed and fail when it is
// installed but the emulator can't be started.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		log.Println("Docker not available, skipping Docker-based Azure Blob tests")
		os.Exit(m.Run())
	}
	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	exec.Command("docker", "stop", testContainerName).Run()
	exec.Command("docker", "rm", testContainerName).Run()

	log.Println("Starting Docker Azurite test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", fmt.Sprintf("%s:10000", testBlobPort),
		"mcr.microsoft.com/azure-storage/azurite",
		"azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck", "--loose",
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		fstest.DockerSetupFailed("Azurite")
		os.Exit(m.Run())
	}

	for i := range 20 {
		client, err := container.NewClientFromConnectionString(testConnectionString, testContainer, nil)
		if err == nil {
			_, err = client.Create(ctx, nil)
			if err != nil && isContainerExists(err) {
				err = nil
			}
		}
		if err == nil {
			dockerAzuriteAvailable = true
			log.Println("Azurite server is ready")
			break
		}
		if i == 19 {
			log.Printf("Failed to connect to the Azurite server: %v", err)
		}
		time.Sleep(time.Second)
	}
	if !dockerAzuriteAvailable {
		exec.Command("docker", "stop", testContainerName).Run()
		exec.Command("docker", "rm", testContainerName).Run()
		fstest.DockerSetupFailed("Azurite")
		os.Exit(m.Run())
	}

	exitCode := m.Run()

	log.Println("Stopping and removing Docker Azurite test server...")
	exec.Command("docker", "stop", testContainerName).Run()
	exec.Command("docker", "rm", testContainerName).Run()
	os.Exit(exitCode)
}

func Test_fileSystem(t *testing.T) {
	if !dockerAzuriteAvailable {
		t.Skip("Docker Azurite server not available")
	}
	blobFS, err := NewFromConnectionString(t.Context(), testConnectionString, testContainer, false)
	require.NoError(t, err, "NewFromConnectionString")
	defer blobFS.Close()

	expectedPrefix := fmt.Sprintf("azblob://127.0.0.1:%s/devstoreaccount1/%s", testBlobPort, testContainer)
	require.Equal(t, expectedPrefix, blobFS.Prefix())

	fstest.RunConformance(t, blobFS, fstest.Config{
		Name:    "Azure Blob file system",
		Prefix:  expectedPrefix,
		TestDir: "/conformance",
	})
}

// TestRangedRead verifies seeking reads and the native server-side copy.
func TestRangedRead(t *testing.T) {
	if !dockerAzuriteAvailable {
		t.Skip("Docker Azurite server not available")
	}
	ctx := t.Context()
	blobFS, err := NewFromConnectionString(ctx, testConnectionString, testContainer, false)
	require.NoError(t, err)
	defer blobFS.Close()

	file := blobFS.RootDir().Join("ranged", "data.txt")
	require.NoError(t, file.WriteAllString(ctx, "0123456789"))
	t.Cleanup(func() { _ = blobFS.RootDir().Join("ranged").RemoveRecursive(context.Background()) })

	r, err := file.OpenReadSeeker()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, isRangeReader := r.(*rangeReader)
	assert.True(t, isRangeReader, "the native range reader must be used")

	_, err = r.Seek(-3, io.SeekEnd)
	require.NoError(t, err)
	tail, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "789", string(tail))

	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 2)
	require.NoError(t, err)
	assert.Equal(t, "2345", string(buf[:n]))

	copied := blobFS.RootDir().Join("ranged", "copy.txt")
	require.NoError(t, fs.CopyFile(ctx, file, copied))
	data, err := copied.ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0123456789", data)
}

func isContainerExists(err error) bool {
	return bloberror.HasCode(err, bloberror.ContainerAlreadyExists)
}
