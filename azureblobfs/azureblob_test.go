package azureblobfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
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

// requestPolicy is a pipeline policy of a test client, which must
// send the request with req.Next or return a response itself.
type requestPolicy func(req *policy.Request) (*http.Response, error)

func (p requestPolicy) Do(req *policy.Request) (*http.Response, error) { return p(req) }

// newPolicyFileSystem returns a file system for the test container
// whose client sends every request through the policy p.
func newPolicyFileSystem(t *testing.T, p requestPolicy) *fileSystem {
	t.Helper()
	if !dockerAzuriteAvailable {
		t.Skip("Docker Azurite server not available")
	}
	client, err := container.NewClientFromConnectionString(testConnectionString, testContainer, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{PerCallPolicies: []policy.Policy{p}},
	})
	require.NoError(t, err)
	f, err := New(t.Context(), client, &Options{SkipContainerCheck: true})
	require.NoError(t, err)
	return f.(*fileSystem)
}

// TestTouch_CreatedConcurrently checks that Touch of a missing blob doesn't
// overwrite a blob that another client creates between the listing and
// the upload of the empty blob, which Touch used to replace with an
// empty one. The upload is conditional now (If-None-Match: *).
// The policy of the client creates the blob right before the upload.
func TestTouch_CreatedConcurrently(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	other := testClient(t)
	ctx := t.Context()
	const name = "touch-race/new.txt"
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/touch-race") })
	var created atomic.Bool
	racing := newPolicyFileSystem(t, func(req *policy.Request) (*http.Response, error) {
		raw := req.Raw()
		if raw.Method == http.MethodPut && strings.HasSuffix(raw.URL.Path, "/"+name) && created.CompareAndSwap(false, true) {
			if _, err := other.NewBlockBlobClient(name).UploadBuffer(raw.Context(), []byte("concurrent"), nil); err != nil {
				return nil, err
			}
		}
		return req.Next()
	})

	require.NoError(t, racing.Touch("/"+name, 0))
	require.True(t, created.Load(), "the policy must have created the blob")
	data, err := blobFS.ReadAll(ctx, "/"+name)
	require.NoError(t, err)
	assert.Equal(t, "concurrent", string(data), "Touch must not overwrite the blob")
}

// emptyFirstPagePolicy returns a policy that answers the first request of
// every listing with a prefix that starts with prefix with an empty page
// and a continuation marker, which Azure may return for any listing.
// It sends the request for the next page without the marker, so that
// Azurite answers it with the first real page. emptyPages counts the
// empty pages that the policy returned.
func emptyFirstPagePolicy(prefix string, emptyPages *atomic.Int32) requestPolicy {
	const fakeMarker = "gofs-test-empty-page"
	return func(req *policy.Request) (*http.Response, error) {
		raw := req.Raw()
		query := raw.URL.Query()
		if query.Get("comp") != "list" || !strings.HasPrefix(query.Get("prefix"), prefix) {
			return req.Next()
		}
		if query.Get("marker") == fakeMarker {
			query.Del("marker")
			raw.URL.RawQuery = query.Encode()
			return req.Next()
		}
		emptyPages.Add(1)
		body := `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="` + testContainer + `">` +
			`<Prefix>` + query.Get("prefix") + `</Prefix><Blobs></Blobs><NextMarker>` + fakeMarker + `</NextMarker></EnumerationResults>`
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    raw,
		}, nil
	}
}

// TestTouch_EmptyListingPage checks that Touch reads on when Azure returns
// an empty listing page with a continuation marker, which it may do for
// any listing. Touch read only the first page, so it took an existing blob
// for a missing one, which it replaced with an empty blob, and a directory
// for a missing path, where it created an empty blob.
func TestTouch_EmptyListingPage(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/touch-pages") })
	for _, name := range []string{"touch-pages/keep.txt", "touch-pages/dir/file.txt"} {
		_, err := client.NewBlockBlobClient(name).UploadBuffer(ctx, []byte("keep"), nil)
		require.NoError(t, err)
	}
	var emptyPages atomic.Int32
	paging := newPolicyFileSystem(t, emptyFirstPagePolicy("touch-pages/", &emptyPages))

	before, err := blobFS.Stat("/touch-pages/keep.txt")
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // Last-Modified has a resolution of seconds
	require.NoError(t, paging.Touch("/touch-pages/keep.txt", 0))
	data, err := blobFS.ReadAll(ctx, "/touch-pages/keep.txt")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data), "Touch must not replace an existing blob")
	after, err := blobFS.Stat("/touch-pages/keep.txt")
	require.NoError(t, err)
	assert.True(t, after.Modified.After(before.Modified), "Touch must find the blob and update its modification time")

	require.NoError(t, paging.Touch("/touch-pages/dir", 0))
	info, err := blobFS.Stat("/touch-pages/dir")
	require.NoError(t, err)
	assert.True(t, info.IsDir, "Touch of a directory must not create a blob")
	assert.Positive(t, emptyPages.Load(), "the policy must have returned empty pages")
}

// TestRemove_EmptyListingPage checks that Remove reads on when Azure
// returns an empty listing page with a continuation marker. Remove decided
// from the first page whether a directory is empty, so for a directory
// with content it deleted only the directory marker and reported success.
func TestRemove_EmptyListingPage(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/remove-pages") })
	require.NoError(t, blobFS.MakeDir("/remove-pages/dir", 0))
	_, err := client.NewBlockBlobClient("remove-pages/dir/file.txt").UploadBuffer(ctx, []byte("keep"), nil)
	require.NoError(t, err)
	require.NoError(t, blobFS.MakeDir("/remove-pages/empty", 0))
	var emptyPages atomic.Int32
	paging := newPolicyFileSystem(t, emptyFirstPagePolicy("remove-pages/", &emptyPages))

	err = paging.Remove("/remove-pages/dir")
	assert.ErrorIs(t, err, fs.ErrDirectoryNotEmpty{}, "Remove of a directory with content")
	marker, err := blobFS.blobItem(ctx, "remove-pages/dir/")
	require.NoError(t, err)
	assert.NotNil(t, marker, "Remove must keep the marker of a directory with content")

	// The marker of an empty directory is the only blob of the listing
	require.NoError(t, paging.Remove("/remove-pages/empty"), "Remove of an empty directory")
	_, err = blobFS.Stat("/remove-pages/empty")
	assert.ErrorIs(t, err, os.ErrNotExist, "Remove must delete the marker of an empty directory")
	assert.Positive(t, emptyPages.Load(), "the policy must have returned empty pages")
}

