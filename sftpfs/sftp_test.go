package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
)

const (
	testImage    = "sftp-test-server"
	testUsername = "testuser"
	testPassword = "testpass"
	testDataDir  = "/home/testuser/testdata"
)

var (
	dockerSFTPAvailable bool
	// testContainerName is unique for every test run and testSFTPPort
	// is the host port that Docker assigned, so that test runs in other
	// worktrees don't stop the container or take the port.
	testContainerName string
	testSFTPPort      string
	testSFTPAddress   string
)

func TestMain(m *testing.M) {
	// Check if Docker is available
	if !dockertest.Available() {
		log.Println("Docker not available, skipping Docker-based SFTP tests")
		dockerSFTPAvailable = false
		os.Exit(m.Run())
		return
	}

	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	testContainerName = dockertest.ContainerName("sftpfs-test")

	// Build the Docker image
	log.Println("Building Docker SFTP test server image...")
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", "Dockerfile.sftp-test",
		"-t", testImage,
		".",
	)
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to build Docker image: %v\nOutput: %s", err, output)
		dockertest.SetupFailed("SFTP")
		os.Exit(m.Run())
		return
	}
	log.Println("Docker image built successfully")

	// Start the SFTP server container on a host port that Docker picks
	log.Println("Starting Docker SFTP test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", "127.0.0.1::22",
		// The test data is kept in memory, so it doesn't depend on free
		// space on the Docker disk. testuser is the first user of the image.
		"--tmpfs", testDataDir+":rw,uid=1000,gid=1000,mode=755",
		testImage,
	)
	output, err = runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		removeContainer(testContainerName)
		dockertest.SetupFailed("SFTP")
		os.Exit(m.Run())
		return
	}
	log.Printf("Started Docker container: %s", testContainerName)
	testSFTPPort, err = dockerHostPort(ctx, testContainerName, "22/tcp")
	if err != nil {
		log.Printf("Failed to get the host port of the SFTP container: %v", err)
		removeContainer(testContainerName)
		dockertest.SetupFailed("SFTP")
		os.Exit(m.Run())
		return
	}

	// Wait for SSH server to be ready with retry logic
	log.Println("Waiting for SFTP server to be ready...")
	time.Sleep(3 * time.Second)

	testSFTPAddress = fmt.Sprintf("localhost:%s", testSFTPPort)
	maxRetries := 10
	var testErr error
	for i := range maxRetries {
		sftpFS, err := Dial(ctx, testSFTPAddress, dockerOptions())
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
		removeContainer(testContainerName)
		dockertest.SetupFailed("SFTP")
		os.Exit(m.Run())
		return
	}

	// Run tests
	exitCode := m.Run()

	log.Println("Stopping and removing Docker SFTP test server...")
	removeContainer(testContainerName)
	log.Println("Docker container cleanup complete")

	os.Exit(exitCode)
}

