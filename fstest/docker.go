package fstest

import (
	"log"
	"os"
	"runtime"
)

// DockerSetupFailed is called by the TestMain of the backends with
// Docker based tests when Docker is installed but the test server
// could not be built or started.
//
// Tests must only be skipped when Docker is not available at all;
// a broken test server must not pass silently. The exception is
// Windows, where Docker usually can't run the Linux images.
func DockerSetupFailed(server string) {
	if runtime.GOOS == "windows" {
		log.Printf("Docker %s test server not available on Windows, skipping its tests", server)
		return
	}
	log.Printf("Docker is installed but the %s test server could not be started, failing instead of skipping", server)
	os.Exit(1)
}