// TestRemoveRoot checks on Azurite that Remove and RemoveAll of the root
// fail with an error wrapping os.ErrInvalid and keep the blobs.
// RemoveAll used to delete every blob of the container.
func TestRemoveRoot(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/remove-root") })
	require.NoError(t, blobFS.WriteAll(ctx, "/remove-root/keep.txt", []byte("keep"), 0))

	for _, root := range []string{"/", "//"} {
		assert.ErrorIs(t, blobFS.RemoveAll(ctx, root), os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorIs(t, blobFS.Remove(root), os.ErrInvalid, "Remove(%q)", root)
	}
	data, err := blobFS.ReadAll(ctx, "/remove-root/keep.txt")
	require.NoError(t, err, "removing the root must not delete any blob")
	assert.Equal(t, "keep", string(data))
}

// newHTTPTestFileSystem returns a file system for a container of a test
// server with handler that stands in for Azure, so that a test runs offline.
func newHTTPTestFileSystem(t *testing.T, handler http.HandlerFunc) *fileSystem {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := container.NewClientWithNoCredential(server.URL+"/account/container", noRetries)
	require.NoError(t, err)
	f, err := New(t.Context(), client, &Options{SkipContainerCheck: true})
	require.NoError(t, err)
	return f.(*fileSystem)
}

// TestReadNotModified checks that ReadAll and the reader of OpenReader
// return an error for a download answered with 304 Not Modified.
// azblob returns that answer as a response without body and without error,
// reading it panicked with a nil pointer dereference.
func TestReadNotModified(t *testing.T) {
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotModified)
	})

	_, err := blobFS.ReadAll(t.Context(), "/file.txt")
	require.ErrorContains(t, err, "304 Not Modified")

	reader, err := blobFS.OpenReader("/file.txt")
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.ErrorContains(t, err, "304 Not Modified")
	require.NoError(t, reader.Close())
}

// TestListingWithoutBlobs checks that a listing answered without a Blobs
// element is an error. azcore decodes such an answer without error into
// a nil Segment, which made every method that lists panic with a nil pointer
// dereference. Azure always sends the element, a proxy or another
// service in its place might not. Every blob is missing, so each method lists.
func TestListingWithoutBlobs(t *testing.T) {
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("comp") == "list" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"></EnumerationResults>`)
			return
		}
		w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
		w.WriteHeader(http.StatusNotFound)
	})
	ctx := t.Context()
	noop := func(*fs.FileInfo) error { return nil }

	_, err := blobFS.Stat("/dir")
	require.ErrorIs(t, err, errListingWithoutBlobs, "Stat")
	err = blobFS.ListDir(ctx, "/dir", nil, noop)
	require.ErrorIs(t, err, errListingWithoutBlobs, "ListDir")
	err = blobFS.ListDirRecursive(ctx, "/dir", nil, noop)
	require.ErrorIs(t, err, errListingWithoutBlobs, "ListDirRecursive")
	err = blobFS.WriteAll(ctx, "/dir/file.txt", []byte("x"), fs.UserReadWrite)
	require.ErrorIs(t, err, errListingWithoutBlobs, "WriteAll")
	err = blobFS.Remove("/dir")
	require.ErrorIs(t, err, errListingWithoutBlobs, "Remove")
	err = blobFS.RemoveAll(ctx, "/dir")
	require.ErrorIs(t, err, errListingWithoutBlobs, "RemoveAll")
}

// TestOpenReaderIgnoredRange checks that the reader of OpenReader returns
// the bytes at its position when the server ignores the Range header
// and sends the whole blob: it returned the start of the blob instead.
func TestOpenReaderIgnoredRange(t *testing.T) {
	const content = "0123456789"
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, content)
	})
	reader, err := blobFS.OpenReader("/file.txt")
	require.NoError(t, err)

	_, err = reader.(io.Seeker).Seek(6, io.SeekStart)
	require.NoError(t, err)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "6789", string(rest))

	// ReadAt opens a body for its own range
	buf := make([]byte, 3)
	_, err = reader.(io.ReaderAt).ReadAt(buf, 2)
	require.NoError(t, err)
	require.Equal(t, "234", string(buf))
	require.NoError(t, reader.Close())
}

// TestOpenReaderBlobChanged checks on Azurite that the reader of OpenReader
// fails when the blob is replaced while it is open. Its range requests were
// not pinned to the ETag of the blob, so after a seek it returned the bytes
// of the new blob after those of the old one. They send If-Match now.
func TestOpenReaderBlobChanged(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	ctx := t.Context()
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/changed") })
	require.NoError(t, blobFS.WriteAll(ctx, "/changed/data.txt", []byte("0123456789"), 0))

	reader, err := blobFS.OpenReader("/changed/data.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	buf := make([]byte, 4)
	_, err = io.ReadFull(reader, buf)
	require.NoError(t, err)
	assert.Equal(t, "0123", string(buf))
	_, err = reader.(io.ReaderAt).ReadAt(buf, 4)
	require.NoError(t, err, "a range request for the unchanged blob")
	assert.Equal(t, "4567", string(buf))

	require.NoError(t, blobFS.WriteAll(ctx, "/changed/data.txt", []byte("abcdefghij"), 0))

	_, err = reader.(io.ReaderAt).ReadAt(buf, 4)
	require.ErrorContains(t, err, "changed while it was being read", "ReadAt after the blob changed")
	assert.True(t, bloberror.HasCode(err, bloberror.ConditionNotMet), "the error wraps the 412 answer: %v", err)
	// The seek closes the body, the next read opens a new range
	_, err = reader.(io.Seeker).Seek(6, io.SeekStart)
	require.NoError(t, err)
	_, err = reader.Read(buf)
	require.ErrorContains(t, err, "changed while it was being read", "Read after the blob changed")

	// A reader opened after the change reads the new blob
	reopened, err := blobFS.OpenReader("/changed/data.txt")
	require.NoError(t, err)
	data, err := io.ReadAll(reopened)
	require.NoError(t, err)
	assert.Equal(t, "abcdefghij", string(data))
	require.NoError(t, reopened.Close())
}

// TestOpenReaderPinsETag checks on a test server that every range request
// of the reader of OpenReader sends If-Match with the ETag of the blob when
// the reader was opened, that a 412 answer is an error telling that the blob
// changed, and that so is an answer with another ETag from a server that
// ignores If-Match, instead of returning the bytes of another version.
func TestOpenReaderPinsETag(t *testing.T) {
	const content = "0123456789"
	var (
		etag     atomic.Value // ETag of the blob on the server
		honor    atomic.Bool  // the server answers a wrong If-Match with 412
		gets     atomic.Int32
		unpinned atomic.Int32 // GET requests without the If-Match of the opened blob
	)
	etag.Store(`"v1"`)
	honor.Store(true)
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		current := etag.Load().(string)
		w.Header().Set("ETag", current)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			return
		}
		gets.Add(1)
		if r.Header.Get("If-Match") != `"v1"` {
			unpinned.Add(1)
		}
		if honor.Load() && r.Header.Get("If-Match") != current {
			w.Header().Set("x-ms-error-code", string(bloberror.ConditionNotMet))
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		// The server ignores the Range header, the reader skips to its offset
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = io.WriteString(w, content)
	})
	reader, err := blobFS.OpenReader("/file.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	readerAt := reader.(io.ReaderAt)

	buf := make([]byte, 3)
	_, err = readerAt.ReadAt(buf, 2)
	require.NoError(t, err)
	assert.Equal(t, "234", string(buf))

	etag.Store(`"v2"`)
	_, err = readerAt.ReadAt(buf, 2)
	require.ErrorContains(t, err, "changed while it was being read", "412 answer")
	assert.True(t, bloberror.HasCode(err, bloberror.ConditionNotMet), "the error wraps the 412 answer: %v", err)

	honor.Store(false)
	_, err = readerAt.ReadAt(buf, 2)
	require.ErrorContains(t, err, "changed while it was being read", "answer with another ETag")
	_, err = io.ReadAll(reader)
	require.ErrorContains(t, err, "changed while it was being read", "sequential read of another ETag")

	assert.Equal(t, int32(4), gets.Load(), "range requests")
	assert.Zero(t, unpinned.Load(), "every range request must send If-Match with the ETag of the opened blob")
}

