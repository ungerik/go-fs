package azureblobfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
	"github.com/ungerik/go-fs/internal/dockertest"
)

const testContainer = "testcontainer"

var (
	dockerAzuriteAvailable bool
	// testContainerName is unique for every test run and testBlobPort
	// is the host port that Docker assigned, so that test runs in other
	// worktrees don't stop the container or take the port.
	testContainerName string
	testBlobPort      string
	// testConnectionString connects to the well-known Azurite
	// development storage account on testBlobPort
	testConnectionString string
)

// azuriteConnectionString returns the connection string of the
// well-known Azurite development storage account at port.
func azuriteConnectionString(port string) string {
	return "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;" +
		"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
		"BlobEndpoint=http://127.0.0.1:" + port + "/devstoreaccount1;"
}

// TestMain starts an Azurite container (the Azure Storage emulator).
// Tests are skipped when Docker is not available and fail when it is
// available but the emulator can't be started.
func TestMain(m *testing.M) {
	if !dockertest.Available() {
		log.Println("Docker not available, skipping Docker-based Azure Blob tests")
		os.Exit(m.Run())
	}
	// TestMain runs before any *testing.T exists, so t.Context() is not
	// available — context.Background() is the right choice here.
	ctx := context.Background()

	testContainerName = dockertest.ContainerName("azureblobfs-test")

	// Start Azurite on a host port that Docker picks
	log.Println("Starting Docker Azurite test server...")
	runCmd := exec.CommandContext(ctx, "docker", "run",
		"-d",
		"--name", testContainerName,
		"-p", "127.0.0.1::10000",
		"mcr.microsoft.com/azure-storage/azurite",
		"azurite-blob", "--blobHost", "0.0.0.0", "--skipApiVersionCheck", "--loose",
	)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Docker container: %v\nOutput: %s", err, output)
		removeContainer(testContainerName)
		dockertest.SetupFailed("Azurite")
		os.Exit(m.Run())
	}
	testBlobPort, err = dockerHostPort(ctx, testContainerName, "10000/tcp")
	if err != nil {
		log.Printf("Failed to get the host port of the Azurite container: %v", err)
		removeContainer(testContainerName)
		dockertest.SetupFailed("Azurite")
		os.Exit(m.Run())
	}
	testConnectionString = azuriteConnectionString(testBlobPort)

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
		removeContainer(testContainerName)
		dockertest.SetupFailed("Azurite")
		os.Exit(m.Run())
	}

	exitCode := m.Run()

	log.Println("Stopping and removing Docker Azurite test server...")
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

func isContainerExists(err error) bool {
	return bloberror.HasCode(err, bloberror.ContainerAlreadyExists)
}

// testClient returns a container client for the test container
// with the shared key of the Azurite development account.
func testClient(t *testing.T) *container.Client {
	t.Helper()
	if !dockerAzuriteAvailable {
		t.Skip("Docker Azurite server not available")
	}
	client, err := container.NewClientFromConnectionString(testConnectionString, testContainer, nil)
	require.NoError(t, err)
	return client
}

// newTestFileSystem returns a registered file system for the test
// container that is closed at the end of the test.
func newTestFileSystem(t *testing.T, opts *Options) *fileSystem {
	t.Helper()
	if !dockerAzuriteAvailable {
		t.Skip("Docker Azurite server not available")
	}
	blobFS, err := NewFromConnectionString(t.Context(), testConnectionString, testContainer, opts)
	require.NoError(t, err, "NewFromConnectionString")
	require.NoError(t, fs.Register(blobFS))
	t.Cleanup(func() { _ = blobFS.Close() })
	return blobFS.(*fileSystem)
}

// sasClient returns a client for the test container
// that authenticates with a container SAS of perms.
func sasClient(t *testing.T, perms sas.ContainerPermissions) *container.Client {
	t.Helper()
	sasURL, err := testClient(t).GetSASURL(perms, time.Now().Add(time.Hour), nil)
	require.NoError(t, err)
	client, err := container.NewClientWithNoCredential(sasURL, nil)
	require.NoError(t, err)
	return client
}

// noRetries makes a client fail on the first error.
var noRetries = &container.ClientOptions{
	ClientOptions: azcore.ClientOptions{Retry: policy.RetryOptions{MaxRetries: -1}},
}

