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

// unicodePathExtraID is the ID of the Info-ZIP Unicode Path extra field,
// which holds the UTF-8 version of a name that is stored in another encoding.
const unicodePathExtraID = 0x7075

// cp437 holds the characters of the bytes 0x80 to 0xFF of code page 437,
// the encoding of zip entry names without the UTF-8 flag.
// The bytes below 0x80 are ASCII.
var cp437 = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■\u00a0")

// Check returns an error wrapping os.ErrInvalid if name,
// the slash separated name of a zip entry without the trailing slash
// of a directory, is one that archive/zip writes although zip tools
// read it wrong or the archive gets corrupt:
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
func Check(name string, isDir bool) error {
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
	case strings.ContainsFunc(name, isFormatRune):
		return fmt.Errorf("%w: zip entry name %.200q contains a control or formatting character", os.ErrInvalid, name)
	case strings.Contains(name, ":"):
		// fs.UnzipToMemFiles rejects it on Windows
		return fmt.Errorf("%w: zip entry name %.200q contains a colon, which marks a drive letter on Windows", os.ErrInvalid, name)
	}
	return nil
}

// isFormatRune reports if r is a control character, a line or paragraph
// separator, or a bidirectional embedding, override or isolate.
// The left-to-right and right-to-left marks are not, they are part
// of names in right-to-left scripts.
func isFormatRune(r rune) bool {
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
// A name that is not marked as UTF-8 (see zip.FileHeader.NonUTF8)
// is taken from its Info-ZIP Unicode Path extra field, if it has one
// for the stored name. Else a name that is valid UTF-8 is returned as it
// is, because many zip tools write UTF-8 without the UTF-8 flag, and
// any other name is decoded from CP-437, the encoding of the zip format
// for names without the UTF-8 flag.
func Decode(header *zip.FileHeader) string {
	name := header.Name
	if header.NonUTF8 {
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
			if id == unicodePathExtraID && len(field) > 5 && field[0] == 1 &&
				binary.LittleEndian.Uint32(field[1:]) == crc32.ChecksumIEEE([]byte(name)) &&
				utf8.Valid(field[5:]) {
				return string(field[5:])
			}
		}
	}
	if utf8.ValidString(name) {
		return name
	}
	var decoded strings.Builder
	decoded.Grow(2 * len(name))
	for i := range len(name) {
		if c := name[i]; c < 0x80 {
			decoded.WriteByte(c)
		} else {
			decoded.WriteRune(cp437[c-0x80])
		}
	}
	return decoded.String()
}
