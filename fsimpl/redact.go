package fsimpl

import (
	"net/url"
	"strings"
)

// redactedPassword replaces a password in RedactURI and RedactAddress.
const redactedPassword = "xxxxx"

// RedactURI returns uri for error messages and logs with the password
// of its user info replaced by "xxxxx", also if url.Parse rejects uri
// or misreads a password with an unencoded '/', '?', '#' or '@'.
// A uri without "://" has no user info and is returned unchanged.
//
// The user info ends with the last '@' of the authority, which ends with
// the first '/', '?' or '#' after "://", and the password starts after the
// first ':' of the user info. A password with an unencoded '/', '?' or '#'
// ends the authority early and moves the end of its user info behind it.
// So if the last '@' of uri follows the authority, that '@' is taken for
// the end of the user info, decided in this order:
//
//   - It is if the user info of the authority has a password, like
//     "user:S3cr" of "sftp://user:S3cr@et/Pw@host/x", which url.Parse
//     takes for the user info of the host "et" with the path "/Pw@host/x":
//     the password may contain an '@' followed by a '/', '?' or '#'.
//     An '@' in the path, query or fragment of a URI with a password can't
//     be told apart from one of the password, so the host and the start of
//     the path are redacted too, like "sftp://user:xxxxx@example.com" for
//     "sftp://user:secret@host/me@example.com".
//   - It is not if the authority has no ':' that can start a password,
//     outside of an IPv6 address in brackets, like "user@host" of
//     "sftp://user@host/me@example.com".
//   - It is if the host or port of the authority is invalid or the port
//     is empty, like "user:secret" of "ftp://user:secret/x@host/dir" and
//     "user:" of "ftp://user:/%zz@host/dir": the password explains the
//     error or starts with a '/', '?' or '#'.
//   - It is not if the user info up to the '@' would be invalid even with
//     its '/', '?', '#' and '@' percent-encoded, like "host:22/a%zz" of
//     "sftp://host:22/a%zz@b".
//   - It is if the authority has no user info, like "u:123" of
//     "ftp://u:123/x@host/dir", which url.Parse takes for the host "u"
//     with the port "123" and the path "/x@host/dir". A username with
//     a password of digits can't be told apart from a host and port.
//
// Otherwise the '@' is part of the path, query or fragment, like in
// "ftp://alice@host:21/me@example.com". The rules can redact more than
// the password, like the host, port and path of "http://localhost:8080/a@b",
// so that error messages lose the host, never a password.
func RedactURI(uri string) string {
	_, rest, found := strings.Cut(uri, "://")
	if !found {
		return uri
	}
	last := strings.LastIndexByte(rest, '@')
	if last < 0 {
		return uri
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	end := strings.LastIndexByte(authority, '@')
	switch {
	case end >= 0 && strings.Contains(rest[:end], ":"):
		// The user info has a password, which can contain an '@'
		// followed by a '/', '?' or '#'
		end = last
	case last > len(authority) && lastAtEndsUserInfo(authority, rest[:last]):
		end = last
	}
	if end < 0 {
		return uri
	}
	start := len(uri) - len(rest)
	colon := strings.IndexByte(rest[:end], ':')
	if colon < 0 {
		return uri
	}
	return uri[:start+colon+1] + redactedPassword + rest[end:]
}

// lastAtEndsUserInfo reports whether the last '@' of a URI ends its user info
// although it follows the authority, which has no user info with a password,
// see RedactURI. userInfo is the part of the URI between "://" and that '@'.
func lastAtEndsUserInfo(authority, userInfo string) bool {
	hostPort := authority
	at := strings.LastIndexByte(authority, '@')
	if at >= 0 {
		hostPort = authority[at+1:]
	}
	switch {
	case !strings.Contains(hostPort, ":") || strings.HasPrefix(hostPort, "["):
		return false
	case !isValidHostPort(hostPort) || strings.HasSuffix(hostPort, ":"):
		// An empty port is the start of a password
		// that begins with a '/', '?' or '#'
		return true
	case !isValidUserInfo(userInfo):
		return false
	}
	return at < 0
}

// isValidHostPort reports whether url.Parse accepts hostPort
// as the host and port of an authority.
func isValidHostPort(hostPort string) bool {
	_, err := url.Parse("//" + hostPort)
	return err == nil
}

// userInfoEscaper percent-encodes the characters that end
// a user info or an authority.
var userInfoEscaper = strings.NewReplacer("/", "%2F", "?", "%3F", "#", "%23", "@", "%40")

// isValidUserInfo reports whether url.Parse accepts userInfo
// with its '/', '?', '#' and '@' percent-encoded.
func isValidUserInfo(userInfo string) bool {
	_, err := url.Parse("//" + userInfoEscaper.Replace(userInfo) + "@host")
	return err == nil
}

// RedactAddress returns address for error messages and logs with the
// password replaced by "xxxxx", taking everything up to the last '@'
// for the user info, after "://" if address has a scheme.
//
// It is meant for the address of a file system that is rejected because
// it has an '@' after the host, which can end a password with an unencoded
// '/', '?' or '#', or because it is no URL of the file system. A URI whose
// path may contain an '@', like the one of a file, is redacted with RedactURI.
//
// The first "://" ends a scheme only if it follows a valid scheme, a letter
// followed by letters, digits, '+', '-' or '.' as defined by RFC 3986.
// Else it is part of the password of an address without scheme,
// like "pa://ss" of "user:pa://ss@host".
//
// The username before the first ':' is kept unless it contains a '/', '?'
// or '#'. Then it can't be told apart from the host and path,
// and the whole user info is replaced.
//
// An address that is rejected because it doesn't have the scheme of the
// file system is redacted with RedactAddressWithoutScheme.
func RedactAddress(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return address
	}
	start := 0
	if i := strings.Index(address[:at], "://"); i >= 0 && isScheme(address[:i]) {
		start = i + len("://")
	}
	return redactUserInfo(address, start, at)
}

// RedactAddressWithoutScheme is RedactAddress for an address that is
// rejected because it doesn't have the scheme of the file system: it takes
// everything up to the last '@' for the user info, also a scheme before
// "://", and replaces the password from the first ':' to that '@'.
//
// The text before "://" can be a username with a password that starts with
// "//", like alice with the password "//S3cret" of the address without scheme
// "alice://S3cret@host", which RedactAddress takes for the scheme alice
// followed by the username S3cret, so it keeps the password. Such an address
// is redacted to "alice:xxxxx@host", a URI with another scheme loses its
// username, like "ftp:xxxxx@host" for "ftp://user:secret@host".
func RedactAddressWithoutScheme(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return address
	}
	return redactUserInfo(address, 0, at)
}

// redactUserInfo returns address with the password of the user info
// address[start:at] replaced, see RedactAddress.
func redactUserInfo(address string, start, at int) string {
	username, _, hasPassword := strings.Cut(address[start:at], ":")
	switch {
	case strings.ContainsAny(username, "/?#"):
		return address[:start] + redactedPassword + address[at:]
	case hasPassword:
		return address[:start] + username + ":" + redactedPassword + address[at:]
	}
	return address
}

// isScheme reports whether s is a URI scheme as defined by RFC 3986:
// a letter followed by letters, digits, '+', '-' or '.'.
func isScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}