func Test_fileSystem(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)

	expectedPrefix := fmt.Sprintf("azblob://127.0.0.1:%s/devstoreaccount1/%s", testBlobPort, testContainer)
	require.Equal(t, expectedPrefix, blobFS.Prefix())

	fstest.RunConformance(t, blobFS, fstest.Config{
		Name:    "Azure Blob Storage",
		Prefix:  expectedPrefix,
		TestDir: "/conformance",
	})
}

// TestRangedRead verifies seeking reads and the native server-side copy.
func TestRangedRead(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	ctx := t.Context()

	file := blobFS.RootDir().Join("ranged", "data.txt")
	require.NoError(t, file.WriteAllString(ctx, "0123456789"))
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/ranged") })

	r, err := file.OpenReadSeeker()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	_, isRangeReader := r.(*fsimpl.RangeReader)
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

// TestNew_Errors verifies that a missing client is rejected instead of
// producing a file system that panics on first use.
func TestNew_Errors(t *testing.T) {
	_, err := New(t.Context(), nil, nil)
	assert.Error(t, err, "nil container client")
	err = NewAndRegister(t.Context(), nil, nil)
	assert.Error(t, err, "nil container client must not be registered")
	_, err = NewFromConnectionString(t.Context(), "not-a-connection-string", testContainer, nil)
	assert.Error(t, err, "invalid connection string")
}

// TestNew checks the metadata of a file system that New creates
// with Options.SkipContainerCheck, which sends no request.
func TestNew(t *testing.T) {
	client, err := container.NewClientWithNoCredential("https://account.blob.core.windows.net/assets?sv=2023-01-01&sp=rw&sig=SECRET", noRetries)
	require.NoError(t, err)
	f, err := New(t.Context(), client, &Options{SkipContainerCheck: true})
	require.NoError(t, err, "SkipContainerCheck must not send a request")
	assert.Equal(t, "azblob://account.blob.core.windows.net/assets", f.Prefix(), "the SAS is not part of the prefix")
	assert.Equal(t, "account.blob.core.windows.net/assets", f.ID())
	assert.Equal(t, "Azure Blob Storage", f.Name())
	assert.Equal(t, "Azure Blob Storage azblob://account.blob.core.windows.net/assets", f.String())
	assert.Equal(t, fs.File("azblob://account.blob.core.windows.net/assets/"), f.RootDir())
	assert.False(t, fs.IsRegistered(f), "New must not register")
	_, writable := f.ReadableWritable()
	assert.True(t, writable)

	readOnly, err := New(t.Context(), client, &Options{ReadOnly: true, SkipContainerCheck: true})
	require.NoError(t, err)
	_, writable = readOnly.ReadableWritable()
	assert.False(t, writable)
}

// TestNewAndRegister checks that a second file system for the same
// container doesn't take over the prefix of the registered one.
func TestNewAndRegister(t *testing.T) {
	client, err := container.NewClientWithNoCredential("https://account.blob.core.windows.net/register-test", noRetries)
	require.NoError(t, err)
	opts := &Options{SkipContainerCheck: true}
	require.NoError(t, NewAndRegister(t.Context(), client, opts))
	registered := fs.GetFileSystemByPrefixOrNil("azblob://account.blob.core.windows.net/register-test")
	require.NotNil(t, registered)
	t.Cleanup(func() { _ = registered.Close() })
	assert.ErrorIs(t, NewAndRegister(t.Context(), client, opts), fs.ErrAlreadyRegistered)
}

// TestSASNotInErrors checks that the signature of a SAS token in the
// container URL doesn't appear in error messages: New used to print the
// container URL, and the transport errors of net/http contain the URL
// of the request.
func TestSASNotInErrors(t *testing.T) {
	const secret = "TOPSECRETSIG"
	client, err := container.NewClientWithNoCredential("http://127.0.0.1:1/devstoreaccount1/c?sv=2023-01-01&sp=rl&sig="+secret, noRetries)
	require.NoError(t, err)
	ctx := t.Context()

	_, err = New(ctx, client, nil)
	require.Error(t, err, "nobody listens on port 1")
	assert.NotContains(t, err.Error(), secret, "New")
	assert.Contains(t, err.Error(), "127.0.0.1:1/devstoreaccount1/c", "the error names the container")

	f, err := New(ctx, client, &Options{SkipContainerCheck: true})
	require.NoError(t, err)
	_, err = f.Stat("/x.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "Stat")
	err = f.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "ListDir")
	_, err = f.(fs.ReadAllFileSystem).ReadAll(ctx, "/x.txt")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "ReadAll")
	err = f.(fs.WriteAllFileSystem).WriteAll(ctx, "/x.txt", []byte("x"), 0)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "WriteAll")
	err = f.(fs.TouchFileSystem).Touch("/x.txt", 0)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "Touch")

	var opErr *net.OpError
	assert.True(t, errors.As(err, &opErr), "the redacted error must unwrap to the transport error")
}

