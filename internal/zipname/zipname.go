// Package zipname implements the rules for the entry names of zip archives
// that fs.Zip, fs.UnzipToMemFiles and the zipfs package share.
package zipname

import (
	"archive/zip"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// unicodePathExtraID is the ID of the Info-ZIP Unicode Path extra field,
	// which holds the UTF-8 version of a name that is stored in another encoding.
	unicodePathExtraID = 0x7075

	// utf8Flag is the bit of zip.FileHeader.Flags
	// that marks the name and comment as UTF-8.
	utf8Flag = 0x800
)

// cp437 holds the characters of the bytes 0x80 to 0xFF of code page 437,
// the encoding of zip entry names without the UTF-8 flag.
// The bytes below 0x80 are ASCII.
var cp437 = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00a0")

// ValidateName returns an error wrapping os.ErrInvalid if name,
// the slash separated name of a zip entry without the trailing slash
// of a directory, is one that zip tools read wrong or that corrupts
// the archive. archive/zip writes all of them, except a name that is
// too long, which it rejects after adding it to the central directory:
//   - a name longer than the 65535 bytes of a zip header,
//     which also corrupts the central directory of the archive
//   - a name that is not valid UTF-8, which zip readers decode as CP-437
//   - a name with a control character like NUL, a line or paragraph
//     separator, or a bidirectional embedding, override or isolate,
//     which can disguise the extension of a name
//   - a name with a colon, which marks a drive letter on Windows
//
// Names that only Windows can't extract are not checked, like device
// names such as CON, names ending in a dot or a space, names with one
// of the characters < > " | ? * and names that differ only in case.
// The path rules, like no ".." elements, are checked separately.
func ValidateName(name string, isDir bool) error {
	size := len(name)
	if isDir {
		size++ // for the trailing slash
	}
	// The length is checked first and the other error messages
	// quote at most 200 characters, so they stay short
	switch {
	case size > math.MaxUint16:
		return fmt.Errorf("%w: zip entry name of %d bytes is longer than the %d bytes of a zip header", os.ErrInvalid, size, math.MaxUint16)
	case !utf8.ValidString(name):
		// archive/zip writes it without the UTF-8 flag
		return fmt.Errorf("%w: zip entry name %.200q is not valid UTF-8", os.ErrInvalid, name)
	case strings.ContainsFunc(name, isDisguisingRune):
		return fmt.Errorf("%w: zip entry name %.200q contains a control or formatting character", os.ErrInvalid, name)
	case strings.Contains(name, ":"):
		// fs.UnzipToMemFiles rejects it on Windows
		return fmt.Errorf("%w: zip entry name %.200q contains a colon, which marks a drive letter on Windows", os.ErrInvalid, name)
	}
	return nil
}

// isDisguisingRune reports if r can disguise a name when it is shown:
// a control character, a line or paragraph separator, or a bidirectional
// embedding, override or isolate. The left-to-right and right-to-left
// marks are not, they are part of names in right-to-left scripts.
func isDisguisingRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == '\u2028', r == '\u2029':
		return true
	case '\u202a' <= r && r <= '\u202e', '\u2066' <= r && r <= '\u2069':
		return true
	}
	return false
}