// TestRemoveRootOffline checks that Remove and RemoveAll refuse the root
// with an error wrapping os.ErrInvalid, like the other file systems, without
// any request. The test server answers every request with an error.
func TestRemoveRootOffline(t *testing.T) {
	var requests atomic.Int32
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotImplemented)
	})
	for _, root := range []string{"/", "//"} {
		err := blobFS.RemoveAll(t.Context(), root)
		assert.ErrorIs(t, err, os.ErrInvalid, "RemoveAll(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "RemoveAll(%q)", root)
		err = blobFS.Remove(root)
		assert.ErrorIs(t, err, os.ErrInvalid, "Remove(%q)", root)
		assert.ErrorContains(t, err, "can't remove root directory", "Remove(%q)", root)
	}
	assert.Zero(t, requests.Load(), "removing the root must not send a request")
}

// listedWithSnapshots returns the names of the blobs with prefix and of
// their snapshots, a snapshot as "name@snapshot".
func listedWithSnapshots(t *testing.T, client *container.Client, prefix string) []string {
	t.Helper()
	var names []string
	pager := client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Prefix:  new(prefix),
		Include: container.ListBlobsInclude{Snapshots: true},
	})
	for pager.More() {
		page, err := pager.NextPage(t.Context())
		require.NoError(t, err)
		for _, item := range page.Segment.BlobItems {
			name := deref(item.Name)
			if snapshot := deref(item.Snapshot); snapshot != "" {
				name += "@" + snapshot
			}
			names = append(names, name)
		}
	}
	return names
}

// TestRemoveSnapshots checks on Azurite that Remove and RemoveAll keep a
// blob with snapshots and return an error wrapping ErrSnapshotsPresent,
// RemoveAll before it deleted anything, and that Options.DeleteSnapshots
// deletes the snapshots with the blobs. Azure refuses to delete a blob with
// snapshots without them, which stopped RemoveAll with a 409 SnapshotsPresent
// error after it had deleted the blobs before that one.
func TestRemoveSnapshots(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	client := testClient(t)
	ctx := t.Context()
	f, err := New(ctx, client, &Options{DeleteSnapshots: true})
	require.NoError(t, err)
	deleting := f.(*fileSystem)
	t.Cleanup(func() { _ = deleting.RemoveAll(context.Background(), "/snapshots") })

	// dir/a.txt is listed before dir/b.txt with its snapshot,
	// file has no snapshot but file.txt with its prefix has one
	for _, name := range []string{"snapshots/file", "snapshots/file.txt", "snapshots/dir/a.txt", "snapshots/dir/b.txt", "snapshots/dir/sub/c.txt"} {
		require.NoError(t, blobFS.WriteAll(ctx, "/"+name, []byte(name), 0))
	}
	require.NoError(t, blobFS.MakeDir("/snapshots/marker", 0))
	for _, name := range []string{"snapshots/file.txt", "snapshots/dir/b.txt", "snapshots/marker/"} {
		_, err := client.NewBlobClient(name).CreateSnapshot(ctx, nil)
		require.NoError(t, err, "snapshot of %q", name)
	}
	before := listedWithSnapshots(t, client, "snapshots/")
	require.Len(t, before, 9, "6 blobs and 3 snapshots: %v", before)

	err = blobFS.Remove("/snapshots/dir/b.txt")
	require.ErrorIs(t, err, ErrSnapshotsPresent, "Remove of a blob with snapshots")
	assert.ErrorContains(t, err, string(blobFS.file("snapshots/dir/b.txt")), "the error names the blob")
	assert.ErrorContains(t, err, "Options.DeleteSnapshots", "the error tells how to delete the snapshots")
	assert.ErrorIs(t, blobFS.Remove("/snapshots/marker"), ErrSnapshotsPresent, "Remove of a directory marker with snapshots")
	for _, filePath := range []string{"/snapshots/dir", "/snapshots/file.txt", "/snapshots/marker", "/snapshots"} {
		assert.ErrorIs(t, blobFS.RemoveAll(ctx, filePath), ErrSnapshotsPresent, "RemoveAll(%q)", filePath)
	}
	assert.Equal(t, before, listedWithSnapshots(t, client, "snapshots/"), "nothing must be deleted")

	// The snapshot of file.txt, a blob with file as prefix, doesn't stop RemoveAll of file
	require.NoError(t, blobFS.RemoveAll(ctx, "/snapshots/file"))
	_, err = blobFS.Stat("/snapshots/file")
	assert.ErrorIs(t, err, os.ErrNotExist, "RemoveAll must delete the blob without snapshots")

	// Move checks the snapshots before it copies anything, a copy has no
	// snapshots and the source couldn't be deleted. It used to replace an
	// existing destination before it failed to delete the source.
	require.NoError(t, blobFS.WriteAll(ctx, "/snapshots/existing.txt", []byte("keep"), 0))
	before = listedWithSnapshots(t, client, "snapshots/")
	src := blobFS.RootDir().Join("snapshots", "dir", "b.txt")
	err = src.MoveTo(ctx, blobFS.RootDir().Join("snapshots", "existing.txt"))
	assert.ErrorIs(t, err, ErrSnapshotsPresent, "Move of a blob with snapshots")
	err = blobFS.RootDir().Join("snapshots", "dir").MoveTo(ctx, blobFS.RootDir().Join("snapshots", "moved"))
	assert.ErrorIs(t, err, ErrSnapshotsPresent, "Move of a directory with a blob with snapshots")
	assert.Equal(t, before, listedWithSnapshots(t, client, "snapshots/"), "Move must neither copy nor delete anything")
	data, err := blobFS.ReadAll(ctx, "/snapshots/existing.txt")
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data), "Move must keep the destination")

	// With DeleteSnapshots Move deletes the source with its snapshots
	require.NoError(t, deleting.Move(ctx, "/snapshots/dir", "/snapshots/moved"), "Move with DeleteSnapshots")
	assert.Empty(t, listedWithSnapshots(t, client, "snapshots/dir/"), "Move must delete the source and its snapshots")
	for _, name := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		data, err := blobFS.ReadAll(ctx, "/snapshots/moved/"+name)
		require.NoError(t, err, "moved %s", name)
		assert.Equal(t, "snapshots/dir/"+name, string(data), "moved %s", name)
	}
	require.NoError(t, deleting.Move(ctx, "/snapshots/moved/b.txt", "/snapshots/dir/b.txt"), "Move back")

	_, err = client.NewBlobClient("snapshots/dir/b.txt").CreateSnapshot(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, deleting.Remove("/snapshots/dir/b.txt"), "Remove with DeleteSnapshots")
	assert.Empty(t, listedWithSnapshots(t, client, "snapshots/dir/b.txt"), "Remove must delete the blob and its snapshots")
	require.NoError(t, deleting.Remove("/snapshots/marker"), "Remove of a directory marker with DeleteSnapshots")
	assert.Empty(t, listedWithSnapshots(t, client, "snapshots/marker/"), "Remove must delete the marker and its snapshots")
	require.NoError(t, deleting.RemoveAll(ctx, "/snapshots"), "RemoveAll with DeleteSnapshots")
	assert.Empty(t, listedWithSnapshots(t, client, "snapshots/"), "RemoveAll must delete the blobs and their snapshots")
}

