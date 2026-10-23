// Package dockertest has the helpers of the Docker based tests
// of the go-fs backend modules. It is internal because it encodes
// the test policy of this repository, not an API for other modules.
package dockertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Available reports whether Docker can actually run containers,
// meaning the CLI is installed and its daemon answers.
//
// A CLI without a reachable daemon is the common case on CI runners and
// on developer machines with Docker Desktop stopped. Treating it as
// "installed" would fail the whole test run instead of skipping the
// Docker based tests as the repository policy requires.
func Available() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// docker info exits non-zero when the daemon is not reachable
	return exec.CommandContext(ctx, "docker", "info").Run() == nil
}

// SetupFailed is called by the TestMain of the backends with
// Docker based tests when Docker is available but the test server
// could not be built or started.
//
// Tests must only be skipped when Docker is not available at all;
// a broken test server must not pass silently. The exception is
// Windows, where Docker usually can't run the Linux images.
func SetupFailed(server string) {
	if runtime.GOOS == "windows" {
		log.Printf("Docker %s test server not available on Windows, skipping its tests", server)
		return
	}
	log.Printf("Docker is available but the %s test server could not be started, failing instead of skipping", server)
	os.Exit(1)
}

// ContainerName returns base with a random suffix, so that test runs
// in different worktrees or processes never use the same container name
// and don't stop or remove each other's containers.
func ContainerName(base string) string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	return base + "-" + hex.EncodeToString(b[:])
}