// dockerOptions returns the Options for the Docker SFTP server.
func dockerOptions() *Options {
	return &Options{
		Username:        testUsername,
		Password:        testPassword,
		HostKeyCallback: AcceptAnyHostKey,
	}
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

func checkAndReadFile(t *testing.T, f fs.File) []byte {
	t.Helper()

	assert.True(t, f.Exists(), "Exists")
	assert.False(t, f.IsDir(), "not IsDir")
	data, err := f.ReadAll(t.Context())
	assert.NoError(t, err)
	return data
}

func TestDialAndRegisterWithPublicOnlineServers(t *testing.T) {
	if os.Getenv("GOFS_ONLINE_TESTS") == "" {
		t.Skip("set GOFS_ONLINE_TESTS=1 to run tests against public internet SFTP servers")
	}
	// https://www.sftp.net/public-online-sftp-servers
	t.Run("test.rebex.net", func(t *testing.T) {
		err := DialAndRegister(t.Context(), "demo@test.rebex.net:22", &Options{Password: "password", HostKeyCallback: AcceptAnyHostKey})
		require.NoError(t, err, "Dial")
		sftpFS := fs.GetFileSystemByPrefixOrNil("sftp://demo@test.rebex.net")
		require.NotNil(t, sftpFS, "registered")

		require.Equal(t, "sftp://demo@test.rebex.net", sftpFS.Prefix())
		require.Equal(t, "sftp://demo@test.rebex.net", sftpFS.ID())
		require.Equal(t, "SFTP sftp://demo@test.rebex.net", sftpFS.String())
		require.Equal(t, "SFTP", sftpFS.Name())
		require.Equal(t, "/a/b", sftpFS.CleanPath("a", "skip", "..", "/", "b", "/"))
		require.Equal(t, fs.File("sftp://demo@test.rebex.net/a/b"), sftpFS.(*fileSystem).JoinCleanFile("a", "skip", "..", "/", "b", "/"))

		f := fs.File("sftp://demo@test.rebex.net/readme.txt")
		assert.Equal(t, "readme.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	})
	t.Run("demo.wftpserver.com", func(t *testing.T) {
		// http://demo.wftpserver.com/main.html
		err := DialAndRegister(t.Context(), "demo.wftpserver.com:2222", &Options{Username: "demo", Password: "demo", HostKeyCallback: AcceptAnyHostKey})
		require.NoError(t, err, "Dial")
		sftpFS := fs.GetFileSystemByPrefixOrNil("sftp://demo@demo.wftpserver.com:2222")
		require.NotNil(t, sftpFS, "registered")

		f := fs.File("sftp://demo@demo.wftpserver.com:2222/download/version.txt")
		assert.Equal(t, "version.txt", f.Name())
		data := checkAndReadFile(t, f)
		assert.True(t, len(data) > 0)

		err = sftpFS.Close()
		require.NoError(t, err, "Close")
	})
}

func TestPasswordURLWithPublicOnlineServers(t *testing.T) {
	if os.Getenv("GOFS_ONLINE_TESTS") == "" {
		t.Skip("set GOFS_ONLINE_TESTS=1 to run tests against public internet SFTP servers")
	}
	// https://www.sftp.net/public-online-sftp-servers
	t.Run("demo.wftpserver.com", func(t *testing.T) {
		// http://demo.wftpserver.com/main.html
		// A URI with credentials is dialed with EnsureRegistered,
		// the File URI has the prefix without the password
		free, err := EnsureRegistered(t.Context(), "sftp://demo:demo@demo.wftpserver.com:2222/download/version.txt", &Options{HostKeyCallback: AcceptAnyHostKey})
		require.NoError(t, err, "EnsureRegistered")
		defer func() { require.NoError(t, free()) }()

		f := fs.File("sftp://demo@demo.wftpserver.com:2222/download/version.txt")
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

	ctx := t.Context()

	// Connect to the shared test SFTP server
	sftpFS, err := Dial(ctx, testSFTPAddress, dockerOptions())
	require.NoError(t, err, "Failed to connect to SFTP server")
	defer sftpFS.Close()
	require.True(t, sftpFS.(*fileSystem).currentConn().posixRename, "OpenSSH supports %s", posixRenameExtension)

	// Get expected prefix
	expectedPrefix := fmt.Sprintf("sftp://%s@localhost:%s", testUsername, testSFTPPort)

	// Run comprehensive filesystem tests
	fstest.RunConformance(t, sftpFS, fstest.Config{
		Name:    "SFTP",
		Prefix:  expectedPrefix,
		TestDir: testDataDir,
	})
}

// TestOpenSSHReconnectAndClose kills the SSH session of the client on
// the OpenSSH server: the next operation must reconnect, and Close must
// close the SSH connection, not only the SFTP session.
func TestOpenSSHReconnectAndClose(t *testing.T) {
	if !dockerSFTPAvailable {
		t.Skip("Docker SFTP server not available")
	}
	sftpFS, err := Dial(t.Context(), testSFTPAddress, dockerOptions())
	require.NoError(t, err)
	f := sftpFS.(*fileSystem)
	t.Cleanup(func() { _ = f.Close() })
	first := f.currentConn()

	waitFor(t, func() bool { return dockerUserProcesses(t) > 0 }, "the processes of the SSH session")
	// The busybox pkill of the container has no -u
	out, err := exec.CommandContext(t.Context(), "docker", "exec", testContainerName, "sh", "-c",
		"ps -o pid,user | awk '$2 == \""+testUsername+"\" {print $1}' | xargs kill").CombinedOutput()
	require.NoError(t, err, "kill: %s", out)
	waitFor(t, first.isLost, "the client to notice the lost connection")

	_, err = f.Stat(testDataDir)
	require.NoError(t, err, "Stat must reconnect")
	require.NotSame(t, first, f.currentConn(), "new connection")

	require.NoError(t, f.Close())
	waitFor(t, func() bool { return dockerUserProcesses(t) == 0 }, "Close to end the SSH session")
}

// dockerUserProcesses returns the number of processes of the test user
// in the Docker container, which run while it has an SSH session.
func dockerUserProcesses(t *testing.T) int {
	t.Helper()
	out, _ := exec.CommandContext(t.Context(), "docker", "exec", testContainerName, "ps", "-o", "user").Output()
	n := 0
	for _, user := range strings.Fields(string(out)) {
		if user == testUsername {
			n++
		}
	}
	return n
}

// Test_fileSystemInProcess runs the conformance suite offline against
// the in-process server serving a temporary directory, which implements
// SFTP renames with os.Rename. That one replaces existing files and
// empty directories, so the overwrite rules must be checked by sftpfs.
func Test_fileSystemInProcess(t *testing.T) {
	root := filepath.ToSlash(t.TempDir()) // SFTP paths use slashes, also on a server on Windows
	s := startTestServer(t, testServerConfig{root: root})
	f := s.dial(t, nil)
	cfg := fstest.Config{
		Name:    "SFTP",
		Prefix:  "sftp://" + testServerUser + "@" + s.addr,
		TestDir: root + "/testdata",
	}
	if runtime.GOOS == "windows" {
		// The in-process server stores the files on the Windows host,
		// where os.Chmod only toggles the read-only attribute and
		// os.Symlink rewrites the target with backslashes. Real SFTP
		// servers are checked by Test_fileSystem against OpenSSH.
		cfg.PermissionMask = fs.UserWrite
		cfg.Skip = func(name string) bool { return name == "OptionalWrite/SymbolicLink" }
	}
	fstest.RunConformance(t, f, cfg)
}

// TestMoveWithoutPosixRename replaces an existing file without the
// posix-rename@openssh.com extension by removing it before the rename.
func TestMoveWithoutPosixRename(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	s := startTestServer(t, testServerConfig{root: root})
	f := s.dial(t, nil)
	f.currentConn().posixRename = false

	require.NoError(t, os.WriteFile(root+"/src.txt", []byte("source"), 0o600))
	require.NoError(t, os.WriteFile(root+"/dest.txt", []byte("destination"), 0o600))
	require.NoError(t, f.Move(t.Context(), root+"/src.txt", root+"/dest.txt"))
	data, err := os.ReadFile(root + "/dest.txt")
	require.NoError(t, err)
	assert.Equal(t, "source", string(data))
	assert.NoFileExists(t, root+"/src.txt")

	// A directory is never replaced
	require.NoError(t, os.Mkdir(root+"/dir", 0o700))
	require.NoError(t, os.WriteFile(root+"/file.txt", []byte("file"), 0o600))
	err = f.Move(t.Context(), root+"/file.txt", root+"/dir")
	assert.ErrorIs(t, err, os.ErrExist)
	assert.FileExists(t, root+"/file.txt")

	// Another file with the same size, permissions and modification time
	// might be the source under another name, so the source is moved away
	// before that file is removed
	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	for name, content := range map[string]string{"/a.txt": "aaa", "/b.txt": "bbb"} {
		require.NoError(t, os.WriteFile(root+name, []byte(content), 0o600))
		require.NoError(t, os.Chtimes(root+name, modTime, modTime))
	}
	require.NoError(t, f.Move(t.Context(), root+"/a.txt", root+"/b.txt"))
	data, err = os.ReadFile(root + "/b.txt")
	require.NoError(t, err)
	assert.Equal(t, "aaa", string(data))
	assert.Equal(t, []string{"b.txt", "dest.txt", "dir", "file.txt"}, dirNames(t, root), "no temporary file")
}

// TestMoveCaseOnlyWithoutPosixRename changes the case of a file name
// without the posix-rename@openssh.com extension on a case-insensitive
// file system, where the destination is the source under another name:
// removing the destination before the rename removed the source.
func TestMoveCaseOnlyWithoutPosixRename(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	require.NoError(t, os.WriteFile(root+"/file.txt", []byte("content"), 0o600))
	if _, err := os.Stat(root + "/FILE.TXT"); err != nil {
		t.Skip("the temporary directory is on a case-sensitive file system")
	}
	s := startTestServer(t, testServerConfig{root: root})
	f := s.dial(t, nil)
	f.currentConn().posixRename = false

	require.NoError(t, f.Move(t.Context(), root+"/file.txt", root+"/File.txt"))
	data, err := os.ReadFile(root + "/File.txt")
	require.NoError(t, err, "the file must still exist")
	assert.Equal(t, "content", string(data))
	assert.Equal(t, []string{"File.txt"}, dirNames(t, root), "the file has the new name, no temporary file is left")
}

// TestMoveAliasWithoutPosixRename moves a file without the
// posix-rename@openssh.com extension to a destination path that reaches
// the same file through a symbolic link to its directory.
func TestMoveAliasWithoutPosixRename(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	require.NoError(t, os.Mkdir(root+"/dir", 0o700))
	require.NoError(t, os.Symlink("dir", root+"/link"))
	require.NoError(t, os.WriteFile(root+"/dir/file.txt", []byte("content"), 0o600))
	s := startTestServer(t, testServerConfig{root: root})
	f := s.dial(t, nil)
	f.currentConn().posixRename = false

	require.NoError(t, f.Move(t.Context(), root+"/dir/file.txt", root+"/link/file.txt"))
	data, err := os.ReadFile(root + "/dir/file.txt")
	require.NoError(t, err, "the file must still exist")
	assert.Equal(t, "content", string(data))
	assert.Equal(t, []string{"file.txt"}, dirNames(t, root+"/dir"), "no temporary file is left")
}

// TestMoveAliasRenamesBack fails the last rename of a move through a
// temporary name: the source must be renamed back.
func TestMoveAliasRenamesBack(t *testing.T) {
	s := startTestServer(t, testServerConfig{fail: func(r *sftp.Request) error {
		if r.Method == "Rename" && r.Target == "/dest.txt" {
			return errors.New("injected rename failure")
		}
		return nil
	}})
	f := s.dial(t, nil)
	f.currentConn().posixRename = false
	writeTestFile(t, f, "/src.txt", "content")
	// A hard link is the same file under another name
	require.NoError(t, f.currentConn().sftp.Link("/src.txt", "/dest.txt"))

	err := f.Move(t.Context(), "/src.txt", "/dest.txt")
	require.ErrorContains(t, err, "injected rename failure")
	r, err := f.OpenReader("/src.txt")
	require.NoError(t, err, "the source must be renamed back")
	data, err := io.ReadAll(r)
	require.NoError(t, errors.Join(err, r.Close()))
	assert.Equal(t, "content", string(data))
	var names []string
	require.NoError(t, f.ListDir(t.Context(), "/", nil, func(info *fs.FileInfo) error {
		names = append(names, info.Name)
		return nil
	}))
	assert.Equal(t, []string{"src.txt"}, names, "no temporary file is left")
}

// TestMoveLinkOntoTarget is a regression test: moving a symbolic link onto
// the file it points to replaced the file with the link, which then pointed
// to itself, so the data of the file was gone. With the posix-rename
// extension the rename replaced the file, without it the file was removed
// before the rename. Move must refuse like mv with an error wrapping
// os.ErrInvalid and keep the file and the link, for a relative and an
// absolute target and for one through a symbolic link to its directory.
func TestMoveLinkOntoTarget(t *testing.T) {
	for _, posixRename := range []bool{true, false} {
		for name, target := range map[string]func(root string) string{
			"relative":    func(string) string { return "dir/file.txt" },
			"absolute":    func(root string) string { return root + "/dir/file.txt" },
			"through-dir": func(string) string { return "dirlink/file.txt" },
		} {
			t.Run(fmt.Sprintf("%s/posixRename=%t", name, posixRename), func(t *testing.T) {
				root := filepath.ToSlash(t.TempDir())
				require.NoError(t, os.Mkdir(root+"/dir", 0o700))
				require.NoError(t, os.Symlink("dir", root+"/dirlink"))
				require.NoError(t, os.WriteFile(root+"/dir/file.txt", []byte("content"), 0o600))
				require.NoError(t, os.Symlink(target(root), root+"/link"))
				s := startTestServer(t, testServerConfig{root: root})
				f := s.dial(t, nil)
				f.currentConn().posixRename = posixRename

				err := f.Move(t.Context(), root+"/link", root+"/dir/file.txt")
				require.ErrorIs(t, err, os.ErrInvalid)
				assert.ErrorContains(t, err, "are the same file")
				data, err := os.ReadFile(root + "/dir/file.txt")
				require.NoError(t, err, "the file must still exist")
				assert.Equal(t, "content", string(data))
				linkInfo, err := os.Lstat(root + "/link")
				require.NoError(t, err, "the link must still exist")
				assert.NotZero(t, linkInfo.Mode()&os.ModeSymlink, "the link must still be a link")
				data, err = os.ReadFile(root + "/link")
				require.NoError(t, err, "the link must still point to the file")
				assert.Equal(t, "content", string(data))
				assert.Equal(t, []string{"file.txt"}, dirNames(t, root+"/dir"), "no temporary file is left")
			})
		}
	}
}

// TestMoveLinkOntoOtherFile moves symbolic links onto files that are not
// their target, which they replace like any other file, and onto another
// hard link of their target. SFTP reports no link count that would tell
// whether the other link keeps the file, so that move is refused like
// one onto the target itself.
func TestMoveLinkOntoOtherFile(t *testing.T) {
	for _, posixRename := range []bool{true, false} {
		t.Run(fmt.Sprintf("posixRename=%t", posixRename), func(t *testing.T) {
			root := filepath.ToSlash(t.TempDir())
			require.NoError(t, os.WriteFile(root+"/target.txt", []byte("target"), 0o600))
			require.NoError(t, os.WriteFile(root+"/other.txt", []byte("other file"), 0o600))
			require.NoError(t, os.Symlink("target.txt", root+"/link"))
			s := startTestServer(t, testServerConfig{root: root})
			f := s.dial(t, nil)
			f.currentConn().posixRename = posixRename

			require.NoError(t, f.Move(t.Context(), root+"/link", root+"/other.txt"))
			linkTarget, err := os.Readlink(root + "/other.txt")
			require.NoError(t, err, "the link must have replaced the file")
			assert.Equal(t, "target.txt", linkTarget)
			assert.NoFileExists(t, root+"/link")

			// A dangling link has no target that the move could remove
			require.NoError(t, os.Symlink("missing.txt", root+"/dangling"))
			require.NoError(t, f.Move(t.Context(), root+"/dangling", root+"/other.txt"))
			linkTarget, err = os.Readlink(root + "/other.txt")
			require.NoError(t, err)
			assert.Equal(t, "missing.txt", linkTarget)

			require.NoError(t, os.Link(root+"/target.txt", root+"/hardlink.txt"))
			require.NoError(t, os.Symlink("target.txt", root+"/link"))
			err = f.Move(t.Context(), root+"/link", root+"/hardlink.txt")
			require.ErrorIs(t, err, os.ErrInvalid)
			for _, name := range []string{"/target.txt", "/hardlink.txt"} {
				data, err := os.ReadFile(root + name)
				require.NoError(t, err, "%s must still exist", name)
				assert.Equal(t, "target", string(data), name)
			}
		})
	}
}

// TestListDirRecursiveSymlink lists a symbolic link to a directory:
// the files of the directory are listed below the path of the link.
func TestListDirRecursiveSymlink(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	require.NoError(t, os.MkdirAll(root+"/dir/sub", 0o700))
	require.NoError(t, os.WriteFile(root+"/dir/a.txt", []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(root+"/dir/sub/b.txt", []byte("b"), 0o600))
	require.NoError(t, os.Symlink("dir", root+"/link"))
	s := startTestServer(t, testServerConfig{root: root})
	f := s.dial(t, nil)

	var listed []fs.File
	require.NoError(t, f.ListDirRecursive(t.Context(), root+"/link", nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.File)
		assert.False(t, info.IsDir, "%s is no directory", info.File)
		return nil
	}))
	// The server lists the entries in the order of the directory
	assert.ElementsMatch(t, []fs.File{f.JoinCleanFile(root, "link/a.txt"), f.JoinCleanFile(root, "link/sub/b.txt")}, listed)
}

// TestRemoveRoot checks that Remove and RemoveAll refuse the root directory
// with an error wrapping os.ErrInvalid. RemoveAll removed every file of the
// server, Remove sent the server a request to remove its root directory.
// The server resolves a relative path like "." or "" against the login
// directory, which the guards let through as long as they only compared
// the cleaned path with "/".
func TestRemoveRoot(t *testing.T) {
	var removeRequests atomic.Int32
	s := startTestServer(t, testServerConfig{fail: func(r *sftp.Request) error {
		if r.Method == "Rmdir" || r.Method == "Remove" {
			removeRequests.Add(1)
		}
		return nil
	}})
	f := s.dial(t, nil)
	writeTestFile(t, f, "/file.txt", "content")

	for _, root := range []string{"/", "//", "/.", "/dir/..", "", ".", "./", "dir/..", ".."} {
		err := f.RemoveAll(t.Context(), root)
		assert.ErrorIs(t, err, os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorContains(t, err, "can't remove the root directory", "RemoveAll(%q)", root)
		err = f.Remove(root)
		assert.ErrorIs(t, err, os.ErrInvalid, "Remove(%q)", root)
		assert.ErrorContains(t, err, "can't remove the root directory", "Remove(%q)", root)
	}
	assert.Zero(t, removeRequests.Load(), "removing the root must not send a remove request")
	_, err := f.Stat("/file.txt")
	assert.NoError(t, err, "the files of the root directory must not be removed")
}

// TestOpenReaderReturnsNilInterface checks that the opening methods
// return a nil interface, not a typed nil *sftpFile, with an error.
func TestOpenReaderReturnsNilInterface(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)

	r, err := f.OpenReader("/missing.txt")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.True(t, r == nil, "OpenReader returned %#v", r)
	assert.Contains(t, err.Error(), "sftp://u@"+s.addr+"/missing.txt", "the error names the file")

	w, err := f.OpenWriter("/missing-dir/file.txt", 0)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.True(t, w == nil, "OpenWriter returned %#v", w)
}

// TestListDirCallbackError checks that an error returned by a listing callback,
// even a net.Error or io.EOF, is returned as it is: the listing is not repeated
// and the healthy connection is kept.
func TestListDirCallbackError(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	for _, name := range []string{"/a", "/b", "/c"} {
		writeTestFile(t, f, name, name)
	}
	first := f.currentConn()

	for _, callbackErr := range []error{
		&net.OpError{Op: "write", Net: "tcp", Err: errors.New("i/o timeout of an upload in the callback")},
		io.EOF,
	} {
		var listDirCalls, recursiveCalls []string
		err := f.ListDir(t.Context(), "/", nil, func(info *fs.FileInfo) error {
			listDirCalls = append(listDirCalls, info.Name)
			if len(listDirCalls) == 2 {
				return callbackErr
			}
			return nil
		})
		assert.Same(t, callbackErr, err, "ListDir must return the callback error")
		assert.Len(t, listDirCalls, 2, "ListDir must not repeat callbacks: %v", listDirCalls)

		err = f.ListDirRecursive(t.Context(), "/", nil, func(info *fs.FileInfo) error {
			recursiveCalls = append(recursiveCalls, info.Name)
			if len(recursiveCalls) == 2 {
				return callbackErr
			}
			return nil
		})
		assert.Same(t, callbackErr, err, "ListDirRecursive must return the callback error")
		assert.Len(t, recursiveCalls, 2, "ListDirRecursive must not repeat callbacks: %v", recursiveCalls)
	}
	assert.Same(t, first, f.currentConn(), "a callback error must not drop the connection")
	assert.Equal(t, int64(1), s.accepted.Load(), "no reconnect")
	assert.Equal(t, int64(1), s.open.Load(), "open SSH connections")
}

// TestListDirContextKeepsConnection checks that a context that times out
// while the callbacks run is no connection error: the connection is kept,
// not dropped and leaked.
func TestListDirContextKeepsConnection(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	writeTestFile(t, f, "/a", "a")
	writeTestFile(t, f, "/b", "b")
	first := f.currentConn()
	slow := func(*fs.FileInfo) error { time.Sleep(40 * time.Millisecond); return nil }
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		err := f.ListDir(ctx, "/", nil, slow)
		cancel()
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
		err = f.ListDirRecursive(ctx, "/", nil, slow)
		cancel()
		assert.ErrorIs(t, err, context.DeadlineExceeded)

		ctx, cancel = context.WithCancel(t.Context())
		cancel()
		assert.ErrorIs(t, f.RemoveAll(ctx, "/a"), context.Canceled)
	}
	assert.Same(t, first, f.currentConn(), "a context error must not drop the connection")
	assert.Equal(t, int64(1), s.accepted.Load(), "no reconnect")
	assert.Equal(t, int64(1), s.open.Load(), "open SSH connections")
}

// TestListDirRecursiveLostAfterCallback loses the connection after the
// first callback: the walk must not start again and repeat callbacks.
func TestListDirRecursiveLostAfterCallback(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	require.NoError(t, f.MakeAllDirs("/dir/sub", 0))
	for _, name := range []string{"/dir/a", "/dir/sub/b", "/dir/sub/c"} {
		writeTestFile(t, f, name, name)
	}
	var calls []fs.File
	err := f.ListDirRecursive(t.Context(), "/dir", nil, func(info *fs.FileInfo) error {
		calls = append(calls, info.File)
		if len(calls) == 1 {
			s.killConns()
			s.waitOpen(t, 0)
		}
		return nil
	})
	assert.True(t, isConnectionError(err), "connection error expected, got %v", err)
	assert.Len(t, calls, 1, "callbacks must not be repeated: %v", calls)
}

// TestListDirRecursiveRetriedBeforeCallback loses the connection
// before the first callback, then the walk is retried.
func TestListDirRecursiveRetriedBeforeCallback(t *testing.T) {
	s := startTestServer(t, testServerConfig{after: killOnce("List", "/sub")})
	f := s.dial(t, nil)
	require.NoError(t, f.MakeAllDirs("/dir/sub", 0))
	writeTestFile(t, f, "/dir/sub/b", "b")
	writeTestFile(t, f, "/dir/sub/c", "c")
	var calls []fs.File
	err := f.ListDirRecursive(t.Context(), "/dir", nil, func(info *fs.FileInfo) error {
		calls = append(calls, info.File)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []fs.File{f.JoinCleanFile("/dir/sub/b"), f.JoinCleanFile("/dir/sub/c")}, calls)
	assert.Equal(t, int64(2), s.accepted.Load(), "reconnected once")
}

// TestIdempotentOperationsRetried loses the connection after the server
// executed the request of an operation that can be repeated:
// it is retried with a new connection and succeeds.
func TestIdempotentOperationsRetried(t *testing.T) {
	for _, test := range []struct {
		method string
		op     func(t *testing.T, f *fileSystem) error
	}{
		{"Lstat", func(t *testing.T, f *fileSystem) error { _, err := f.Stat("/file.txt"); return err }},
		{"List", func(t *testing.T, f *fileSystem) error {
			return f.ListDir(t.Context(), "/", nil, func(*fs.FileInfo) error { return nil })
		}},
		{"Setstat", func(t *testing.T, f *fileSystem) error { return f.Truncate(t.Context(), "/file.txt", 2) }},
		{"Get", func(t *testing.T, f *fileSystem) error {
			r, err := f.OpenReader("/file.txt")
			if err != nil {
				return err
			}
			return r.Close()
		}},
	} {
		t.Run(test.method, func(t *testing.T) {
			var armed atomic.Bool
			s := startTestServer(t, testServerConfig{after: func(s *testServer, r *sftp.Request) {
				if r.Method == test.method && armed.CompareAndSwap(true, false) {
					s.killConns()
				}
			}})
			f := s.dial(t, nil)
			writeTestFile(t, f, "/file.txt", "content")
			armed.Store(true)
			require.NoError(t, test.op(t, f))
			assert.False(t, armed.Load(), "the hook did not kill the connection")
			assert.Equal(t, int64(2), s.accepted.Load(), "reconnected once")
		})
	}
}

// TestNonIdempotentOperationsNotRetried loses the connection after the
// server executed the request of an operation that must not be repeated:
// it returns the connection error instead of the error of a repetition,
// like os.ErrNotExist for a repeated Remove.
func TestNonIdempotentOperationsNotRetried(t *testing.T) {
	for _, test := range []struct {
		name, method string
		op           func(t *testing.T, f *fileSystem) error
		check        func(t *testing.T, f *fileSystem)
	}{
		{"Remove", "Remove", func(t *testing.T, f *fileSystem) error { return f.Remove("/file.txt") }, func(t *testing.T, f *fileSystem) {
			_, err := f.Stat("/file.txt")
			assert.ErrorIs(t, err, os.ErrNotExist, "the server removed the file")
		}},
		{"RemoveDir", "Rmdir", func(t *testing.T, f *fileSystem) error { return f.Remove("/dir") }, func(t *testing.T, f *fileSystem) {
			_, err := f.Stat("/dir")
			assert.ErrorIs(t, err, os.ErrNotExist, "the server removed the directory")
		}},
		{"MakeDir", "Mkdir", func(t *testing.T, f *fileSystem) error { return f.MakeDir("/new", 0) }, func(t *testing.T, f *fileSystem) {
			info, err := f.Stat("/new")
			require.NoError(t, err, "the server made the directory")
			assert.True(t, info.IsDir)
		}},
		{"Move", "Rename", func(t *testing.T, f *fileSystem) error { return f.Move(t.Context(), "/file.txt", "/moved.txt") }, func(t *testing.T, f *fileSystem) {
			_, err := f.Stat("/moved.txt")
			assert.NoError(t, err, "the server moved the file")
		}},
		{"MoveOverwrite", "PosixRename", func(t *testing.T, f *fileSystem) error { return f.Move(t.Context(), "/file.txt", "/other.txt") }, func(t *testing.T, f *fileSystem) {
			_, err := f.Stat("/file.txt")
			assert.ErrorIs(t, err, os.ErrNotExist, "the server moved the file")
		}},
		{"CreateSymbolicLink", "Symlink", func(t *testing.T, f *fileSystem) error { return f.CreateSymbolicLink("/file.txt", "/link") }, func(t *testing.T, f *fileSystem) {
			_, err := f.Lstat("/link")
			assert.NoError(t, err, "the server made the link")
		}},
		{"OpenWriter", "Put", func(t *testing.T, f *fileSystem) error {
			w, err := f.OpenWriter("/new.txt", 0)
			if err != nil {
				return err
			}
			return w.Close()
		}, func(t *testing.T, f *fileSystem) {
			_, err := f.Stat("/new.txt")
			assert.NoError(t, err, "the server created the file")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var armed atomic.Bool
			s := startTestServer(t, testServerConfig{after: func(s *testServer, r *sftp.Request) {
				if r.Method == test.method && armed.CompareAndSwap(true, false) {
					s.killConns()
				}
			}})
			f := s.dial(t, nil)
			writeTestFile(t, f, "/file.txt", "content")
			writeTestFile(t, f, "/other.txt", "other")
			require.NoError(t, f.MakeDir("/dir", 0))
			armed.Store(true)

			err := test.op(t, f)
			assert.False(t, armed.Load(), "the hook did not kill the connection")
			assert.True(t, isConnectionError(err), "the connection error expected, got %v", err)
			test.check(t, f)
		})
	}
}

// TestNonIdempotentOperationRetriedBeforeRequest loses the connection before
// Remove sent its request: once known before the operation started and once
// while it checked the file. Both times nothing was removed yet, so the
// operation runs with a new connection.
func TestNonIdempotentOperationRetriedBeforeRequest(t *testing.T) {
	s := startTestServer(t, testServerConfig{after: killOnce("Lstat", "/b.txt")})
	f := s.dial(t, nil)
	writeTestFile(t, f, "/a.txt", "a")
	writeTestFile(t, f, "/b.txt", "b")

	first := f.currentConn()
	s.killConns()
	waitFor(t, first.isLost, "the client to notice the lost connection")
	require.NoError(t, f.Remove("/a.txt"), "Remove after a lost connection")

	// The hook kills the connection after the Lstat of Remove
	require.NoError(t, f.Remove("/b.txt"), "Remove with the connection lost before its request")
	assert.Equal(t, int64(3), s.accepted.Load(), "reconnected twice")
	for _, name := range []string{"/a.txt", "/b.txt"} {
		_, err := f.Stat(name)
		assert.ErrorIs(t, err, os.ErrNotExist, "%s removed", name)
	}
}

// TestTypedErrors checks the mapping of SFTP errors to the typed errors of
// package fs with the in-memory server, the conformance suite checks it
// with OpenSSH and the os based server.
func TestTypedErrors(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	nop := func(*fs.FileInfo) error { return nil }
	require.NoError(t, f.MakeAllDirs("/dir/sub", 0))
	writeTestFile(t, f, "/file.txt", "content")

	assert.ErrorIs(t, f.Remove("/dir"), fs.ErrDirectoryNotEmpty{})
	assert.ErrorIs(t, f.Remove("/missing"), os.ErrNotExist)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), os.ErrExist)
	assert.ErrorIs(t, f.MakeAllDirs("/file.txt", 0), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.ListDir(t.Context(), "/file.txt", nil, nop), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.ListDir(t.Context(), "/missing", nil, nop), os.ErrNotExist)
	assert.ErrorIs(t, f.ListDirRecursive(t.Context(), "/file.txt", nil, nop), fs.ErrIsNotDirectory{})
	assert.ErrorIs(t, f.ListDirRecursive(t.Context(), "/missing", nil, nop), os.ErrNotExist)
	assert.ErrorIs(t, f.Move(t.Context(), "/missing", "/x"), os.ErrNotExist)
	assert.ErrorIs(t, f.Move(t.Context(), "/dir", "/dir/sub/x"), os.ErrInvalid)
	assert.ErrorIs(t, f.Move(t.Context(), "/file.txt", "/dir"), os.ErrExist)
	assert.ErrorIs(t, f.Move(t.Context(), "/dir", "/file.txt"), fs.ErrIsNotDirectory{})
	assert.NoError(t, f.Move(t.Context(), "/dir/", "/dir"), "Move onto itself is a no-op")
	assert.ErrorIs(t, f.Truncate(t.Context(), "/missing", 0), os.ErrNotExist)
	assert.ErrorIs(t, f.SetPermissions("/missing", 0o600), os.ErrNotExist)
	_, err := f.ReadSymbolicLink("/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = f.Lstat("/missing")
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, f.CreateSymbolicLink("/file.txt", "/link"))
	assert.ErrorIs(t, f.CreateSymbolicLink("/file.txt", "/link"), os.ErrExist)
	assert.NoError(t, f.Touch("/file.txt", 0))
	assert.NoError(t, f.Touch("/touched.txt", 0o600))
	assert.NoError(t, f.RemoveAll(t.Context(), "/dir"))
	assert.NoError(t, f.RemoveAll(t.Context(), "/dir"), "RemoveAll of a missing path")
}

// dirNames returns the sorted names of the entries of a local directory.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

// writeTestFile writes a file with content to f.
func writeTestFile(t *testing.T, f *fileSystem, filePath, content string) {
	t.Helper()
	w, err := f.OpenWriter(filePath, 0)
	require.NoError(t, err)
	_, err = w.Write([]byte(content))
	require.NoError(t, errors.Join(err, w.Close()))
}

// TestBackslashInNames checks that paths with a backslash are rejected
// unless Options.AllowBackslashInNames is set, because an SFTP server
// on Windows takes `..\x` for a name in the parent directory.
func TestBackslashInNames(t *testing.T) {
	s := startTestServer(t, testServerConfig{})
	f := s.dial(t, nil)
	ctx := t.Context()
	require.NoError(t, f.MakeDir("/dir", 0))
	listRoot := func() (names []string) {
		require.NoError(t, f.ListDirRecursive(ctx, "/", nil, func(info *fs.FileInfo) error {
			names = append(names, info.File.Path())
			return nil
		}))
		return names
	}
	before := listRoot()

	noCallback := func(*fs.FileInfo) error { return nil }
	for _, name := range []string{`/dir/..\victim.txt`, `/dir\x`} {
		for op, call := range map[string]func() error{
			"Stat":                      func() error { _, err := f.Stat(name); return err },
			"Lstat":                     func() error { _, err := f.Lstat(name); return err },
			"ListDir":                   func() error { return f.ListDir(ctx, name, nil, noCallback) },
			"ListDirRecursive":          func() error { return f.ListDirRecursive(ctx, name, nil, noCallback) },
			"OpenReader":                func() error { _, err := f.OpenReader(name); return err },
			"OpenWriter":                func() error { _, err := f.OpenWriter(name, 0); return err },
			"OpenAppendWriter":          func() error { _, err := f.OpenAppendWriter(name, 0); return err },
			"OpenReadWriter":            func() error { _, err := f.OpenReadWriter(name, 0); return err },
			"Truncate":                  func() error { return f.Truncate(ctx, name, 0) },
			"Touch":                     func() error { return f.Touch(name, 0) },
			"MakeDir":                   func() error { return f.MakeDir(name, 0) },
			"MakeAllDirs":               func() error { return f.MakeAllDirs(name, 0) },
			"SetPermissions":            func() error { return f.SetPermissions(name, fs.UserReadWrite) },
			"Move from":                 func() error { return f.Move(ctx, name, "/moved.txt") },
			"Move to":                   func() error { return f.Move(ctx, "/dir", name) },
			"Remove":                    func() error { return f.Remove(name) },
			"RemoveAll":                 func() error { return f.RemoveAll(ctx, name) },
			"CreateSymbolicLink":        func() error { return f.CreateSymbolicLink("/dir", name) },
			"CreateSymbolicLink target": func() error { return f.CreateSymbolicLink(name, "/link") },
			"ReadSymbolicLink":          func() error { _, err := f.ReadSymbolicLink(name); return err },
		} {
			assert.ErrorIs(t, call(), os.ErrInvalid, "%s(%q)", op, name)
		}
	}
	assert.Equal(t, before, listRoot(), "nothing may be created on the server")

	if runtime.GOOS == "windows" {
		// The in-process server runs on Windows here, where pkg/sftp takes
		// a backslash for a separator like the servers on Windows that the
		// default rule protects against; the opt-out is for servers on Unix
		return
	}
	// Servers on Unix store a backslash as part of a name
	opts := s.testOptions()
	opts.AllowBackslashInNames = true
	allowFS := s.dial(t, opts)
	w, err := allowFS.OpenWriter(`/dir/a\b.txt`, 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("unix name"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	var names []string
	require.NoError(t, allowFS.ListDir(ctx, "/dir", nil, func(info *fs.FileInfo) error {
		names = append(names, info.Name)
		return nil
	}))
	assert.Equal(t, []string{`a\b.txt`}, names)
	r, err := allowFS.OpenReader(`/dir/a\b.txt`)
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, errors.Join(err, r.Close()))
	assert.Equal(t, "unix name", string(data))
}

// TestValidateName verifies the names that a copy or move into the file
// system rejects with ValidateName before it writes anything.
func TestValidateName(t *testing.T) {
	f := &fileSystem{}
	for _, name := range []string{"a.txt", "Größe: what?.txt", "trailing."} {
		assert.NoError(t, f.ValidateName(name), "%q", name)
	}
	for _, name := range []string{`a\b.txt`, "a\x00b", "\xff.txt"} {
		assert.ErrorIs(t, f.ValidateName(name), os.ErrInvalid, "%q", name)
	}
	f.opts.AllowBackslashInNames = true
	assert.NoError(t, f.ValidateName(`a\b.txt`), "Options.AllowBackslashInNames")
	assert.ErrorIs(t, f.ValidateName("a\x00b"), os.ErrInvalid, "a NUL character, also with Options.AllowBackslashInNames")
}