// Decode returns the name of the zip entry of header as UTF-8.
// archive/zip returns the stored bytes of a name without converting them.
//
// A name without the UTF-8 flag of the zip format, or that is not
// valid UTF-8, is taken from its Info-ZIP Unicode Path extra field
// if it has one for the stored name that agrees on being a directory,
// like zip tools do, and only differs from the stored name in the
// characters that its encoding can't hold, see keepsASCII. So the field
// can't give the entry another extension or directory than archive/zip
// and the zip tools that ignore the field show. header.Name keeps the
// stored name.
// Else a name that is valid UTF-8 is returned as it is, because many
// zip tools write UTF-8 without the UTF-8 flag, and any other name
// is decoded from CP-437, the encoding of the zip format for names
// without the UTF-8 flag.
func Decode(header *zip.FileHeader) string {
	name := header.Name
	validUTF8 := utf8.ValidString(name)
	if header.Flags&utf8Flag == 0 || !validUTF8 {
		// Fields of a 2 byte ID and a 2 byte size,
		// a Unicode Path field holds the version 1,
		// the CRC-32 of the stored name and the UTF-8 name
		extra := header.Extra
		for len(extra) >= 4 {
			id := binary.LittleEndian.Uint16(extra)
			size := int(binary.LittleEndian.Uint16(extra[2:]))
			if size > len(extra)-4 {
				break
			}
			field := extra[4 : 4+size]
			extra = extra[4+size:]
			if id != unicodePathExtraID {
				continue
			}
			if len(field) > 5 && field[0] == 1 && utf8.Valid(field[5:]) &&
				(field[len(field)-1] == '/') == strings.HasSuffix(name, "/") &&
				binary.LittleEndian.Uint32(field[1:]) == crc32.ChecksumIEEE([]byte(name)) {
				if unicodeName := string(field[5:]); keepsASCII(name, unicodeName) {
					return unicodeName
				}
			}
			// Zip tools write one Unicode Path field per entry.
			// Only the first counts, so a long name is hashed once
			// and not for each of thousands of fields
			break
		}
	}
	if validUTF8 {
		return name
	}
	// The characters of the bytes from 0x80 take two or three bytes in UTF-8
	size := len(name)
	for i := range len(name) {
		if c := name[i]; c >= 0x80 {
			size += utf8.RuneLen(cp437[c-0x80]) - 1
		}
	}
	var decoded strings.Builder
	decoded.Grow(size)
	for i := range len(name) {
		if c := name[i]; c < 0x80 {
			decoded.WriteByte(c)
		} else {
			decoded.WriteRune(cp437[c-0x80])
		}
	}
	return decoded.String()
}

// keepsASCII reports if unicodeName, the name of a Unicode Path field,
// only differs from stored, the stored name, in the characters that the
// encoding of stored can't hold: the ASCII parts of unicodeName must be
// the ones of stored in the same order, and every run of other characters
// stands for at least one byte of stored that is no slash, no dot and no
// backslash, which zip readers take for a separator, like a character of
// another code page or a substitute like "_". A backslash can only be the
// second byte of a double-byte character, after a byte from 0x80, like in
// "表" of Shift-JIS. So a run can't hide a directory or an extension.
func keepsASCII(stored, unicodeName string) bool {
	isASCII := func(r rune) bool { return r < utf8.RuneSelf }
	isOther := func(r rune) bool { return r >= utf8.RuneSelf }
	isRunBytes := func(b string) bool {
		if b == "" || strings.ContainsAny(b, "/.") {
			return false
		}
		for i := range len(b) {
			if b[i] == '\\' && (i == 0 || b[i-1] < utf8.RuneSelf) {
				return false
			}
		}
		return true
	}
	i := strings.IndexFunc(unicodeName, isOther)
	if i < 0 {
		return unicodeName == stored
	}
	// The ASCII part before the first other character starts stored
	if !strings.HasPrefix(stored, unicodeName[:i]) {
		return false
	}
	pos, rest := i, unicodeName[i:]
	for {
		// rest starts with a run of other characters
		i = strings.IndexFunc(rest, isASCII)
		if i < 0 {
			return isRunBytes(stored[pos:])
		}
		rest = rest[i:]
		i = strings.IndexFunc(rest, isOther)
		if i < 0 {
			// The ASCII part after the last other character ends stored
			end := len(stored) - len(rest)
			return end > pos && strings.HasSuffix(stored, rest) && isRunBytes(stored[pos:end])
		}
		if pos >= len(stored) {
			return false
		}
		// The leftmost match leaves the most bytes for the parts after it
		// and the fewest for the run before it
		at := strings.Index(stored[pos+1:], rest[:i])
		if at < 0 || !isRunBytes(stored[pos:pos+1+at]) {
			return false
		}
		pos += 1 + at + i
		rest = rest[i:]
	}
}