// TestSnapshotCells checks on Azurite the cells of Remove, RemoveAll and
// Move with and without Options.DeleteSnapshots that TestRemoveSnapshots
// leaves out. With the option they delete plain blobs, blobs and directory
// markers with snapshots and trees, Move copies without the snapshots and
// deletes the source with them. Without it Move of a directory marker with
// a snapshot is refused and changes nothing. Remove of a directory with
// content deletes nothing either way. Every case has its own directory.
func TestSnapshotCells(t *testing.T) {
	client := testClient(t)
	ctx := t.Context()
	f, err := New(ctx, client, nil)
	require.NoError(t, err)
	keeping := f.(*fileSystem)
	f, err = New(ctx, client, &Options{DeleteSnapshots: true})
	require.NoError(t, err)
	deleting := f.(*fileSystem)
	t.Cleanup(func() { _ = deleting.RemoveAll(context.Background(), "/snapshot-cells") })

	tree := []string{"tree/", "tree/a.txt", "tree/sub/b.txt"}
	tests := []struct {
		name            string
		deleteSnapshots bool
		blobs           []string // names below the directory of the case
		snapshotted     []string // blobs that get a snapshot
		op              func(ctx context.Context, f *fileSystem, dir string) error
		wantErr         error
		unchanged       bool     // the blobs and snapshots must stay as they are
		want            []string // else the names left, a snapshot as "name@snapshot"
	}{
		{
			name:            "Remove of a blob",
			deleteSnapshots: true,
			blobs:           []string{"file.txt"},
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.Remove(dir + "/file.txt") },
			want:            []string{},
		},
		{
			name:            "RemoveAll of a blob",
			deleteSnapshots: true,
			blobs:           []string{"file.txt"},
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.RemoveAll(ctx, dir+"/file.txt") },
			want:            []string{},
		},
		{
			name:            "RemoveAll of a blob with a snapshot",
			deleteSnapshots: true,
			blobs:           []string{"file.txt"},
			snapshotted:     []string{"file.txt"},
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.RemoveAll(ctx, dir+"/file.txt") },
			want:            []string{},
		},
		{
			name:            "RemoveAll of a marker with a snapshot",
			deleteSnapshots: true,
			blobs:           []string{"marker/"},
			snapshotted:     []string{"marker/"},
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.RemoveAll(ctx, dir+"/marker") },
			want:            []string{},
		},
		{
			name:            "RemoveAll of a tree",
			deleteSnapshots: true,
			blobs:           tree,
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.RemoveAll(ctx, dir+"/tree") },
			want:            []string{},
		},
		{
			name:            "Move of a blob",
			deleteSnapshots: true,
			blobs:           []string{"file.txt"},
			op: func(ctx context.Context, f *fileSystem, dir string) error {
				return f.Move(ctx, dir+"/file.txt", dir+"/moved.txt")
			},
			want: []string{"moved.txt"},
		},
		{
			name:            "Move of a blob with a snapshot",
			deleteSnapshots: true,
			blobs:           []string{"file.txt"},
			snapshotted:     []string{"file.txt"},
			op: func(ctx context.Context, f *fileSystem, dir string) error {
				return f.Move(ctx, dir+"/file.txt", dir+"/moved.txt")
			},
			want: []string{"moved.txt"}, // The copy has no snapshot
		},
		{
			name:            "Move of a marker with a snapshot",
			deleteSnapshots: true,
			blobs:           []string{"marker/"},
			snapshotted:     []string{"marker/"},
			op: func(ctx context.Context, f *fileSystem, dir string) error {
				return f.Move(ctx, dir+"/marker", dir+"/moved")
			},
			want: []string{"moved/"},
		},
		{
			name:            "Move of a tree",
			deleteSnapshots: true,
			blobs:           tree,
			op: func(ctx context.Context, f *fileSystem, dir string) error {
				return f.Move(ctx, dir+"/tree", dir+"/moved")
			},
			want: []string{"moved/", "moved/a.txt", "moved/sub/b.txt"},
		},
		{
			name:        "Move of a marker with a snapshot without DeleteSnapshots",
			blobs:       []string{"marker/"},
			snapshotted: []string{"marker/"},
			op: func(ctx context.Context, f *fileSystem, dir string) error {
				return f.Move(ctx, dir+"/marker", dir+"/moved")
			},
			wantErr:   ErrSnapshotsPresent,
			unchanged: true,
		},
		{
			name:            "Remove of a directory with a snapshot below",
			deleteSnapshots: true,
			blobs:           []string{"dir/", "dir/a.txt"},
			snapshotted:     []string{"dir/a.txt"},
			op:              func(ctx context.Context, f *fileSystem, dir string) error { return f.Remove(dir + "/dir") },
			wantErr:         fs.ErrDirectoryNotEmpty{},
			unchanged:       true,
		},
		{
			name:        "Remove of a directory with a snapshot below without DeleteSnapshots",
			blobs:       []string{"dir/", "dir/a.txt"},
			snapshotted: []string{"dir/a.txt"},
			op:          func(ctx context.Context, f *fileSystem, dir string) error { return f.Remove(dir + "/dir") },
			wantErr:     fs.ErrDirectoryNotEmpty{},
			unchanged:   true,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			prefix := fmt.Sprintf("snapshot-cells/%d/", i)
			for _, name := range tt.blobs {
				var data []byte
				if !strings.HasSuffix(name, Separator) {
					data = []byte(name) // Directory markers are empty
				}
				_, err := client.NewBlockBlobClient(prefix+name).UploadBuffer(ctx, data, nil)
				require.NoError(t, err, "upload %q", name)
			}
			for _, name := range tt.snapshotted {
				_, err := client.NewBlobClient(prefix+name).CreateSnapshot(ctx, nil)
				require.NoError(t, err, "snapshot of %q", name)
			}
			before := listedWithSnapshots(t, client, prefix)
			require.Len(t, before, len(tt.blobs)+len(tt.snapshotted), "blobs and snapshots: %v", before)

			f := keeping
			if tt.deleteSnapshots {
				f = deleting
			}
			err := tt.op(ctx, f, "/"+strings.TrimSuffix(prefix, Separator))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			after := listedWithSnapshots(t, client, prefix)
			if tt.unchanged {
				assert.Equal(t, before, after, "nothing must be copied or deleted")
				return
			}
			left := make([]string, len(after))
			for j, name := range after {
				name = strings.TrimPrefix(name, prefix)
				if base, _, isSnapshot := strings.Cut(name, "@"); isSnapshot {
					name = base + "@snapshot"
				}
				left[j] = name
			}
			assert.ElementsMatch(t, tt.want, left, "blobs and snapshots left")
		})
	}
}

