package smbfs

import (
	"context"
	"errors"
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

	testGuestShare = "public"
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
		// The shares and the session databases of smbd are in memory,
		// so the tests don't depend on the free space of the Docker disk:
		// on a full disk smbd starts but fails every open
		"--tmpfs", "/share",
		"--tmpfs", "/public",
		"--tmpfs", "/var/cache/samba",
		"dperson/samba",
		"-p",
		"-u", fmt.Sprintf("%s;%s", testUsername, testPassword),
		"-s", fmt.Sprintf("%s;/share;no;no;no;%s", testShare, testUsername),
		// A share with guest access for anonymous sessions
		"-s", fmt.Sprintf("%s;/public;yes;no;yes", testGuestShare),
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
		Name:    "SMB",
		Prefix:  expectedPrefix,
		TestDir: "/conformance",
		// SMB only stores a read-only attribute
		PermissionMask: fs.UserWrite,
	})
}

// Test_fileSystem_Identity checks the prefix, ID, Name and String,
// which never contain the password.
func Test_fileSystem_Identity(t *testing.T) {
	f := &fileSystem{PathHelper: fsimpl.PathHelper{URIPrefix: "smb://alice@nas/share", Rooted: true}}
	assert.Equal(t, "alice@nas/share", f.ID())
	assert.Equal(t, "SMB", f.Name())
	assert.Equal(t, "SMB smb://alice@nas/share", f.String())
	assert.Equal(t, fs.File("smb://alice@nas/share/"), f.RootDir())
	readable, writable := f.ReadableWritable()
	assert.True(t, readable)
	assert.True(t, writable)
}

// TestDial_Credentials verifies the credentials of the address and the
// options: Options.Password is used with a username from the address
// (it logged in with an empty password), a password of the address works,
// and a wrong password is an error without the password.
func TestDial_Credentials(t *testing.T) {
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	ctx := t.Context()
	hostShare := fmt.Sprintf("127.0.0.1:%s/%s", testSMBPort, testShare)

	smbFS, err := Dial(ctx, fmt.Sprintf("smb://%s@%s", testUsername, hostShare), &Options{Password: testPassword})
	require.NoError(t, err, "Options.Password with the username of the address")
	assert.Equal(t, fmt.Sprintf("smb://%s@%s", testUsername, hostShare), smbFS.Prefix())
	require.NoError(t, smbFS.Close())

	smbFS, err = Dial(ctx, fmt.Sprintf("%s:%s@%s", testUsername, testPassword, hostShare), nil)
	require.NoError(t, err, "password of the address")
	assert.Equal(t, fmt.Sprintf("smb://%s@%s", testUsername, hostShare), smbFS.Prefix(), "no password in the prefix")
	require.NoError(t, smbFS.Close())

	_, err = Dial(ctx, fmt.Sprintf("smb://%s:S3cretWrong@%s", testUsername, hostShare), nil)
	require.Error(t, err, "wrong password")
	assert.NotContains(t, err.Error(), "S3cretWrong")
}

// TestDial_Anonymous verifies that a share with guest access
// can be mounted without credentials.
func TestDial_Anonymous(t *testing.T) {
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	address := fmt.Sprintf("smb://127.0.0.1:%s/%s", testSMBPort, testGuestShare)
	smbFS, err := Dial(t.Context(), address, nil)
	require.NoError(t, err, "anonymous session")
	defer smbFS.Close()
	assert.Equal(t, address, smbFS.Prefix())
	_, err = smbFS.Stat("/")
	require.NoError(t, err)
	err = smbFS.ListDir(t.Context(), "/", nil, func(*fs.FileInfo) error { return nil })
	require.NoError(t, err)
}

// TestEnsureRegistered verifies that EnsureRegistered shares one
// registered file system per prefix with reference counting.
func TestEnsureRegistered(t *testing.T) {
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	ctx := t.Context()
	address := fmt.Sprintf("smb://%s:%s@127.0.0.1:%s/%s", testUsername, testPassword, testSMBPort, testShare)
	prefix := fmt.Sprintf("smb://%s@127.0.0.1:%s/%s", testUsername, testSMBPort, testShare)

	free1, err := EnsureRegistered(ctx, address, nil)
	require.NoError(t, err)
	free2, err := EnsureRegistered(ctx, address, nil)
	require.NoError(t, err)
	registered := fs.GetFileSystemByPrefixOrNil(prefix)
	require.NotNil(t, registered)

	name := fmt.Sprintf("/ensure-%d.txt", time.Now().UnixNano())
	file := fs.File(prefix + name)
	require.NoError(t, file.WriteAllString(ctx, "registered"))
	content, err := file.ReadAllString(ctx)
	require.NoError(t, err)
	assert.Equal(t, "registered", content)
	require.NoError(t, file.Remove())

	require.NoError(t, free1())
	require.NoError(t, free1(), "free is idempotent")
	assert.True(t, fs.IsRegistered(registered), "registered while a reference remains")
	require.NoError(t, free2())
	assert.False(t, fs.IsRegistered(registered), "unregistered after the last free")
	_, err = registered.Stat("/")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	free, err := EnsureRegistered(ctx, "smb://alice:S3cret@host", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "S3cret")
	require.NotNil(t, free)
	require.NoError(t, free())
}