// TestRedactErr checks the redaction of URL queries and SAS signatures.
func TestRedactErr(t *testing.T) {
	assert.NoError(t, redactErr(nil))
	plain := errors.New("no URL here")
	assert.Same(t, plain, redactErr(plain), "errors without secrets are returned unchanged")

	for msg, want := range map[string]string{
		`Get "https://a.blob.core.windows.net/c/x.txt?sv=1&sig=abc%2F": dial tcp: refused`: `Get "https://a.blob.core.windows.net/c/x.txt": dial tcp: refused`,
		`PUT http://127.0.0.1:1/acc/c?comp=list&sig=abc and more`:                          `PUT http://127.0.0.1:1/acc/c and more`,
		`signature sig=abc&se=2026 rejected`:                                                `signature sig=REDACTED&se=2026 rejected`,
		`two https://h/a?x=1 and https://h/b?sig=2#frag`:                                    `two https://h/a and https://h/b#frag`,
	} {
		err := redactErr(fmt.Errorf("wrapped: %w", errors.New(msg)))
		assert.Equal(t, "wrapped: "+want, err.Error(), "redactErr(%q)", msg)
	}
	err := redactErr(fmt.Errorf("x?sig=1: %w", context.Canceled))
	assert.ErrorIs(t, err, context.Canceled, "redacted errors unwrap")
}

// TestContainerSAS checks a file system with a container SAS: New used to
// get the container properties, which a container SAS doesn't allow,
// it now lists one blob. A SAS without the list permission needs
// SkipContainerCheck, and writes then skip the directory check.
func TestContainerSAS(t *testing.T) {
	ctx := t.Context()
	all := sas.ContainerPermissions{Read: true, Add: true, Create: true, Write: true, Delete: true, List: true}
	f, err := New(ctx, sasClient(t, all), nil)
	require.NoError(t, err, "New with a container SAS")
	blobFS := f.(*fileSystem)
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/sas") })
	require.NoError(t, blobFS.WriteAll(ctx, "/sas/file.txt", []byte("via SAS"), 0))
	data, err := blobFS.ReadAll(ctx, "/sas/file.txt")
	require.NoError(t, err)
	assert.Equal(t, "via SAS", string(data))

	writeOnly := sasClient(t, sas.ContainerPermissions{Create: true, Write: true})
	_, err = New(ctx, writeOnly, nil)
	assert.Error(t, err, "the container check needs the list permission")
	f, err = New(ctx, writeOnly, &Options{SkipContainerCheck: true})
	require.NoError(t, err)
	require.NoError(t, f.(fs.WriteAllFileSystem).WriteAll(ctx, "/sas/write-only.txt", []byte("upload"), 0), "WriteAll without the list permission")
	w, err := f.(fs.WriteFileSystem).OpenWriter("/sas/write-only-writer.txt", 0)
	require.NoError(t, err, "OpenWriter without the list permission")
	_, err = io.WriteString(w, "upload")
	require.NoError(t, err)
	require.NoError(t, w.Close())
	data, err = blobFS.ReadAll(ctx, "/sas/write-only-writer.txt")
	require.NoError(t, err)
	assert.Equal(t, "upload", string(data))
}

