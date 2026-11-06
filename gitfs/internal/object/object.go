// Package object implements the git object model that gitfs needs:
// object IDs of the sha1 and sha256 object formats, the hashing of
// objects and the parsing of commits, annotated tags and trees.
package object

import (
	"bytes"
	"crypto/sha1" //#nosec G505 -- git object IDs of the sha1 object format are SHA-1 hashes
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"time"
)

// Type is the type of a git object as stored in a packfile.
type Type int

// Object types of packfiles, see gitformat-pack(5).
const (
	TypeCommit   Type = 1
	TypeTree     Type = 2
	TypeBlob     Type = 3
	TypeTag      Type = 4
	TypeOfsDelta Type = 6
	TypeRefDelta Type = 7
)

// String returns the name of the type used in object headers,
// like "commit" or "blob".
func (t Type) String() string {
	switch t {
	case TypeCommit:
		return "commit"
	case TypeTree:
		return "tree"
	case TypeBlob:
		return "blob"
	case TypeTag:
		return "tag"
	case TypeOfsDelta:
		return "ofs-delta"
	case TypeRefDelta:
		return "ref-delta"
	}
	return "type(" + strconv.Itoa(int(t)) + ")"
}

// OID is the lowercase hex encoded ID of a git object.
type OID string

// Format is an object format of git, the hash function of object IDs.
type Format struct {
	// Name is the name used by the protocol, "sha1" or "sha256".
	Name string
	// Size is the size of a raw object ID in bytes.
	Size int
	// New returns a new hash of the format.
	New func() hash.Hash
}

// The object formats of git.
var (
	SHA1   = Format{Name: "sha1", Size: sha1.Size, New: sha1.New}
	SHA256 = Format{Name: "sha256", Size: sha256.Size, New: sha256.New}
)

// FormatByName returns the object format with the protocol name,
// SHA1 for an empty name, which a server sends for the default.
func FormatByName(name string) (Format, error) {
	switch name {
	case "", SHA1.Name:
		return SHA1, nil
	case SHA256.Name:
		return SHA256, nil
	}
	return Format{}, fmt.Errorf("unsupported git object format %q", name)
}

// ParseOID returns the OID of a hex string of the format,
// accepting upper case hex digits.
func (f Format) ParseOID(s string) (OID, error) {
	if len(s) != f.Size*2 {
		return "", fmt.Errorf("invalid %s object ID %.100q", f.Name, s)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("invalid %s object ID %.100q", f.Name, s)
	}
	return OID(hex.EncodeToString(b)), nil
}

// IsOID reports whether s is a hex object ID of the format.
func (f Format) IsOID(s string) bool {
	_, err := f.ParseOID(s)
	return err == nil
}

// RawOID returns the OID of a raw binary object ID.
func RawOID(raw []byte) OID {
	return OID(hex.EncodeToString(raw))
}

// Raw returns the raw binary object ID.
func (oid OID) Raw() ([]byte, error) {
	return hex.DecodeString(string(oid))
}

// Hash returns the OID of an object, the hash of its
// header "<type> <size>\0" followed by its data.
func (f Format) Hash(t Type, data []byte) OID {
	h := f.New()
	_, _ = fmt.Fprintf(h, "%s %d\x00", t, len(data))
	_, _ = h.Write(data)
	return RawOID(h.Sum(nil))
}

// Commit holds the fields of a commit that gitfs uses.
type Commit struct {
	Tree      OID
	Committed time.Time // time of the committer line
}

// ParseCommit parses the header of a commit object.
func (f Format) ParseCommit(data []byte) (*Commit, error) {
	var commit Commit
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(line) == 0 {
			break // end of the header, the message follows
		}
		key, value, _ := bytes.Cut(line, []byte{' '})
		switch string(key) {
		case "tree":
			if commit.Tree != "" {
				return nil, errors.New("commit has more than one tree")
			}
			tree, err := f.ParseOID(string(value))
			if err != nil {
				return nil, fmt.Errorf("commit tree: %w", err)
			}
			commit.Tree = tree
		case "committer":
			committed, err := parseSignatureTime(value)
			if err != nil {
				return nil, fmt.Errorf("commit committer: %w", err)
			}
			commit.Committed = committed
		}
	}
	if commit.Tree == "" {
		return nil, errors.New("commit has no tree")
	}
	return &commit, nil
}

// parseSignatureTime parses the time of a signature like
// "Name <email> 1793019269 +0100".
func parseSignatureTime(signature []byte) (time.Time, error) {
	end := bytes.LastIndexByte(signature, '>')
	if end < 0 {
		return time.Time{}, fmt.Errorf("invalid signature %.100q", signature)
	}
	fields := bytes.Fields(signature[end+1:])
	if len(fields) != 2 {
		return time.Time{}, fmt.Errorf("invalid signature time %.100q", signature[end+1:])
	}
	seconds, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid signature time %.100q", fields[0])
	}
	zone := fields[1]
	if len(zone) != 5 || (zone[0] != '+' && zone[0] != '-') {
		return time.Time{}, fmt.Errorf("invalid signature time zone %.100q", zone)
	}
	hours, errH := strconv.Atoi(string(zone[1:3]))
	minutes, errM := strconv.Atoi(string(zone[3:5]))
	if errH != nil || errM != nil {
		return time.Time{}, fmt.Errorf("invalid signature time zone %.100q", zone)
	}
	offset := hours*3600 + minutes*60
	if zone[0] == '-' {
		offset = -offset
	}
	return time.Unix(seconds, 0).In(time.FixedZone(string(zone), offset)), nil
}

// Tag holds the fields of an annotated tag that gitfs uses.
type Tag struct {
	Object OID
	Type   string // type of the tagged object, like "commit"
}

// ParseTag parses the header of an annotated tag object.
func (f Format) ParseTag(data []byte) (*Tag, error) {
	var tag Tag
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(line) == 0 {
			break
		}
		key, value, _ := bytes.Cut(line, []byte{' '})
		switch string(key) {
		case "object":
			object, err := f.ParseOID(string(value))
			if err != nil {
				return nil, fmt.Errorf("tag object: %w", err)
			}
			tag.Object = object
		case "type":
			tag.Type = string(value)
		}
	}
	if tag.Object == "" || tag.Type == "" {
		return nil, errors.New("tag has no object or type")
	}
	return &tag, nil
}

// Modes of tree entries.
const (
	ModeDir        = 0o040000
	ModeFile       = 0o100644
	ModeExecutable = 0o100755
	ModeSymlink    = 0o120000
	ModeSubmodule  = 0o160000
)

// TreeEntry is an entry of a tree object.
type TreeEntry struct {
	Mode uint32
	Name string // raw bytes of the name, not validated
	OID  OID
}

// ParseTree parses the entries of a tree object:
// "<octal mode> <name>\0<raw object ID>" each.
func (f Format) ParseTree(data []byte) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space <= 0 {
			return nil, errors.New("invalid tree entry mode")
		}
		mode, err := strconv.ParseUint(string(data[:space]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid tree entry mode %.20q", data[:space])
		}
		data = data[space+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 0 {
			return nil, errors.New("tree entry name is not terminated")
		}
		name := string(data[:nul])
		data = data[nul+1:]
		if len(data) < f.Size {
			return nil, errors.New("tree entry object ID is truncated")
		}
		entries = append(entries, TreeEntry{Mode: uint32(mode), Name: name, OID: RawOID(data[:f.Size])})
		data = data[f.Size:]
	}
	return entries, nil
}
