package dropboxfs

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fstest"
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
	dbfs, err := NewAndRegister(t.Context(), accessToken, 5*time.Minute, mute)
	require.NoError(t, err, "NewAndRegister")
	assert.True(t, strings.HasPrefix(dbfs.ID(), "dbid:"), "ID must be the Dropbox account id")

	// Clean up after test
	t.Cleanup(func() {
		if err := dbfs.Close(); err != nil {
			t.Logf("Error closing filesystem: %v", err)
		}
	})

	// Run comprehensive filesystem tests
	fstest.RunConformance(t, dbfs, fstest.Config{
		Name:    "Dropbox file system",
		Prefix:  dbfs.Prefix(),
		TestDir: testDir,
	})
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
		dbfs, err := NewAndRegister(t.Context(), accessToken, 5*time.Minute, true)
		require.NoError(t, err, "NewAndRegister with mute=true")

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
		dbfs, err := NewAndRegister(t.Context(), accessToken, 5*time.Minute, false)
		require.NoError(t, err, "NewAndRegister with mute=false")

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
