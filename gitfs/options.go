package gitfs

import (
	"net/http"
)

const (
	// DefaultCacheSize is the default of Options.CacheSize.
	DefaultCacheSize = 64 << 20
	// DefaultMaxObjectSize is the default of Options.MaxObjectSize.
	DefaultMaxObjectSize = 1 << 30
	// DefaultMaxFetchSize is the default of Options.MaxFetchSize.
	DefaultMaxFetchSize = 1 << 30
)

// Options for New. The zero value and nil use the defaults.
type Options struct {
	// HTTPClient is used for all requests, http.DefaultClient if nil.
	HTTPClient *http.Client

	// Username and Password authenticate the git requests with HTTP Basic
	// authentication. Credentials in the user info of the repository URL
	// are used if Username is empty.
	Username string
	Password string

	// Token is an access token, like a GitHub or GitLab personal access
	// token. It is sent as the password of HTTP Basic authentication
	// with the username "x-access-token" (or Username, if set) to the git
	// server, and as bearer token to the GitHub REST API and
	// raw.githubusercontent.com.
	Token string

	// DisableGitHub turns off the GitHub shortcuts for repositories on
	// github.com, see the package documentation: everything is then read
	// through the git protocol, also for github.com.
	DisableGitHub bool

	// CacheSize is the maximum number of bytes of file content kept in
	// memory, DefaultCacheSize if zero. Larger files are not cached.
	CacheSize int64

	// MaxObjectSize is the maximum size of a git object,
	// DefaultMaxObjectSize if zero. Reading a larger file fails.
	MaxObjectSize int64

	// MaxFetchSize is the maximum sum of the sizes of the git objects of
	// one response of the git server, which bounds the memory it uses,
	// DefaultMaxFetchSize if zero, and at least MaxObjectSize. A server
	// without the filter feature sends all files of the commit when its
	// first path is accessed, which fails for a commit whose files are
	// larger in total.
	MaxFetchSize int64

	// github replaces the endpoints of the GitHub shortcuts
	// for tests, also for repositories that are not on github.com.
	github *github
}