// TestRemoveSnapshotsOffline checks the requests of Remove and RemoveAll on
// a test server. A delete answered with 409 SnapshotsPresent, here for a
// blob that the snapshot listing of RemoveAll reported without snapshots,
// like for a snapshot that another client created after it, is an error
// wrapping ErrSnapshotsPresent. With Options.DeleteSnapshots every delete
// sends "x-ms-delete-snapshots: include" and RemoveAll lists no snapshots.
func TestRemoveSnapshotsOffline(t *testing.T) {
	var deletes, deletesWithSnapshots, snapshotListings, listings atomic.Int32
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("comp") == "list":
			listings.Add(1)
			if strings.Contains(query.Get("include"), "snapshots") {
				snapshotListings.Add(1)
			}
			var blobs string
			if strings.HasPrefix("dir/a.txt", query.Get("prefix")) {
				blobs = `<Blob><Name>dir/a.txt</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>`
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"><Blobs>`+blobs+`</Blobs></EnumerationResults>`)
		case r.URL.Path == "/account/container/dir/a.txt" && r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "1")
		case r.URL.Path == "/account/container/dir/a.txt" && r.Method == http.MethodDelete:
			deletes.Add(1)
			if r.Header.Get("x-ms-delete-snapshots") != "include" {
				w.Header().Set("x-ms-error-code", string(bloberror.SnapshotsPresent))
				w.WriteHeader(http.StatusConflict)
				return
			}
			deletesWithSnapshots.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := t.Context()

	err := blobFS.Remove("/dir/a.txt")
	require.ErrorIs(t, err, ErrSnapshotsPresent, "Remove")
	err = blobFS.RemoveAll(ctx, "/dir")
	require.ErrorIs(t, err, ErrSnapshotsPresent, "RemoveAll of a blob that got a snapshot after the listing")
	assert.ErrorContains(t, err, string(blobFS.file("dir/a.txt")), "the error names the blob")
	assert.Equal(t, int32(1), snapshotListings.Load(), "RemoveAll lists the tree below the blob with snapshots, Azure checks the blob itself")
	assert.Equal(t, int32(2), deletes.Load())
	assert.Equal(t, int32(1), listings.Load(), "RemoveAll deletes the blobs of the snapshot listing without listing them again")

	// A single blob costs a listing and a delete
	listings.Store(0)
	require.NoError(t, blobFS.RemoveAll(ctx, "/file.txt"), "RemoveAll of a missing blob")
	assert.Equal(t, int32(1), listings.Load(), "RemoveAll of a blob without a tree lists once")

	blobFS.deleteSnapshots = true
	snapshotListings.Store(0)
	require.NoError(t, blobFS.Remove("/dir/a.txt"), "Remove with DeleteSnapshots")
	require.NoError(t, blobFS.RemoveAll(ctx, "/dir"), "RemoveAll with DeleteSnapshots")
	assert.Equal(t, int32(2), deletesWithSnapshots.Load(), "every delete must send x-ms-delete-snapshots: include")
	assert.Zero(t, snapshotListings.Load(), "RemoveAll with DeleteSnapshots must not list snapshots")
}

// TestMoveSnapshotsOffline checks on a test server that Move of a blob
// with a snapshot, and of a directory with such a blob, is an error
// wrapping ErrSnapshotsPresent before it sends a copy or a delete request.
func TestMoveSnapshotsOffline(t *testing.T) {
	var writes atomic.Int32
	baseBlob := `<Blob><Name>dir/a.txt</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>`
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("comp") == "list":
			var blobs string
			if strings.HasPrefix("dir/a.txt", query.Get("prefix")) {
				if strings.Contains(query.Get("include"), "snapshots") {
					blobs = `<Blob><Name>dir/a.txt</Name><Snapshot>2026-10-25T00:00:00.0000000Z</Snapshot><Properties><Content-Length>1</Content-Length></Properties></Blob>`
				}
				blobs += baseBlob
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"><Blobs>`+blobs+`</Blobs></EnumerationResults>`)
		case r.Method == http.MethodPut || r.Method == http.MethodDelete:
			writes.Add(1)
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/account/container/dir/a.txt" && r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "1")
		default:
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := t.Context()

	err := blobFS.Move(ctx, "/dir/a.txt", "/moved.txt")
	require.ErrorIs(t, err, ErrSnapshotsPresent, "Move of a blob with a snapshot")
	assert.ErrorContains(t, err, string(blobFS.file("dir/a.txt")), "the error names the blob")
	require.ErrorIs(t, blobFS.Move(ctx, "/dir", "/moved"), ErrSnapshotsPresent, "Move of a directory with a blob with a snapshot")
	// A missing source is no success that copies or deletes nothing
	require.ErrorIs(t, blobFS.Move(ctx, "/missing.txt", "/moved.txt"), os.ErrNotExist, "Move of a missing blob")
	assert.Zero(t, writes.Load(), "Move must not copy or delete anything")
}

// TestMoveSnapshotOfPrefixSibling checks that the snapshot check of Move
// of a blob ends at the first listed blob with another name: a snapshot
// of dir/a.txt.bak, which has the name of the source as prefix, must not
// refuse the move of dir/a.txt, which has none.
func TestMoveSnapshotOfPrefixSibling(t *testing.T) {
	var writes atomic.Int32
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("comp") == "list":
			var blobs string
			if strings.HasPrefix("dir/a.txt", query.Get("prefix")) || strings.HasPrefix(query.Get("prefix"), "dir/a.txt") {
				blobs = `<Blob><Name>dir/a.txt</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>`
				if strings.Contains(query.Get("include"), "snapshots") {
					blobs += `<Blob><Name>dir/a.txt.bak</Name><Snapshot>2026-10-25T00:00:00.0000000Z</Snapshot><Properties><Content-Length>1</Content-Length></Properties></Blob>`
				}
				blobs += `<Blob><Name>dir/a.txt.bak</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>`
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"><Blobs>`+blobs+`</Blobs></EnumerationResults>`)
		case r.Method == http.MethodPut || r.Method == http.MethodDelete:
			writes.Add(1)
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/account/container/dir/a.txt" && r.Method == http.MethodHead:
			w.Header().Set("Content-Length", "1")
		default:
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
		}
	})

	err := blobFS.Move(t.Context(), "/dir/a.txt", "/moved.txt")
	assert.NotErrorIs(t, err, ErrSnapshotsPresent, "the snapshot of dir/a.txt.bak belongs to another blob")
	assert.NotZero(t, writes.Load(), "Move must get past the snapshot check to the copy, which the server refuses")
}

