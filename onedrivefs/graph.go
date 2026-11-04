package onedrivefs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxRetries is the number of times a request is repeated
	// after a 429 Too Many Requests or 503 Service Unavailable.
	maxRetries = 4

	// maxRetryDelay limits the wait before a repeated request,
	// also the one a Retry-After header asks for.
	maxRetryDelay = time.Minute

	// itemFields are the driveItem properties the file system reads,
	// requested with $select.
	itemFields = "id,name,eTag,size,lastModifiedDateTime,fileSystemInfo,file,folder,package,root"
)

// driveItem is the part of a Microsoft Graph driveItem resource
// that the file system reads.
type driveItem struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ETag           string    `json:"eTag"`
	Size           int64     `json:"size"`
	LastModified   time.Time `json:"lastModifiedDateTime"`
	FileSystemInfo *struct {
		LastModified time.Time `json:"lastModifiedDateTime"`
	} `json:"fileSystemInfo"`
	File   *struct{} `json:"file"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
	Package *struct{} `json:"package"`
	Root    *struct{} `json:"root"`
}

// isDir reports whether the item is a folder, the root,
// or a package like a OneNote notebook, which has children.
func (item *driveItem) isDir() bool {
	return item.Folder != nil || item.Root != nil || item.Package != nil
}

// modified returns the modification time that clients set,
// which Touch sets, or the one of the server if there is none.
func (item *driveItem) modified() time.Time {
	if item.FileSystemInfo != nil && !item.FileSystemInfo.LastModified.IsZero() {
		return item.FileSystemInfo.LastModified
	}
	return item.LastModified
}

// APIError is an error response of the Microsoft Graph API,
// or of a pre-authenticated URL of a download, an upload session
// or a copy monitor.
//
// The errors of the file system methods wrap it if the response doesn't
// translate to one of the error types of go-fs, like for an expired
// access token (401), a missing permission of the app (403 accessDenied
// is an fs.ErrPermission), or throttling that outlasted the retries (429).
type APIError struct {
	// Method and URL of the request. The URL of a pre-authenticated
	// request has only the scheme and host, because the rest of it
	// grants access to the file.
	Method, URL string

	// StatusCode is the HTTP status code of the response.
	StatusCode int

	// Code is the error code of the response, like "itemNotFound",
	// "nameAlreadyExists" or "resourceModified", empty if the body
	// had no Graph error.
	Code string

	// Message is the error message of the response.
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("onedrivefs: %s %s: %d %s", e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// apiErrorOf returns the APIError of err, or nil.
func apiErrorOf(err error) *APIError {
	e, _ := errors.AsType[*APIError](err)
	return e
}

// statusOf returns the HTTP status code of the APIError of err, or 0.
func statusOf(err error) int {
	if e := apiErrorOf(err); e != nil {
		return e.StatusCode
	}
	return 0
}

// redactURL returns the scheme and host of a pre-authenticated URL,
// whose path and query grant access to a file.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "(pre-authenticated URL)"
	}
	return u.Scheme + "://" + u.Host + "/..."
}

// isGraphURL reports whether rawURL is a URL of the Graph API,
// the only URLs that get the access token.
func (f *fileSystem) isGraphURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, f.graphURL+"/")
}

// do sends a request with body, which may be nil, and returns the
// response if its status is below 400. A redirect is returned as
// response, not followed. Only requests to the Graph API get the
// access token. Requests answered with 429 Too Many Requests or
// 503 Service Unavailable are repeated after the delay of their
// Retry-After header or an exponential backoff, up to maxRetries times.
// Other error responses are an *APIError.
func (f *fileSystem) do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*http.Response, error) {
	auth := f.isGraphURL(rawURL)
	errURL := rawURL
	if !auth {
		errURL = redactURL(rawURL)
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var reqBody io.Reader = http.NoBody
		if body != nil {
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
		if err != nil {
			return nil, fmt.Errorf("onedrivefs: %s %s: %w", method, errURL, err)
		}
		for name, values := range header {
			req.Header[name] = values
		}
		if auth {
			token, err := f.tokens.Token()
			if err != nil {
				return nil, fmt.Errorf("onedrivefs: getting an access token: %w", err)
			}
			token.SetAuthHeader(req)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			if urlErr, ok := errors.AsType[*url.Error](err); ok {
				urlErr.URL = errURL
			}
			return nil, err
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}
		apiErr := readAPIError(resp, method, errURL)
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable
		if !retry || attempt == maxRetries {
			return nil, apiErr
		}
		delay := f.retryDelay << attempt
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds >= 0 {
			delay = time.Duration(seconds) * time.Second
		}
		timer := time.NewTimer(min(delay, maxRetryDelay))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// readAPIError reads the error response resp and closes its body.
func readAPIError(resp *http.Response, method, errURL string) *APIError {
	defer resp.Body.Close()
	apiErr := &APIError{Method: method, URL: errURL, StatusCode: resp.StatusCode}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &body) == nil {
		apiErr.Code = body.Error.Code
		apiErr.Message = body.Error.Message
	}
	return apiErr
}

// doJSON sends a request with the JSON of in as body, if in is not nil,
// and decodes the JSON of the response into out, if out is not nil.
// It returns the response header.
func (f *fileSystem) doJSON(ctx context.Context, method, rawURL string, header http.Header, in, out any) (http.Header, error) {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return nil, err
		}
		header = header.Clone()
		if header == nil {
			header = http.Header{}
		}
		header.Set("Content-Type", "application/json")
	}
	resp, err := f.do(ctx, method, rawURL, header, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			errURL := rawURL
			if !f.isGraphURL(rawURL) {
				errURL = redactURL(rawURL)
			}
			return nil, fmt.Errorf("onedrivefs: decoding the response of %s %s: %w", method, errURL, err)
		}
	}
	return resp.Header, nil
}
