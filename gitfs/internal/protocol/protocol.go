// Package protocol implements the client side of git protocol version 2
// over smart HTTP: the capability advertisement, ls-refs and fetch,
// see gitprotocol-v2(5) and gitprotocol-http(5).
package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/ungerik/go-fs/gitfs/internal/object"
	"github.com/ungerik/go-fs/gitfs/internal/pktline"
)

// ErrProtocolV2Required is returned by Discover for servers
// that don't speak git protocol version 2 over smart HTTP.
var ErrProtocolV2Required = errors.New("git server does not support protocol version 2 over smart HTTP")

// Agent is sent to servers that advertise the agent capability.
const Agent = "go-fs-gitfs/1"

// HTTPError is the error of a request that the server answered
// with a status other than 200 OK. It unwraps to os.ErrPermission
// for 401 and 403 and to os.ErrNotExist for 404.
type HTTPError struct {
	URL        string // without credentials
	StatusCode int
	Status     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("git server answered %s for %s", e.Status, e.URL)
}

func (e *HTTPError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return os.ErrPermission
	case http.StatusNotFound:
		return os.ErrNotExist
	}
	return nil
}

// Client talks to the upload-pack service of one repository.
// Call Discover before any other method.
type Client struct {
	// URL of the repository without credentials and without trailing slash,
	// like "https://github.com/ungerik/go-fs.git".
	URL string
	// HTTP is the client used for requests.
	HTTP *http.Client
	// Authorize is called for every request to add credentials, may be nil.
	Authorize func(*http.Request)

	// Format is the object format of the repository, set by Discover.
	Format object.Format

	capabilities map[string]string
}

// Discover requests the capability advertisement of the server
// and sets Format. If the server redirects the request to another
// path on the same host, Discover sets URL to the redirected one.
func (c *Client) Discover(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Git-Protocol", "version=2")
	response, err := c.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Like git with http.followRedirects=initial, the later requests
	// go to the URL that the advertisement was redirected to, like
	// that of a renamed repository: a redirected POST would become a
	// GET. Only on the same host, the credentials stay with the host.
	if final := response.Request.URL; final.Scheme == request.URL.Scheme && final.Host == request.URL.Host {
		redirected := *final
		redirected.User, redirected.RawQuery, redirected.Fragment = nil, "", ""
		if base, ok := strings.CutSuffix(redirected.String(), "/info/refs"); ok {
			c.URL = base
		}
	}

	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/x-git-upload-pack-advertisement" {
		// A dumb HTTP server or not a git repository at all
		return ErrProtocolV2Required
	}
	decoder := pktline.NewDecoder(response.Body)
	kind, line, err := decoder.NextLine()
	if err != nil {
		return fmt.Errorf("reading capability advertisement: %w", err)
	}
	if kind == pktline.Data && line == "# service=git-upload-pack" {
		// Smart HTTP servers prefix the advertisement with the service
		kind, _, err = decoder.NextLine()
		if err != nil || kind != pktline.Flush {
			return fmt.Errorf("invalid capability advertisement: %w", errors.Join(err, errors.New("missing flush after service line")))
		}
		kind, line, err = decoder.NextLine()
		if err != nil {
			return fmt.Errorf("reading capability advertisement: %w", err)
		}
	}
	if kind != pktline.Data || line != "version 2" {
		return ErrProtocolV2Required
	}
	c.capabilities = make(map[string]string)
	for {
		kind, line, err = decoder.NextLine()
		if err != nil {
			return fmt.Errorf("reading capability advertisement: %w", err)
		}
		if kind == pktline.Flush {
			break
		}
		if kind != pktline.Data {
			return fmt.Errorf("invalid capability advertisement packet kind %d", kind)
		}
		key, value, _ := strings.Cut(line, "=")
		c.capabilities[key] = value
	}
	if _, ok := c.capabilities["fetch"]; !ok {
		return errors.New("git server does not advertise the fetch command")
	}
	c.Format, err = object.FormatByName(c.capabilities["object-format"])
	return err
}

// FetchFeature reports whether the server advertised the feature
// of the fetch command, like "filter" or "shallow".
func (c *Client) FetchFeature(feature string) bool {
	features, ok := c.capabilities["fetch"]
	if !ok {
		return false
	}
	for f := range strings.FieldsSeq(features) {
		if f == feature {
			return true
		}
	}
	return false
}

// Ref is a reference returned by LsRefs.
type Ref struct {
	Name         string     // like "refs/heads/main" or "HEAD"
	OID          object.OID // the object the ref points to
	Peeled       object.OID // the object an annotated tag points to, else empty
	SymrefTarget string     // like "refs/heads/main" for HEAD, else empty
}