// TestTouchKeepsMetadata checks that Touch keeps the user metadata
// and the content headers of a blob. It used to set empty metadata,
// which deleted all of them. Azurite stores metadata names in lower case,
// so the names are lower case here; Azure keeps their case, which Touch
// preserves by reading them from a listing instead of GetProperties.
func TestTouchKeepsMetadata(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/touch") })

	blobClient := client.NewBlockBlobClient("touch/meta.html")
	_, err := blobClient.UploadBuffer(ctx, []byte("<p>"), nil)
	require.NoError(t, err)
	_, err = blobClient.SetMetadata(ctx, map[string]*string{"owner": new("erik"), "project": new("kept")}, nil)
	require.NoError(t, err)
	_, err = blobClient.SetHTTPHeaders(ctx, blob.HTTPHeaders{BlobContentType: new("text/html")}, nil)
	require.NoError(t, err)
	before, err := blobFS.Stat("/touch/meta.html")
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond) // Last-Modified has a resolution of seconds
	require.NoError(t, blobFS.Touch("/touch/meta.html", 0))

	item, err := blobFS.blobItem(ctx, "touch/meta.html")
	require.NoError(t, err)
	require.NotNil(t, item)
	metadata := map[string]string{}
	for k, v := range item.Metadata {
		metadata[k] = deref(v)
	}
	assert.Equal(t, map[string]string{"owner": "erik", "project": "kept"}, metadata, "metadata")
	assert.Equal(t, "text/html", deref(item.Properties.ContentType), "Content-Type")
	after, err := blobFS.Stat("/touch/meta.html")
	require.NoError(t, err)
	assert.True(t, after.Modified.After(before.Modified), "Touch must update the modification time")
	data, err := blobFS.ReadAll(ctx, "/touch/meta.html")
	require.NoError(t, err)
	assert.Equal(t, "<p>", string(data), "Touch must not change the content")

	// Touch of a missing blob creates an empty one, of a directory does nothing
	require.NoError(t, blobFS.Touch("/touch/new.txt", 0))
	info, err := blobFS.Stat("/touch/new.txt")
	require.NoError(t, err)
	assert.Zero(t, info.Size)
	require.NoError(t, blobFS.Touch("/touch", 0))
	info, err = blobFS.Stat("/touch")
	require.NoError(t, err)
	assert.True(t, info.IsDir, "Touch of a directory must not create a blob")
	assert.NoError(t, blobFS.Touch("/", 0), "Touch of the root")
}