// TestMoveBlobChanged checks on Azurite that Move keeps a source blob that
// another client replaces during the move, right before its copy or right
// before its delete, and returns an error telling that it changed. Move
// copied and deleted the source unpinned, so it deleted the new version,
// which nobody had copied. The policy of the client replaces the source.
func TestMoveBlobChanged(t *testing.T) {
	blobFS := newTestFileSystem(t, nil)
	other := testClient(t)
	t.Cleanup(func() { _ = blobFS.RemoveAll(context.Background(), "/move-changed") })
	const src = "move-changed/src.txt"
	for _, before := range []string{"copy", "delete"} {
		ctx := t.Context()
		dest := "move-changed/dest-before-" + before + ".txt"
		require.NoError(t, blobFS.WriteAll(ctx, "/"+src, []byte("old"), 0))
		var replaced atomic.Bool
		racing := newPolicyFileSystem(t, func(req *policy.Request) (*http.Response, error) {
			raw := req.Raw()
			copyRequest := raw.Method == http.MethodPut && strings.HasSuffix(raw.URL.Path, "/"+dest)
			deleteRequest := raw.Method == http.MethodDelete && strings.HasSuffix(raw.URL.Path, "/"+src)
			if ((before == "copy" && copyRequest) || (before == "delete" && deleteRequest)) && replaced.CompareAndSwap(false, true) {
				if _, err := other.NewBlockBlobClient(src).UploadBuffer(raw.Context(), []byte("new"), nil); err != nil {
					return nil, err
				}
			}
			return req.Next()
		})

		err := racing.Move(ctx, "/"+src, "/"+dest)
		require.True(t, replaced.Load(), "the policy must have replaced the source before the %s", before)
		require.ErrorContains(t, err, "changed while it was being moved", "replaced before the %s", before)
		assert.True(t, bloberror.HasCode(err, bloberror.ConditionNotMet, bloberror.SourceConditionNotMet), "the error wraps the 412 answer: %v", err)
		data, err := blobFS.ReadAll(ctx, "/"+src)
		require.NoError(t, err, "Move must keep the source replaced before the %s", before)
		assert.Equal(t, "new", string(data), "replaced before the %s", before)
		copied, err := blobFS.ReadAll(ctx, "/"+dest)
		if before == "copy" {
			assert.ErrorIs(t, err, os.ErrNotExist, "Move must not copy the replaced source")
		} else {
			require.NoError(t, err)
			assert.Equal(t, "old", string(copied), "the destination holds the copy of the version that Move found")
		}
	}
}

// moveTestServer stands in for Azure in the offline tests of Move, with
// blobs in memory that are only an ETag. Listings return the blobs with
// a prefix and their ETags, GetProperties the ETag of a blob, a copy and a
// delete honor x-ms-source-if-match and If-Match. replaceBeforeCopy and
// replaceAfterCopy replace the blob with that name, which gets the ETag
// "new", right before the server checks the condition of its copy or right
// after it copied the blob. Copy number failCopy, counted from 1, fails
// with 403 Forbidden. Read the fields with mu locked.
type moveTestServer struct {
	mu                sync.Mutex
	etags             map[string]string // the ETags of the blobs by name
	copies            []string          // "src>dest x-ms-source-if-match" of every copy request
	deletes           []string          // "name If-Match" of every delete request
	replaceBeforeCopy string
	replaceAfterCopy  string
	deleteAfterCopy   string // another client deletes this blob right after its copy
	failCopy          int
}

