package protocol

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/gitfs/internal/gittest"
	"github.com/ungerik/go-fs/gitfs/internal/object"
	"github.com/ungerik/go-fs/gitfs/internal/pack"
	"github.com/ungerik/go-fs/gitfs/internal/pktline"
)

func newClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{URL: server.URL + "/owner/repo.git", HTTP: server.Client()}
}

func TestClient(t *testing.T) {
	ctx := t.Context()
	repo := gittest.NewRepo()
	blob := repo.Add(object.TypeBlob, []byte("content"))
	tree := repo.RawTree(object.TreeEntry{Mode: object.ModeFile, Name: "file.txt", OID: blob})
	commit := repo.Commit(tree, time.Unix(0, 0))
	repo.Refs["refs/heads/main"] = commit
	tag := repo.Tag("v1", commit)
	server := &gittest.Server{Repo: repo}
	client := newClient(t, server)

	require.NoError(t, client.Discover(ctx))
	assert.Equal(t, object.SHA1.Name, client.Format.Name)
	assert.True(t, client.FetchFeature("filter"))
	assert.True(t, client.FetchFeature("shallow"))
	assert.False(t, client.FetchFeature("sideband-all"))

	refs, err := client.LsRefs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []Ref{
		{Name: "HEAD", OID: commit, SymrefTarget: "refs/heads/main"},
		{Name: "refs/heads/main", OID: commit},
		{Name: "refs/tags/v1", OID: tag, Peeled: commit},
	}, refs)
	refs, err = client.LsRefs(ctx, "refs/tags/")
	require.NoError(t, err)
	assert.Len(t, refs, 1)

	for filter, want := range map[string][]object.OID{
		"":          {commit, tree, blob},
		"tree:0":    {commit},
		"blob:none": {commit, tree},
	} {
		var objects map[object.OID]*pack.Object
		err = client.Fetch(ctx, FetchRequest{Wants: []object.OID{commit}, Deepen: 1, Filter: filter}, func(r io.Reader) (err error) {
			objects, err = pack.Read(r, client.Format, pack.Limits{MaxObjectSize: 1 << 20, MaxTotalSize: 1 << 20})
			return err
		})
		require.NoError(t, err, filter)
		assert.Len(t, objects, len(want), filter)
		for _, oid := range want {
			assert.Contains(t, objects, oid, filter)
		}
	}

	err = client.Fetch(ctx, FetchRequest{}, nil)
	assert.Error(t, err, "no wants")

	err = client.Fetch(ctx, FetchRequest{Wants: []object.OID{"0000000000000000000000000000000000000000"}}, nil)
	var remote *pktline.RemoteError
	require.True(t, errors.As(err, &remote), err)
	assert.Contains(t, remote.Message, "not our ref")

	server.FetchError = "fatal: broken"
	err = client.Fetch(ctx, FetchRequest{Wants: []object.OID{blob}}, func(r io.Reader) error {
		_, err := io.ReadAll(r)
		return err
	})
	require.True(t, errors.As(err, &remote), err)
	assert.Equal(t, "fatal: broken", remote.Message)
}

// TestDiscoverRedirect checks that the commands after a redirected
// advertisement, like that of a renamed repository, go to the new URL:
// the Go client would turn a redirected POST into a GET.
func TestDiscoverRedirect(t *testing.T) {
	ctx := t.Context()
	repo := gittest.NewRepo()
	repo.Refs["refs/heads/main"] = repo.Commit(repo.RawTree(), time.Unix(0, 0))
	server := &gittest.Server{Repo: repo}
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/old/repo.git/"); ok {
			http.Redirect(w, r, "/owner/repo.git/"+rest+"?"+r.URL.RawQuery, http.StatusMovedPermanently)
			return
		}
		server.ServeHTTP(w, r)
	}))
	client.URL = strings.Replace(client.URL, "/owner/repo.git", "/old/repo.git", 1)

	require.NoError(t, client.Discover(ctx))
	assert.True(t, strings.HasSuffix(client.URL, "/owner/repo.git"), client.URL)
	refs, err := client.LsRefs(ctx)
	require.NoError(t, err)
	assert.Len(t, refs, 2)
}