// TestListDirSkipsNamesWithoutPath checks that blobs with empty path
// elements are not listed: a blob "/x" made the root list itself as its
// own child and "d//x.txt" made d list itself, so recursive walkers looped.
func TestListDirSkipsNamesWithoutPath(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	t.Cleanup(func() {
		_ = blobFS.RemoveAll(context.Background(), "/no-path")
		_, _ = client.NewBlobClient("/no-path-root.txt").Delete(context.Background(), nil)
	})
	for _, name := range []string{"no-path/d//x.txt", "no-path/d/y.txt", "/no-path-root.txt"} {
		_, err := client.NewBlockBlobClient(name).UploadBuffer(ctx, []byte("x"), nil)
		require.NoError(t, err, "upload %q", name)
	}

	err := blobFS.ListDir(ctx, "/", nil, func(info *fs.FileInfo) error {
		assert.NotEqual(t, blobFS.RootDir(), info.File, "the root must not list itself")
		assert.NotEmpty(t, info.Name)
		return nil
	})
	require.NoError(t, err)

	var listed []string
	err = blobFS.ListDir(ctx, "/no-path/d", nil, func(info *fs.FileInfo) error {
		listed = append(listed, info.File.Path())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/no-path/d/y.txt"}, listed, "ListDir must not list the directory itself")

	var walked []string
	err = blobFS.RootDir().Join("no-path").ListDirRecursive(ctx, func(file fs.File) error {
		walked = append(walked, file.Path())
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/no-path/d/y.txt"}, walked)
}

// TestDirectoryErrors checks the error contract for directories on the
// native methods: writing or copying onto a directory must fail with
// fs.ErrIsDirectory and leave it intact, listing a missing directory
// must report os.ErrNotExist.
func TestDirectoryErrors(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/dir-errors") })
	_, err := client.NewBlockBlobClient("dir-errors/implicit/file.txt").UploadBuffer(ctx, []byte("x"), nil)
	require.NoError(t, err)
	require.NoError(t, blobFS.MakeDir("/dir-errors/marker", 0))
	require.NoError(t, blobFS.WriteAll(ctx, "/dir-errors/src.txt", []byte("src"), 0))

	for _, d := range []string{"/dir-errors/implicit", "/dir-errors/marker", "/"} {
		assert.ErrorIs(t, blobFS.WriteAll(ctx, d, []byte("x"), 0), fs.ErrIsDirectory{}, "WriteAll(%q)", d)
		_, err := blobFS.OpenWriter(d, 0)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "OpenWriter(%q)", d)
		assert.ErrorIs(t, blobFS.CopyFile(ctx, "/dir-errors/src.txt", d), fs.ErrIsDirectory{}, "CopyFile onto %q", d)
		assert.ErrorIs(t, blobFS.CopyFile(ctx, d, "/dir-errors/copy.txt"), fs.ErrIsDirectory{}, "CopyFile from %q", d)
		_, err = blobFS.ReadAll(ctx, d)
		assert.ErrorIs(t, err, fs.ErrIsDirectory{}, "ReadAll(%q)", d)
		info, err := blobFS.Stat(d)
		require.NoError(t, err)
		assert.True(t, info.IsDir, "%q must stay a directory", d)
	}
	assert.ErrorIs(t, blobFS.Remove("/dir-errors/implicit"), fs.ErrDirectoryNotEmpty{}, "Remove of a non-empty directory")
	assert.ErrorIs(t, blobFS.CopyFile(ctx, "/dir-errors/missing.txt", "/dir-errors/copy.txt"), os.ErrNotExist, "CopyFile of a missing blob")

	err = blobFS.ListDir(ctx, "/dir-errors/missing", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrNotExist, "ListDir of a missing directory")
	err = blobFS.ListDirRecursive(ctx, "/dir-errors/missing", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, os.ErrNotExist, "ListDirRecursive of a missing directory")
	err = blobFS.ListDir(ctx, "/dir-errors/src.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "ListDir of a blob")
	err = blobFS.ListDirRecursive(ctx, "/dir-errors/src.txt", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrIsNotDirectory{}, "ListDirRecursive of a blob")
	err = blobFS.ListDir(ctx, "/dir-errors/marker", nil, func(*fs.FileInfo) error { return errors.New("listed") })
	assert.NoError(t, err, "ListDir of an empty directory")
}

// TestOpenWriterStreaming checks that OpenWriter stages blocks while
// data is written and that Abort commits nothing.
func TestOpenWriterStreaming(t *testing.T) {
	const mib = 1 << 20
	blobFS := newTestFileSystem(t, nil)
	ctx := t.Context()
	t.Cleanup(func() { _ = testFSRemoveAll(blobFS, "/streaming") })

	data := make([]byte, 3*uploadBlockSize+mib)
	for i := range data {
		data[i] = byte(i % 251)
	}
	w, err := blobFS.OpenWriter("/streaming/large.bin", 0)
	require.NoError(t, err)
	_, isAborter := w.(fs.WriteAborter)
	require.True(t, isAborter, "the writer must implement fs.WriteAborter")
	for start := 0; start < len(data); start += 32 * 1024 {
		_, err = w.Write(data[start:min(start+32*1024, len(data))])
		require.NoError(t, err)
	}
	_, err = blobFS.Stat("/streaming/large.bin")
	assert.ErrorIs(t, err, os.ErrNotExist, "the blob must not exist before Close")
	require.NoError(t, w.Close())
	read, err := blobFS.ReadAll(ctx, "/streaming/large.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "streamed content")

	// Abort after blocks were staged, of a new and an existing blob
	require.NoError(t, blobFS.WriteAll(ctx, "/streaming/existing.bin", []byte("existing"), 0))
	for _, name := range []string{"aborted.bin", "existing.bin"} {
		w, err = blobFS.OpenWriter("/streaming/"+name, 0)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
		require.NoError(t, w.(fs.WriteAborter).Abort())
		require.NoError(t, w.Close(), "Close after Abort")
	}
	_, err = blobFS.Stat("/streaming/aborted.bin")
	assert.ErrorIs(t, err, os.ErrNotExist, "Abort must not create the blob")
	existing, err := blobFS.ReadAll(ctx, "/streaming/existing.bin")
	require.NoError(t, err)
	assert.Equal(t, "existing", string(existing), "Abort must not change an existing blob")

	// Close of a writer after the file system was closed
	w, err = blobFS.OpenWriter("/streaming/after-close.txt", 0)
	require.NoError(t, err)
	_, err = io.WriteString(w, "data")
	require.NoError(t, err)
	require.NoError(t, blobFS.Close())
	assert.ErrorIs(t, w.Close(), fs.ErrFileSystemClosed)
}

// testFSRemoveAll removes dirPath with a new file system,
// for tests that close their file system.
func testFSRemoveAll(blobFS *fileSystem, dirPath string) error {
	f, err := New(context.Background(), blobFS.client, &Options{SkipContainerCheck: true})
	if err != nil {
		return err
	}
	return f.(fs.RemoveAllFileSystem).RemoveAll(context.Background(), dirPath)
}

// TestClosedFileSystem verifies that a closed file system reports
// fs.ErrFileSystemClosed from every operation instead of using the
// released container client, and that a read-only file system reports
// fs.ErrReadOnlyFileSystem for every write.
func TestClosedFileSystem(t *testing.T) {
	ctx := t.Context()
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + "account.example.com/container", Rooted: true},
	}
	f.closed.Store(true)

	_, err := f.Stat("/blob.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = f.ListDir(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	err = f.ListDirRecursive(ctx, "/", nil, func(*fs.FileInfo) error { return nil })
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenReader("/blob.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.ReadAll(ctx, "/blob.txt")
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenWriter("/blob.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	_, err = f.OpenReadWriter("/blob.txt", 0)
	assert.ErrorIs(t, err, fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.WriteAll(ctx, "/blob.txt", []byte("x"), 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Touch("/blob.txt", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.MakeDir("/dir", 0), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.Remove("/blob.txt"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.RemoveAll(ctx, "/dir"), fs.ErrFileSystemClosed)
	assert.ErrorIs(t, f.CopyFile(ctx, "/a.txt", "/b.txt"), fs.ErrFileSystemClosed)
	assert.NoError(t, f.Close(), "Close must be idempotent")

	readOnly := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + "account.example.com/container", Rooted: true},
		readOnly:   true,
	}
	readable, writable := readOnly.ReadableWritable()
	assert.True(t, readable)
	assert.False(t, writable)
	assert.ErrorIs(t, readOnly.WriteAll(ctx, "/blob.txt", []byte("x"), 0), fs.ErrReadOnlyFileSystem)
	_, err = readOnly.OpenWriter("/blob.txt", 0)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	_, err = readOnly.OpenReadWriter("/blob.txt", 0)
	assert.ErrorIs(t, err, fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, readOnly.MakeDir("/dir", 0), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, readOnly.Remove("/blob.txt"), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, readOnly.Touch("/blob.txt", 0), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, readOnly.RemoveAll(ctx, "/dir"), fs.ErrReadOnlyFileSystem)
	assert.ErrorIs(t, readOnly.CopyFile(ctx, "/a.txt", "/b.txt"), fs.ErrReadOnlyFileSystem)
}

// TestEmptyPath verifies that every method rejects an empty path
// with fs.ErrEmptyPath without a request.
func TestEmptyPath(t *testing.T) {
	ctx := t.Context()
	f := &fileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: Prefix + "account.example.com/container", Rooted: true},
	}
	_, err := f.Stat("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = f.OpenReader("")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = f.OpenWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = f.OpenReadWriter("", 0)
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	_, err = f.ReadAll(ctx, "")
	assert.ErrorIs(t, err, fs.ErrEmptyPath)
	assert.ErrorIs(t, f.ListDir(ctx, "", nil, nil), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.ListDirRecursive(ctx, "", nil, nil), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.WriteAll(ctx, "", nil, 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Touch("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.MakeDir("", 0), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Remove(""), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.RemoveAll(ctx, ""), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.CopyFile(ctx, "", "/b.txt"), fs.ErrEmptyPath)

	// The root is a directory without any request
	info, err := f.Stat("/")
	require.NoError(t, err)
	assert.True(t, info.IsDir)
	_, err = f.ReadAll(ctx, "/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.ErrorIs(t, f.CopyFile(ctx, "/", "/b.txt"), fs.ErrIsDirectory{})
	assert.NoError(t, f.CopyFile(ctx, "/same.txt", "/same.txt"), "copying onto itself is a no-op")
	assert.NoError(t, f.Touch("/", 0), "Touch of the root does nothing")
}