// ServeHTTP answers a request of the azblob client like Azure.
func (s *moveTestServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const containerPath = "/account/container/"
	name := strings.TrimPrefix(r.URL.Path, containerPath)
	query := r.URL.Query()
	switch {
	case query.Get("comp") == "list":
		var blobs strings.Builder
		for _, n := range slices.Sorted(maps.Keys(s.etags)) {
			if strings.HasPrefix(n, query.Get("prefix")) {
				fmt.Fprintf(&blobs, `<Blob><Name>%s</Name><Properties><Etag>%s</Etag><Content-Length>1</Content-Length></Properties></Blob>`, n, s.etags[n])
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"><Blobs>`+blobs.String()+`</Blobs></EnumerationResults>`)
	case r.Method == http.MethodHead && s.etags[name] != "":
		w.Header().Set("ETag", s.etags[name])
		w.Header().Set("Content-Length", "1")
	case r.Method == http.MethodPut && r.Header.Get("x-ms-copy-source") != "":
		copySource, err := url.Parse(r.Header.Get("x-ms-copy-source"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		src := strings.TrimPrefix(copySource.Path, containerPath)
		ifMatch := r.Header.Get("x-ms-source-if-match")
		s.copies = append(s.copies, src+">"+name+" "+ifMatch)
		if len(s.copies) == s.failCopy {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if src == s.replaceBeforeCopy {
			s.etags[src] = `"new"`
		}
		switch {
		case s.etags[src] == "":
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
			return
		case ifMatch != "" && ifMatch != s.etags[src]:
			w.Header().Set("x-ms-error-code", string(bloberror.SourceConditionNotMet))
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		// The copy gets the ETag of its source, so it shows the copied version
		s.etags[name] = s.etags[src]
		if src == s.replaceAfterCopy {
			s.etags[src] = `"new"`
		}
		if src == s.deleteAfterCopy {
			delete(s.etags, src)
		}
		w.Header().Set("x-ms-copy-status", string(blob.CopyStatusTypeSuccess))
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodDelete:
		ifMatch := r.Header.Get("If-Match")
		s.deletes = append(s.deletes, name+" "+ifMatch)
		switch {
		case s.etags[name] == "":
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
		case ifMatch != "" && ifMatch != s.etags[name]:
			w.Header().Set("x-ms-error-code", string(bloberror.ConditionNotMet))
			w.WriteHeader(http.StatusPreconditionFailed)
		default:
			delete(s.etags, name)
			w.WriteHeader(http.StatusAccepted)
		}
	default:
		w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestMovePinsETag checks on a test server that Move pins the copy and the
// delete of every source blob to the ETag that it found (x-ms-source-if-match,
// If-Match), for a blob and for the blobs of a directory, with and without
// Options.DeleteSnapshots, whose directory listing doesn't list snapshots.
// Move copied and deleted unpinned, so a source blob that another client
// replaced after its copy was deleted, and with it the version that nobody
// had copied. A blob replaced before its copy is neither copied nor deleted,
// one replaced after its copy is kept, both are errors.
func TestMovePinsETag(t *testing.T) {
	tests := []struct {
		name                     string
		src, dest                string
		replaceBefore            string // blob replaced right before the server checks its copy
		replaceAfter             string // blob replaced right after its copy
		wantCopies, wantDeletes  []string
		wantBlobs                map[string]string
		wantChanged, wantWrapped string // blob that changed, error code of the 412 answer
	}{
		{
			name: "blob",
			src:  "/dir/a.txt", dest: "/moved.txt",
			wantCopies:  []string{`dir/a.txt>moved.txt "a1"`},
			wantDeletes: []string{`dir/a.txt "a1"`},
			wantBlobs:   map[string]string{"dir/b.txt": `"b1"`, "moved.txt": `"a1"`},
		},
		{
			name: "blob replaced before its copy",
			src:  "/dir/a.txt", dest: "/moved.txt",
			replaceBefore: "dir/a.txt",
			wantCopies:    []string{`dir/a.txt>moved.txt "a1"`},
			wantBlobs:     map[string]string{"dir/a.txt": `"new"`, "dir/b.txt": `"b1"`},
			wantChanged:   "dir/a.txt", wantWrapped: string(bloberror.SourceConditionNotMet),
		},
		{
			name: "blob replaced after its copy",
			src:  "/dir/a.txt", dest: "/moved.txt",
			replaceAfter: "dir/a.txt",
			wantCopies:   []string{`dir/a.txt>moved.txt "a1"`},
			wantDeletes:  []string{`dir/a.txt "a1"`},
			wantBlobs:    map[string]string{"dir/a.txt": `"new"`, "dir/b.txt": `"b1"`, "moved.txt": `"a1"`},
			wantChanged:  "dir/a.txt", wantWrapped: string(bloberror.ConditionNotMet),
		},
		{
			name: "directory",
			src:  "/dir", dest: "/moved",
			wantCopies:  []string{`dir/a.txt>moved/a.txt "a1"`, `dir/b.txt>moved/b.txt "b1"`},
			wantDeletes: []string{`dir/a.txt "a1"`, `dir/b.txt "b1"`},
			wantBlobs:   map[string]string{"moved/a.txt": `"a1"`, "moved/b.txt": `"b1"`},
		},
		{
			name: "directory with a blob replaced before its copy",
			src:  "/dir", dest: "/moved",
			replaceBefore: "dir/b.txt",
			wantCopies:    []string{`dir/a.txt>moved/a.txt "a1"`, `dir/b.txt>moved/b.txt "b1"`},
			wantBlobs:     map[string]string{"dir/a.txt": `"a1"`, "dir/b.txt": `"new"`, "moved/a.txt": `"a1"`},
			wantChanged:   "dir/b.txt", wantWrapped: string(bloberror.SourceConditionNotMet),
		},
		{
			name: "directory with a blob replaced after its copy",
			src:  "/dir", dest: "/moved",
			replaceAfter: "dir/a.txt",
			wantCopies:   []string{`dir/a.txt>moved/a.txt "a1"`, `dir/b.txt>moved/b.txt "b1"`},
			wantDeletes:  []string{`dir/a.txt "a1"`},
			wantBlobs:    map[string]string{"dir/a.txt": `"new"`, "dir/b.txt": `"b1"`, "moved/a.txt": `"a1"`, "moved/b.txt": `"b1"`},
			wantChanged:  "dir/a.txt", wantWrapped: string(bloberror.ConditionNotMet),
		},
	}
	for _, deleteSnapshots := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/DeleteSnapshots=%t", tt.name, deleteSnapshots), func(t *testing.T) {
				server := &moveTestServer{
					etags:             map[string]string{"dir/a.txt": `"a1"`, "dir/b.txt": `"b1"`},
					replaceBeforeCopy: tt.replaceBefore,
					replaceAfterCopy:  tt.replaceAfter,
				}
				blobFS := newHTTPTestFileSystem(t, server.ServeHTTP)
				blobFS.deleteSnapshots = deleteSnapshots

				err := blobFS.Move(t.Context(), tt.src, tt.dest)
				if tt.wantChanged == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, string(blobFS.file(tt.wantChanged))+" changed while it was being moved")
					assert.True(t, bloberror.HasCode(err, bloberror.Code(tt.wantWrapped)), "the error wraps the 412 answer: %v", err)
				}
				server.mu.Lock()
				defer server.mu.Unlock()
				assert.Equal(t, tt.wantCopies, server.copies, "copy requests")
				assert.Equal(t, tt.wantDeletes, server.deletes, "delete requests")
				assert.Equal(t, tt.wantBlobs, server.etags, "blobs left")
			})
		}
	}
}

// TestMoveDirectoryCopyFails checks on a test server that a directory Move
// whose second copy fails deletes no source blob: Move deletes the source
// blobs only after every copy succeeded, the copy of the first blob is left
// at the destination.
func TestMoveDirectoryCopyFails(t *testing.T) {
	server := &moveTestServer{
		etags:    map[string]string{"dir/a.txt": `"a1"`, "dir/b.txt": `"b1"`},
		failCopy: 2,
	}
	blobFS := newHTTPTestFileSystem(t, server.ServeHTTP)

	err := blobFS.Move(t.Context(), "/dir", "/moved")
	require.Error(t, err)
	assert.True(t, isPermissionDenied(err), "the error of the second copy: %v", err)
	server.mu.Lock()
	defer server.mu.Unlock()
	assert.Len(t, server.copies, 2, "copy requests")
	assert.Empty(t, server.deletes, "Move must not delete a source blob before every copy succeeded")
	assert.Equal(t, map[string]string{"dir/a.txt": `"a1"`, "dir/b.txt": `"b1"`, "moved/a.txt": `"a1"`}, server.etags, "blobs left")
}

// TestMoveOntoBlobThatIsADirectory checks on a test server that Move of a
// blob onto a name that is a blob and a directory, with blobs below it,
// is an fs.ErrIsDirectory without a copy or a delete. Move checks that
// itself since it no longer copies with CopyFile.
func TestMoveOntoBlobThatIsADirectory(t *testing.T) {
	server := &moveTestServer{
		etags: map[string]string{"a.txt": `"a1"`, "dest": `"d1"`, "dest/x.txt": `"x1"`},
	}
	blobFS := newHTTPTestFileSystem(t, server.ServeHTTP)

	err := blobFS.Move(t.Context(), "/a.txt", "/dest")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	server.mu.Lock()
	defer server.mu.Unlock()
	assert.Empty(t, server.copies, "no copy onto a directory")
	assert.Empty(t, server.deletes, "no delete")
}

// TestMoveBlobDeletedAfterCopy checks on a test server that a source blob
// that another client deletes after Move copied it is no error: the copy
// is at the destination and the source is gone, like after the move.
func TestMoveBlobDeletedAfterCopy(t *testing.T) {
	server := &moveTestServer{
		etags:           map[string]string{"dir/a.txt": `"a1"`, "dir/b.txt": `"b1"`},
		deleteAfterCopy: "dir/a.txt",
	}
	blobFS := newHTTPTestFileSystem(t, server.ServeHTTP)

	require.NoError(t, blobFS.Move(t.Context(), "/dir", "/moved"))
	server.mu.Lock()
	defer server.mu.Unlock()
	assert.Equal(t, map[string]string{"moved/a.txt": `"a1"`, "moved/b.txt": `"b1"`}, server.etags, "blobs left")
}

// TestRemoveAllScanError checks that RemoveAll deletes nothing
// if the listing of the snapshots fails.
func TestRemoveAllScanError(t *testing.T) {
	var deletes atomic.Int32
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	assert.Error(t, blobFS.RemoveAll(t.Context(), "/dir"))
	assert.Zero(t, deletes.Load(), "RemoveAll must not delete anything after a failed listing")
}

// TestRemoveAllManyBlobs checks on a test server that RemoveAll of a tree
// with more blobs than the snapshot listing keeps the names of
// (maxRemoveAllNames) lists the tree a second time and deletes every blob.
// The test server returns all of them on one listing page.
func TestRemoveAllManyBlobs(t *testing.T) {
	names := make([]string, maxRemoveAllNames+1)
	var blobs strings.Builder
	for i := range names {
		names[i] = fmt.Sprintf("dir/%05d.txt", i)
		fmt.Fprintf(&blobs, `<Blob><Name>%s</Name><Properties><Content-Length>1</Content-Length></Properties></Blob>`, names[i])
	}
	listing := `<?xml version="1.0" encoding="utf-8"?><EnumerationResults ContainerName="container"><Blobs>` + blobs.String() + `</Blobs></EnumerationResults>`
	var (
		mu       sync.Mutex
		deleted  []string
		listings atomic.Int32
	)
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/account/container/")
		switch {
		case r.URL.Query().Get("comp") == "list" && r.URL.Query().Get("prefix") == "dir/":
			listings.Add(1)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, listing)
		case r.Method == http.MethodDelete && strings.HasPrefix(name, "dir/"):
			mu.Lock()
			deleted = append(deleted, name)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("x-ms-error-code", string(bloberror.BlobNotFound))
			w.WriteHeader(http.StatusNotFound)
		}
	})

	require.NoError(t, blobFS.RemoveAll(t.Context(), "/dir"))
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(deleted)
	assert.Equal(t, names, deleted, "RemoveAll must delete every blob of the tree once")
	assert.Equal(t, int32(2), listings.Load(), "the snapshot listing and the listing of the blobs to delete")
}