// LsRefs returns the references of the server whose names
// start with one of the prefixes, or all references without prefixes.
// Annotated tags are peeled and the targets of symbolic refs returned.
func (c *Client) LsRefs(ctx context.Context, prefixes ...string) ([]Ref, error) {
	for _, prefix := range prefixes {
		// The line has a trailing newline and the packet a 4 byte header
		if len("ref-prefix ")+len(prefix)+1+4 > pktline.MaxLength {
			return nil, fmt.Errorf("ref prefix of %d bytes doesn't fit into a pkt-line: %w", len(prefix), os.ErrInvalid)
		}
	}
	var request pktline.Encoder
	c.command(&request, "ls-refs")
	request.Line("peel")
	request.Line("symrefs")
	for _, prefix := range prefixes {
		request.Line("ref-prefix " + prefix)
	}
	request.Flush()

	response, err := c.post(ctx, &request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var refs []Ref
	decoder := pktline.NewDecoder(response.Body)
	for {
		kind, line, err := decoder.NextLine()
		if err != nil {
			return nil, fmt.Errorf("reading ls-refs response: %w", err)
		}
		if kind == pktline.Flush {
			return refs, nil
		}
		if kind != pktline.Data {
			return nil, fmt.Errorf("invalid ls-refs response packet kind %d", kind)
		}
		if msg, ok := strings.CutPrefix(line, "ERR "); ok {
			return nil, &pktline.RemoteError{Message: msg}
		}
		fields := strings.Split(line, " ")
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid ls-refs line %.200q", line)
		}
		oid, err := c.Format.ParseOID(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid ls-refs line %.200q: %w", line, err)
		}
		ref := Ref{Name: fields[1], OID: oid}
		for _, attribute := range fields[2:] {
			if target, ok := strings.CutPrefix(attribute, "symref-target:"); ok {
				ref.SymrefTarget = target
			} else if peeled, ok := strings.CutPrefix(attribute, "peeled:"); ok {
				ref.Peeled, err = c.Format.ParseOID(peeled)
				if err != nil {
					return nil, fmt.Errorf("invalid ls-refs line %.200q: %w", line, err)
				}
			}
		}
		refs = append(refs, ref)
	}
}

// FetchRequest are the arguments of a fetch command.
type FetchRequest struct {
	Wants []object.OID
	// Deepen limits the history of wanted commits, 0 for no limit.
	// Requires the "shallow" fetch feature.
	Deepen int
	// Filter is an object filter like "blob:none" or "tree:0",
	// empty for no filter. Requires the "filter" fetch feature.
	Filter string
}

// Fetch requests a packfile with the wanted objects
// and calls readPack with the data of the packfile.
func (c *Client) Fetch(ctx context.Context, fetch FetchRequest, readPack func(pack io.Reader) error) error {
	if len(fetch.Wants) == 0 {
		return errors.New("fetch without wanted objects")
	}
	var request pktline.Encoder
	c.command(&request, "fetch")
	request.Line("no-progress")
	request.Line("ofs-delta")
	if fetch.Deepen > 0 {
		request.Line(fmt.Sprintf("deepen %d", fetch.Deepen))
	}
	if fetch.Filter != "" {
		request.Line("filter " + fetch.Filter)
	}
	for _, want := range fetch.Wants {
		request.Line("want " + string(want))
	}
	request.Line("done")
	request.Flush()

	response, err := c.post(ctx, &request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	decoder := pktline.NewDecoder(response.Body)
	for {
		kind, section, err := decoder.NextLine()
		if err != nil {
			return fmt.Errorf("reading fetch response: %w", err)
		}
		if kind != pktline.Data {
			return errors.New("fetch response has no packfile")
		}
		if msg, ok := strings.CutPrefix(section, "ERR "); ok {
			return &pktline.RemoteError{Message: msg}
		}
		switch section {
		case "packfile":
			return readPack(pktline.NewSidebandReader(decoder))
		case "acknowledgments", "shallow-info", "wanted-refs", "packfile-uris":
			// Skip the lines of the section up to the delimiter
			// that separates it from the next section
			for kind == pktline.Data {
				kind, _, err = decoder.NextLine()
				if err != nil {
					return fmt.Errorf("reading fetch response section %s: %w", section, err)
				}
			}
			if kind != pktline.Delim {
				return fmt.Errorf("fetch response has no packfile after section %s", section)
			}
		default:
			return fmt.Errorf("unexpected fetch response section %.200q", section)
		}
	}
}

// command writes the command and the capabilities
// that the client sends with every command.
func (c *Client) command(request *pktline.Encoder, command string) {
	request.Line("command=" + command)
	if _, ok := c.capabilities["agent"]; ok {
		request.Line("agent=" + Agent)
	}
	if _, ok := c.capabilities["object-format"]; ok {
		request.Line("object-format=" + c.Format.Name)
	}
	request.Delim()
}

func (c *Client) post(ctx context.Context, body *pktline.Encoder) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/git-upload-pack", bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Git-Protocol", "version=2")
	request.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	request.Header.Set("Accept", "application/x-git-upload-pack-result")
	return c.do(request)
}

func (c *Client) do(request *http.Request) (*http.Response, error) {
	if c.Authorize != nil {
		c.Authorize(request)
	}
	request.Header.Set("User-Agent", Agent)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, &HTTPError{URL: request.URL.Redacted(), StatusCode: response.StatusCode, Status: response.Status}
	}
	return response, nil
}