func TestDiscoverErrors(t *testing.T) {
	ctx := t.Context()
	respond := func(contentType string, packets ...string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			var e pktline.Encoder
			for _, p := range packets {
				switch p {
				case "":
					e.Flush()
				case "delim":
					e.Delim()
				default:
					e.Line(p)
				}
			}
			_, _ = w.Write(e.Bytes())
		})
	}
	const advertisement = "application/x-git-upload-pack-advertisement"
	tests := []struct {
		name    string
		handler http.Handler
		wantErr error
		wantMsg string
	}{
		{name: "Dumb", handler: respond("text/plain"), wantErr: ErrProtocolV2Required},
		{name: "V0", handler: respond(advertisement, "# service=git-upload-pack", "", "0123 HEAD", ""), wantErr: ErrProtocolV2Required},
		{name: "NoFetch", handler: respond(advertisement, "version 2", "ls-refs", ""), wantMsg: "fetch command"},
		{name: "UnknownFormat", handler: respond(advertisement, "version 2", "fetch", "object-format=md5", ""), wantMsg: "object format"},
		{name: "MissingFlush", handler: respond(advertisement, "# service=git-upload-pack", "version 2"), wantMsg: "missing flush"},
		{name: "Truncated", handler: respond(advertisement, "version 2", "fetch"), wantMsg: "reading capability"},
		{name: "Delim", handler: respond(advertisement, "version 2", "delim"), wantMsg: "packet kind"},
		{name: "Empty", handler: respond(advertisement), wantMsg: "reading capability"},
		{name: "NotFound", handler: http.NotFoundHandler(), wantErr: os.ErrNotExist},
		{name: "Forbidden", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusForbidden)
		}), wantErr: os.ErrPermission},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newClient(t, tt.handler).Discover(ctx)
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}

	sha256 := newClient(t, respond(advertisement, "version 2", "agent=x", "fetch=filter", "object-format=sha256", ""))
	require.NoError(t, sha256.Discover(ctx))
	assert.Equal(t, object.SHA256.Name, sha256.Format.Name)

	var httpErr *HTTPError
	err := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})).Discover(ctx)
	require.True(t, errors.As(err, &httpErr))
	assert.NoError(t, httpErr.Unwrap())
	assert.Contains(t, err.Error(), "500")
}

func TestResponseErrors(t *testing.T) {
	ctx := t.Context()
	// The advertisement of a server whose ls-refs and fetch answer with the packets
	server := func(packets ...string) *Client {
		client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var e pktline.Encoder
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				e.Line("version 2")
				e.Line("fetch")
				e.Flush()
			} else {
				for _, p := range packets {
					switch p {
					case "":
						e.Flush()
					case "delim":
						e.Delim()
					default:
						e.Line(p)
					}
				}
			}
			_, _ = w.Write(e.Bytes())
		}))
		require.NoError(t, client.Discover(ctx))
		return client
	}
	oid := "0123456789abcdef0123456789abcdef01234567"

	for name, packets := range map[string][]string{
		"ERR":           {"ERR access denied"},
		"ShortLine":     {"onlyoneword", ""},
		"InvalidOID":    {"xyz refs/heads/main", ""},
		"InvalidPeeled": {oid + " refs/tags/v1 peeled:xyz", ""},
		"Delim":         {"delim"},
		"Truncated":     {oid + " HEAD"},
	} {
		t.Run("LsRefs"+name, func(t *testing.T) {
			_, err := server(packets...).LsRefs(ctx)
			assert.Error(t, err)
		})
	}
	t.Run("LsRefsPrefixTooLong", func(t *testing.T) {
		// Returns an error instead of the panic of pktline.Encoder.Packet
		_, err := server(oid+" HEAD", "").LsRefs(ctx, strings.Repeat("a", pktline.MaxLength))
		require.ErrorIs(t, err, os.ErrInvalid)
	})
	for name, packets := range map[string][]string{
		"ERR":            {"ERR not our ref"},
		"NoPackfile":     {""},
		"UnknownSection": {"bogus", ""},
		"SectionEnd":     {"shallow-info", "shallow " + oid, ""},
		"Truncated":      {"shallow-info"},
		"Empty":          {},
	} {
		t.Run("Fetch"+name, func(t *testing.T) {
			err := server(packets...).Fetch(ctx, FetchRequest{Wants: []object.OID{object.OID(oid)}}, func(io.Reader) error { return nil })
			assert.Error(t, err)
		})
	}
}