// TestOpenReaderRoot checks that OpenReader of the root directory
// is an fs.ErrIsDirectory without a request.
func TestOpenReaderRoot(t *testing.T) {
	var requests atomic.Int32
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := blobFS.OpenReader("/")
	assert.ErrorIs(t, err, fs.ErrIsDirectory{})
	assert.Zero(t, requests.Load(), "OpenReader of the root must not send a request")
}

// transportFunc is the transport of a test client,
// which answers every request itself.
type transportFunc func(req *http.Request) (*http.Response, error)

func (f transportFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

// newTransportFileSystem returns a file system for a container whose
// client sends every request to the transport t, so that a test runs
// offline and can send responses that a test server can't, like a
// Content-Length that doesn't match the body.
func newTransportFileSystem(t *testing.T, transport transportFunc) *fileSystem {
	t.Helper()
	client, err := container.NewClientWithNoCredential("https://account.blob.core.windows.net/container", &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transport,
			Retry:     policy.RetryOptions{MaxRetries: -1},
		},
	})
	require.NoError(t, err)
	f, err := New(t.Context(), client, &Options{SkipContainerCheck: true})
	require.NoError(t, err)
	return f.(*fileSystem)
}

// TestReadAllCapsSizeHint checks that ReadAll allocates its buffer for at
// most maxSizeHint of the Content-Length before any data arrived. It allocated
// the whole Content-Length, so a huge blob or a wrong header made the
// process die of an out-of-memory error at once, which no context can
// cancel. The transport announces 1 GiB and the largest int, but sends
// a short body, which ReadAll must read completely.
func TestReadAllCapsSizeHint(t *testing.T) {
	const body = "a short body"
	for _, contentLength := range []int64{1 << 30, math.MaxInt} {
		blobFS := newTransportFileSystem(t, func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				Status:        "200 OK",
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Length": {strconv.FormatInt(contentLength, 10)}},
				ContentLength: contentLength,
				Body:          io.NopCloser(strings.NewReader(body)),
				Request:       req,
			}, nil
		})
		read, err := blobFS.ReadAll(t.Context(), "/huge.bin")
		require.NoError(t, err, "Content-Length %d", contentLength)
		assert.Equal(t, body, string(read), "Content-Length %d", contentLength)
		assert.LessOrEqual(t, cap(read), maxSizeHint+bytes.MinRead, "Content-Length %d must not allocate more than the cap", contentLength)
	}
}

// TestReadAllPreallocates checks that ReadAll reads into a buffer of the
// Content-Length of the download, instead of growing one from 512 bytes.
func TestReadAllPreallocates(t *testing.T) {
	data := make([]byte, 1<<20+17)
	for i := range data {
		data[i] = byte(i % 251)
	}
	blobFS := newHTTPTestFileSystem(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	})
	read, err := blobFS.ReadAll(t.Context(), "/data.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "content")
	assert.LessOrEqual(t, cap(read), len(data)+bytes.MinRead, "the buffer must be allocated for the size of the blob")
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

	// The staged blocks replace an existing blob when Close commits the block list
	require.NoError(t, blobFS.WriteAll(ctx, "/streaming/replaced.bin", []byte("existing"), 0))
	w, err = blobFS.OpenWriter("/streaming/replaced.bin", 0)
	require.NoError(t, err)
	for start := 0; start < len(data); start += 32 * 1024 {
		_, err = w.Write(data[start:min(start+32*1024, len(data))])
		require.NoError(t, err)
	}
	existing, err := blobFS.ReadAll(ctx, "/streaming/replaced.bin")
	require.NoError(t, err)
	assert.Equal(t, "existing", string(existing), "the blob must not change before Close")
	require.NoError(t, w.Close())
	read, err = blobFS.ReadAll(ctx, "/streaming/replaced.bin")
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, read), "Close must replace the existing blob")
	blocks, err := blobFS.client.NewBlockBlobClient("streaming/replaced.bin").GetBlockList(ctx, blockblob.BlockListTypeCommitted, nil)
	require.NoError(t, err)
	assert.Len(t, blocks.CommittedBlocks, 4, "the blob must be committed from the 4 staged blocks")

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
	existing, err = blobFS.ReadAll(ctx, "/streaming/existing.bin")
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
	assert.ErrorIs(t, f.Move(ctx, "", "/b.txt"), fs.ErrEmptyPath)
	assert.ErrorIs(t, f.Move(ctx, "/a.txt", ""), fs.ErrEmptyPath)

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
