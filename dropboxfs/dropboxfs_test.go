package dropboxfs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
)

func Test_fileSystem(t *testing.T) {
	// Check for required environment variables
	accessToken := os.Getenv("DROPBOX_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("Skipping Dropbox filesystem test: DROPBOX_ACCESS_TOKEN environment variable not set")
	}

	// Optional environment variables
	testDir := os.Getenv("DROPBOX_TEST_DIR")
	if testDir == "" {
		testDir = "/go-fs-test"
	}

	muteStr := os.Getenv("DROPBOX_MUTE")
	mute := muteStr == "true" || muteStr == "1"

	// Create the filesystem
	dbfs := NewAndRegister(accessToken, 5*time.Minute, mute)
	require.NotNil(t, dbfs, "NewAndRegister should return a filesystem")

	// Clean up after test
	t.Cleanup(func() {
		if err := dbfs.Close(); err != nil {
			t.Logf("Error closing filesystem: %v", err)
		}
	})

	// Run comprehensive filesystem tests
	fs.RunFileSystemTests(
		context.Background(),
		t,
		dbfs,                  // filesystem
		"Dropbox file system", // expected name
		"dropbox://",          // expected prefix
		testDir,               // test directory
	)
}

func Test_fileSystem_MuteConfiguration(t *testing.T) {
	// Check for required environment variables
	accessToken := os.Getenv("DROPBOX_ACCESS_TOKEN")
	if accessToken == "" {
		t.Skip("Skipping Dropbox filesystem test: DROPBOX_ACCESS_TOKEN environment variable not set")
	}

	testDir := os.Getenv("DROPBOX_TEST_DIR")
	if testDir == "" {
		testDir = "/go-fs-test-mute"
	}

	// Test with mute enabled
	t.Run("MuteEnabled", func(t *testing.T) {
		dbfs := NewAndRegister(accessToken, 5*time.Minute, true)
		require.NotNil(t, dbfs, "NewAndRegister with mute=true should return a filesystem")

		t.Cleanup(func() {
			if err := dbfs.Close(); err != nil {
				t.Logf("Error closing filesystem: %v", err)
			}
		})

		// Test that the filesystem was created successfully
		name := dbfs.Name()
		assert.Equal(t, "Dropbox file system", name, "Name should match expected value")

		prefix := dbfs.Prefix()
		assert.True(t, len(prefix) > 0, "Prefix should not be empty")
		assert.Contains(t, prefix, "dropbox://", "Prefix should contain dropbox://")
	})

	// Test with mute disabled
	t.Run("MuteDisabled", func(t *testing.T) {
		dbfs := NewAndRegister(accessToken, 5*time.Minute, false)
		require.NotNil(t, dbfs, "NewAndRegister with mute=false should return a filesystem")

		t.Cleanup(func() {
			if err := dbfs.Close(); err != nil {
				t.Logf("Error closing filesystem: %v", err)
			}
		})

		// Test that the filesystem was created successfully
		name := dbfs.Name()
		assert.Equal(t, "Dropbox file system", name, "Name should match expected value")

		prefix := dbfs.Prefix()
		assert.True(t, len(prefix) > 0, "Prefix should not be empty")
		assert.Contains(t, prefix, "dropbox://", "Prefix should contain dropbox://")
	})
}
