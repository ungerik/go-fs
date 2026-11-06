package gitfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ungerik/go-fs/gitfs/internal/object"
	"github.com/ungerik/go-fs/gitfs/internal/pack"
	"github.com/ungerik/go-fs/gitfs/internal/protocol"
)

var (
	// errGitHubLossyName is the error of fetchGitHubTree for a tree
	// with a name that the JSON of the REST API can't represent.
	errGitHubLossyName = errors.New("GitHub tree has a name with a replacement character")

	// errGitHubTruncated is the error of fetchGitHubTree for a tree
	// with more entries than one response of the REST API can hold.
	errGitHubTruncated = errors.New("GitHub tree is truncated")

	// errGitHubRawMismatch is the error of fetchGitHubRaw for content
	// that doesn't match the object ID of the blob.
	errGitHubRawMismatch = errors.New("content from raw.githubusercontent.com doesn't match the blob")
)

// github holds the endpoints of the GitHub shortcuts of a repository.
type github struct {
	apiURL string // like "https://api.github.com"
	rawURL string // like "https://raw.githubusercontent.com"
	owner  string
	repo   string
}

// githubRepo returns the GitHub endpoints of a repository URL
// on github.com, or nil for other hosts.
func githubRepo(repoURL *url.URL) *github {
	if !strings.EqualFold(repoURL.Hostname(), "github.com") || repoURL.Scheme != "https" || repoURL.Port() != "" {
		return nil
	}
	elements := strings.Split(strings.Trim(repoURL.Path, "/"), "/")
	if len(elements) != 2 || elements[0] == "" || elements[1] == "" {
		return nil
	}
	return &github{
		apiURL: "https://api.github.com",
		rawURL: "https://raw.githubusercontent.com",
		owner:  elements[0],
		repo:   strings.TrimSuffix(elements[1], ".git"),
	}
}

// githubTree is the response of the GitHub REST API for a tree.
type githubTree struct {
	SHA       string `json:"sha"`
	Truncated bool   `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size int64  `json:"size"`
	} `json:"tree"`
}

// fetchGitHubTree returns the entries of a tree from the GitHub REST API
// by the slash separated path relative to the tree, "" for the tree
// itself. With recursive, the entries of all sub-trees are included.
// A truncated response returns errGitHubTruncated, a tree with a name
// with a U+FFFD replacement character returns errGitHubLossyName.
func (f *FileSystem) fetchGitHubTree(ctx context.Context, tree object.OID, recursive bool) (map[string][]treeEntry, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s",
		f.github.apiURL,
		url.PathEscape(f.github.owner),
		url.PathEscape(f.github.repo),
		tree,
	)
	if recursive {
		u += "?recursive=1"
	}
	response, err := f.githubGet(ctx, u, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var result githubTree
	err = json.NewDecoder(response.Body).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("decoding GitHub tree %s: %w", tree, err)
	}
	if result.Truncated {
		return nil, fmt.Errorf("%w: tree %s", errGitHubTruncated, tree)
	}
	if object.OID(result.SHA) != tree {
		return nil, fmt.Errorf("GitHub returned tree %.100q instead of %s", result.SHA, tree)
	}
	format := f.client.Format
	entries := make(map[string][]treeEntry)
	for _, e := range result.Tree {
		if strings.ContainsRune(e.Path, utf8.RuneError) {
			// JSON can't hold names that are not valid UTF-8, GitHub replaces
			// their invalid bytes, which makes them indistinguishable from
			// valid names with U+FFFD: git returns the real names
			return nil, fmt.Errorf("%w: tree %s", errGitHubLossyName, tree)
		}
		mode, err := strconv.ParseUint(e.Mode, 8, 32)
		if err != nil {
			return nil, fmt.Errorf("GitHub tree %s has entry %.200q with invalid mode %.20q", tree, e.Path, e.Mode)
		}
		oid, err := format.ParseOID(e.SHA)
		if err != nil {
			return nil, fmt.Errorf("GitHub tree %s has entry %.200q: %w", tree, e.Path, err)
		}
		dir, name := "", e.Path
		if i := strings.LastIndexByte(e.Path, '/'); i >= 0 {
			dir, name = e.Path[:i], e.Path[i+1:]
		}
		size := int64(-1)
		if e.Type == "blob" {
			size = e.Size
		}
		entries[dir] = append(entries[dir], treeEntry{
			TreeEntry: object.TreeEntry{Mode: uint32(mode), Name: name, OID: oid},
			size:      size,
		})
	}
	return entries, nil
}

// fetchGitHubRaw returns the content of a file of the commit from
// raw.githubusercontent.com. It returns an error wrapping pack.ErrTooLarge
// for content above Options.MaxObjectSize and errGitHubRawMismatch for
// content that doesn't match the object ID of the blob of the file.
func (f *FileSystem) fetchGitHubRaw(ctx context.Context, file *node) ([]byte, error) {
	elements := strings.Split(file.path, "/")
	for i, element := range elements {
		elements[i] = url.PathEscape(element)
	}
	u := fmt.Sprintf("%s/%s/%s/%s/%s",
		f.github.rawURL,
		url.PathEscape(f.github.owner),
		url.PathEscape(f.github.repo),
		f.commitOID(),
		strings.Join(elements, "/"),
	)
	response, err := f.githubGet(ctx, u, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, f.maxObjectSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > f.maxObjectSize {
		return nil, fmt.Errorf("file %q has a size above the limit %d: %w", file.path, f.maxObjectSize, pack.ErrTooLarge)
	}
	if f.client.Format.Hash(object.TypeBlob, data) != file.oid {
		return nil, fmt.Errorf("%w %s of %q", errGitHubRawMismatch, file.oid, file.path)
	}
	return data, nil
}

func (f *FileSystem) githubGet(ctx context.Context, u, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	request.Header.Set("User-Agent", protocol.Agent)
	if f.token != "" {
		request.Header.Set("Authorization", "Bearer "+f.token)
	}
	response, err := f.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, &protocol.HTTPError{URL: request.URL.Redacted(), StatusCode: response.StatusCode, Status: response.Status}
	}
	return response, nil
}