// TestDialAndRegister verifies the registration and that a second
// registration of the prefix fails without closing the first.
func TestDialAndRegister(t *testing.T) {
	if !dockerSMBAvailable {
		t.Skip("Docker Samba server not available")
	}
	ctx := t.Context()
	opts := &Options{Username: testUsername, Password: testPassword}
	require.NoError(t, DialAndRegister(ctx, testSMBAddress, opts))
	prefix := fmt.Sprintf("smb://%s@127.0.0.1:%s/%s", testUsername, testSMBPort, testShare)
	registered := fs.GetFileSystemByPrefixOrNil(prefix)
	require.NotNil(t, registered)
	defer registered.Close()

	err := DialAndRegister(ctx, testSMBAddress, opts)
	require.ErrorIs(t, err, fs.ErrAlreadyRegistered)
	_, err = registered.Stat("/")
	assert.NoError(t, err, "the registered file system must still work")
	require.Error(t, DialAndRegister(ctx, "smb://host", nil))
}

// TestSymbolicLink verifies that creating a symbolic link on a server
// without symbolic link support is an fs.ErrUnsupported and that
// reading a missing link is an os.ErrNotExist.
func TestSymbolicLink(t *testing.T) {
	f, _ := dialTestFS(t)
	err := f.CreateSymbolicLink("/target.txt", fmt.Sprintf("/link-%d", time.Now().UnixNano()))
	if err == nil {
		t.Skip("the server supports symbolic links")
	}
	assert.ErrorIs(t, err, errors.ErrUnsupported)
	_, err = f.ReadSymbolicLink("/missing-link")
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.ErrorIs(t, f.CreateSymbolicLink("", "/link"), fs.ErrEmptyPath)
}

