package fsimpl

import (
	"net/url"
	"strings"
)

// redactedPassword replaces a password in RedactURI and RedactAddress.
const redactedPassword = "xxxxx"

// RedactURI returns uri for error messages and logs with the password
// of its user info replaced by "xxxxx", also if url.Parse rejects uri
// or misreads a password with an unencoded '/', '?' or '#'.
// A uri without "://" has no user info and is returned unchanged.
//
// The user info ends with the last '@' of the authority, which ends with
// the first '/', '?' or '#' after "://", and the password starts after the
// first ':' of the user info. A password with an unencoded '/', '?' or '#'
// ends the authority early and moves the end of its user info behind it.
// So if the last '@' of uri follows the authority, and the authority has
// a ':' that can start a password, outside of an IPv6 address in brackets,
// that '@' is taken for the end of the user info, decided in this order:
//
//   - It is if the host or port of the authority is invalid, like
//     "user:secret" of "ftp://user:secret/x@host/dir": the password
//     explains the error.
//   - It is not if the user info up to the '@' would be invalid even with
//     its '/', '?', '#' and '@' percent-encoded, like "host:22/a%zz" of
//     "sftp://host:22/a%zz@b".
//   - It is if the authority has no user info, like "u:123" of
//     "ftp://u:123/x@host/dir", which url.Parse takes for the host "u"
//     with the port "123" and the path "/x@host/dir". A username with
//     a password of digits can't be told apart from a host and port.
//   - It is if the authority has a user info with a password and url.Parse
//     rejects uri, like "s3://user:se@cr/et@host/b%zz", because the password
//     may contain an '@' and a '/'.
//
// Otherwise the '@' is part of the path, query or fragment, like in
// "sftp://user:secret@host/me@example.com", which is redacted as url.Parse
// parses it. That can redact more than the password, like the port and path
// of "http://localhost:8080/a@b", never less.
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
	if last > len(authority) && lastAtEndsUserInfo(uri, authority, rest[:last]) {
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

// lastAtEndsUserInfo reports whether the last '@' of uri ends its user info
// although it follows the authority, see RedactURI. userInfo is the part
// of uri between "://" and that '@'.
func lastAtEndsUserInfo(uri, authority, userInfo string) bool {
	user, hostPort := "", authority
	at := strings.LastIndexByte(authority, '@')
	if at >= 0 {
		user, hostPort = authority[:at], authority[at+1:]
	}
	userColon := strings.Contains(user, ":")
	portColon := strings.Contains(hostPort, ":") && !strings.HasPrefix(hostPort, "[")
	switch {
	case !userColon && !portColon:
		return false
	case !isValidHostPort(hostPort):
		return true
	case !isValidUserInfo(userInfo):
		return false
	case at < 0:
		return true
	}
	_, err := url.Parse(uri)
	return err != nil && userColon
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
// '/', '?' or '#'. A URI whose path may contain an '@', like the one of
// a file, is redacted with RedactURI.
//
// The username before the first ':' is kept unless it contains a '/', '?'
// or '#'. Then it can't be told apart from the host and path,
// and the whole user info is replaced.
func RedactAddress(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return address
	}
	start := 0
	if i := strings.Index(address[:at], "://"); i >= 0 {
		start = i + len("://")
	}
	username, _, hasPassword := strings.Cut(address[start:at], ":")
	switch {
	case strings.ContainsAny(username, "/?#"):
		return address[:start] + redactedPassword + address[at:]
	case hasPassword:
		return address[:start] + username + ":" + redactedPassword + address[at:]
	}
	return address
}