// TestRemove_Root verifies that the root directory of the share is never removed.
func TestRemove_Root(t *testing.T) {
	f, _ := dialTestFS(t)
	assert.Error(t, f.Remove("/"))
	assert.Error(t, f.RemoveAll(t.Context(), "/"))
	assert.ErrorIs(t, f.MakeDir("/", 0), os.ErrExist)
	assert.NoError(t, f.MakeAllDirs("/", 0))
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
	assert.ErrorIs(t, f.Truncate(ctx, "/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Touch("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeAllDirs("/dir/sub", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.SetPermissions("/file", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(ctx, "/a", "/b"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Move(ctx, "/a", "/a"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Remove("/file"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
	// The root directory is never removed, but a closed file system
	// reports that it is closed first, like for every other path
	assert.ErrorIs(t, f.Remove("/"), fs.ErrFileSystemClosed, "Remove of the root directory")
	assert.ErrorIs(t, f.RemoveAll(ctx, "/"), fs.ErrFileSystemClosed, "RemoveAll of the root directory")
	assert.ErrorIs(t, f.CreateSymbolicLink("/a", "/link"), fs.ErrFileSystemClosed)
	_, err = f.ReadSymbolicLink("/link")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.Lstat("/file")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)

	// Empty paths are rejected before anything else
	_, err = f.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Move(ctx, "", "/b"), fs.ErrEmptyPath)

	// Close on an already-closed file system is a safe no-op
	// that must not touch the released connection.
	assert.NoError(t, f.Close())
}

// Test_sharePath checks the share relative paths and that an empty path
// and a path with a backslash are rejected.
func Test_sharePath(t *testing.T) {
	for filePath, want := range map[string]string{
		"/":        "",
		".":        "",
		"/a":       "a",
		"a/b/":     "a/b",
		"//a//b":   "a/b",
		"/../a":    "a",
		"/a/../b":  "b",
		"/a b/c.d": "a b/c.d",
	} {
		p, err := sharePath(filePath)
		require.NoError(t, err, filePath)
		assert.Equal(t, want, p, filePath)
	}

	_, err := sharePath("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	for _, filePath := range []string{`\`, `/..\outside.txt`, `/a\b`, `C:\x`, `dir\`} {
		_, err := sharePath(filePath)
		assert.ErrorIs(t, err, os.ErrInvalid, filePath)
		assert.ErrorIs(t, checkPath(filePath), os.ErrInvalid, filePath)
	}
}

// Test_fileSystem_BackslashPaths verifies that every method rejects a path
// with a backslash, also the destination of Move and the target of a
// symbolic link, before it sends a request. The file system is closed, so
// a method that doesn't check the path returns fs.ErrFileSystemClosed.
func Test_fileSystem_BackslashPaths(t *testing.T) {
	ctx := t.Context()
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: "smb://user@example.com/share", Rooted: true},
	}
	f.closed.Store(true)
	const bad = `/dir/..\..\outside.txt`

	for _, check := range []struct {
		method string
		call   func() error
	}{
		{"Stat", func() error { _, err := f.Stat(bad); return err }},
		{"Lstat", func() error { _, err := f.Lstat(bad); return err }},
		{"ListDir", func() error { return f.ListDir(ctx, bad, nil, func(*fs.FileInfo) error { return nil }) }},
		{"OpenReader", func() error { _, err := f.OpenReader(bad); return err }},
		{"OpenWriter", func() error { _, err := f.OpenWriter(bad, 0); return err }},
		{"OpenAppendWriter", func() error { _, err := f.OpenAppendWriter(bad, 0); return err }},
		{"OpenReadWriter", func() error { _, err := f.OpenReadWriter(bad, 0); return err }},
		{"ReadAll", func() error { _, err := f.ReadAll(ctx, bad); return err }},
		{"WriteAll", func() error { return f.WriteAll(ctx, bad, []byte("x"), 0) }},
		{"Truncate", func() error { return f.Truncate(ctx, bad, 0) }},
		{"Touch", func() error { return f.Touch(bad, 0) }},
		{"MakeDir", func() error { return f.MakeDir(bad, 0) }},
		{"MakeAllDirs", func() error { return f.MakeAllDirs(bad, 0) }},
		{"SetPermissions", func() error { return f.SetPermissions(bad, 0) }},
		{"Move source", func() error { return f.Move(ctx, bad, "/b") }},
		{"Move destination", func() error { return f.Move(ctx, "/a", bad) }},
		{"Remove", func() error { return f.Remove(bad) }},
		{"RemoveAll", func() error { return f.RemoveAll(ctx, bad) }},
		{"CreateSymbolicLink target", func() error { return f.CreateSymbolicLink(`..\..\outside.txt`, "/link") }},
		{"CreateSymbolicLink link", func() error { return f.CreateSymbolicLink("/a", bad) }},
		{"ReadSymbolicLink", func() error { _, err := f.ReadSymbolicLink(bad); return err }},
	} {
		assert.ErrorIs(t, check.call(), os.ErrInvalid, check.method)
	}
}

// TestBackslashPaths_Escape verifies on the Samba server that a name with a
// backslash can't reach a file outside of a SubFileSystem or outside of the
// destination of CopyRecursive. smbfs has the separator "/", so `..\x` is a
// single name for both, but Samba splits names at backslashes and resolves
// "..": the view read and wrote the files next to its root directory, and
// CopyRecursive of an entry named like that wrote outside of dest.
func TestBackslashPaths_Escape(t *testing.T) {
	f, _ := dialTestFS(t)
	ctx := t.Context()
	dir := fmt.Sprintf("/backslash-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = f.RemoveAll(context.Background(), dir) })
	require.NoError(t, f.MakeAllDirs(dir+"/root", 0))
	require.NoError(t, f.WriteAll(ctx, dir+"/outside.txt", []byte("outside"), 0))
	require.NoError(t, f.WriteAll(ctx, dir+"/root/inside.txt", []byte("inside"), 0))

	view, err := fs.NewSubFileSystem(f, dir+"/root", "")
	require.NoError(t, err)
	data, err := view.ReadAll(ctx, `/..\outside.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "the view read %q outside of its root", data)
	err = view.WriteAll(ctx, `/..\written-outside.txt`, []byte("x"), 0)
	assert.ErrorIs(t, err, os.ErrInvalid, "WriteAll outside of the view")
	err = view.Move(ctx, "/inside.txt", `/..\moved-outside.txt`)
	assert.ErrorIs(t, err, os.ErrInvalid, "Move outside of the view")

	// CopyRecursive uses the File API, which needs registered file systems
	require.NoError(t, fs.Register(f))
	t.Cleanup(func() { fs.Unregister(f) })
	// An entry with that name, like from an archive
	mem, err := fs.NewMemFileSystem(nil, fs.NewMemFile(`/src/..\copied-outside.txt`, []byte("copied")))
	require.NoError(t, err)
	require.NoError(t, fs.Register(mem))
	t.Cleanup(func() { _ = mem.Close() })
	err = fs.CopyRecursive(ctx, mem.RootDir().Join("src"), f.file(dir+"/root/dest"))
	assert.ErrorIs(t, err, os.ErrInvalid, "CopyRecursive outside of dest")

	for _, outside := range []string{
		dir + "/written-outside.txt",
		dir + "/moved-outside.txt",
		dir + "/root/copied-outside.txt",
	} {
		_, err := f.Stat(outside)
		assert.ErrorIs(t, err, os.ErrNotExist, "%s must not have been written", outside)
	}
}
