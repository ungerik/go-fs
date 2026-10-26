package zipname

import (
	"archive/zip"
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// TestValidateName verifies the names that are not valid in a zip archive,
// up to the length limit that counts the trailing slash of a directory.
func TestValidateName(t *testing.T) {
	maxName := strings.Repeat("a", math.MaxUint16)
	for _, name := range []string{
		"a.txt",
		"dir/Größe ü.txt",
		"dir/..a",
		"dir/\u200fname.txt", // right-to-left mark
		maxName,
	} {
		assert.NoError(t, ValidateName(name, false), "%.20q", name)
	}
	assert.NoError(t, ValidateName(maxName[1:], true), "a directory of the maximum length")

	for _, name := range []string{
		"\xff.txt",
		"a\x00b",
		"a\tb",
		"a\u0085b",
		"a\u2028b",          // line separator
		"a\u2029b",          // paragraph separator
		"a\u202ab",          // left-to-right embedding
		"evil\u202etxt.exe", // right-to-left override shows "evilexe.txt"
		"a\u2066b",          // left-to-right isolate
		"a\u2069b",          // pop directional isolate
		"C:x",
		"dir/a:b",
		maxName + "a",
	} {
		assert.ErrorIs(t, ValidateName(name, false), os.ErrInvalid, "%.20q", name)
	}
	assert.ErrorIs(t, ValidateName(maxName, true), os.ErrInvalid, "the slash of a directory is too much")

	// The error message doesn't grow with the name
	for _, name := range []string{
		strings.Repeat("\x00", 60000),
		strings.Repeat("\xff", 60000),
		strings.Repeat(":", 60000),
		strings.Repeat("\x00", 1<<20),
	} {
		err := ValidateName(name, false)
		assert.ErrorIs(t, err, os.ErrInvalid)
		assert.Less(t, len(err.Error()), 1000, "error of a %d byte name", len(name))
	}
}

// unicodePathExtra returns an Info-ZIP Unicode Path extra field
// for the stored name with the UTF-8 name.
func unicodePathExtra(stored, utf8Name string) []byte {
	field := []byte{1}
	field = binary.LittleEndian.AppendUint32(field, crc32.ChecksumIEEE([]byte(stored)))
	field = append(field, utf8Name...)
	extra := binary.LittleEndian.AppendUint16(nil, unicodePathExtraID)
	extra = binary.LittleEndian.AppendUint16(extra, uint16(len(field)))
	return append(extra, field...)
}

// withByte returns a copy of b with the byte at i set to c.
func withByte(b []byte, i int, c byte) []byte {
	b = append([]byte(nil), b...)
	b[i] = c
	return b
}

// TestDecode verifies that entry names are returned as UTF-8:
// archive/zip returns the stored bytes, which are CP-437
// for names without the UTF-8 flag.
func TestDecode(t *testing.T) {
	assert.Len(t, cp437, 128)

	for _, tc := range []struct {
		desc   string
		header zip.FileHeader
		want   string
	}{
		{"ASCII", zip.FileHeader{Name: "dir/a.txt"}, "dir/a.txt"},
		{"UTF-8 flag", zip.FileHeader{Name: "Größe.txt", Flags: utf8Flag}, "Größe.txt"},
		{"UTF-8 without the flag", zip.FileHeader{Name: "Größe.txt", NonUTF8: true}, "Größe.txt"},
		{"CP-437", zip.FileHeader{Name: "dir/M\x81ller \x84\xe1.pdf", NonUTF8: true}, "dir/Müller äß.pdf"},
		{"CP-437 box and space", zip.FileHeader{Name: "\xb3\xff", NonUTF8: true}, "│\u00a0"},
		{"CP-437 first byte", zip.FileHeader{Name: "\x80", NonUTF8: true}, "Ç"},
		{
			"Unicode Path field",
			zip.FileHeader{Name: "\x93\x9a.txt", NonUTF8: true, Extra: unicodePathExtra("\x93\x9a.txt", "日本.txt")},
			"日本.txt",
		},
		{
			"Unicode Path field after another field",
			zip.FileHeader{Name: "\x93.txt", NonUTF8: true, Extra: append([]byte{0x55, 0x54, 1, 0, 0}, unicodePathExtra("\x93.txt", "日.txt")...)},
			"日.txt",
		},
		{
			// Tools replace characters their code page lacks with ASCII
			"Unicode Path field of an ASCII name",
			zip.FileHeader{Name: "__.txt", Extra: unicodePathExtra("__.txt", "日本.txt")},
			"日本.txt",
		},
		{
			"Unicode Path field of a directory",
			zip.FileHeader{Name: "\x84/", NonUTF8: true, Extra: unicodePathExtra("\x84/", "日/")},
			"日/",
		},
		{
			"Unicode Path field before a valid UTF-8 name without the flag",
			zip.FileHeader{Name: "Größe.txt", NonUTF8: true, Extra: unicodePathExtra("Größe.txt", "日.txt")},
			"日.txt",
		},
		{
			"UTF-8 flag ignores the Unicode Path field",
			zip.FileHeader{Name: "Größe.txt", Flags: utf8Flag, Extra: unicodePathExtra("Größe.txt", "日.txt")},
			"Größe.txt",
		},
		{
			// archive/zip sets NonUTF8 for a comment that is not UTF-8
			"UTF-8 flag and a comment that is not UTF-8",
			zip.FileHeader{Name: "report.pdf", Flags: utf8Flag, NonUTF8: true, Extra: unicodePathExtra("report.pdf", "report.exe")},
			"report.pdf",
		},
		{
			"UTF-8 flag of a name that is not UTF-8",
			zip.FileHeader{Name: "\x84.txt", Flags: utf8Flag, NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "日.txt")},
			"日.txt",
		},
		{
			"Unicode Path field that makes a directory a file",
			zip.FileHeader{Name: "\x84/", NonUTF8: true, Extra: unicodePathExtra("\x84/", "日")},
			"ä/",
		},
		{
			"Unicode Path field that makes a file a directory",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "dir/")},
			"ä.txt",
		},
		{
			"Unicode Path field with invalid UTF-8",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "\xff.txt")},
			"ä.txt",
		},
		{
			"Unicode Path field without a name",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "")},
			"ä.txt",
		},
		{
			"Unicode Path field of version 2",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: withByte(unicodePathExtra("\x84.txt", "日.txt"), 4, 2)},
			"ä.txt",
		},
		{
			"Unicode Comment field",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: withByte(unicodePathExtra("\x84.txt", "日.txt"), 1, 0x63)},
			"ä.txt",
		},
		{
			"extra field shorter than its header",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: []byte{0x75, 0x70, 0}},
			"ä.txt",
		},
		{
			// Regression: Decode hashed the stored name for every field,
			// so thousands of fields for a long name took 0.1 s per entry
			"only the first Unicode Path field",
			zip.FileHeader{
				Name:    "\x84.txt",
				NonUTF8: true,
				Extra:   append(unicodePathExtra("other.txt", "x.txt"), unicodePathExtra("\x84.txt", "日.txt")...),
			},
			"ä.txt",
		},
		{
			// The second byte of a Shift-JIS character can be ASCII
			"Unicode Path field of a double-byte code page",
			zip.FileHeader{Name: "\x83A.txt", NonUTF8: true, Extra: unicodePathExtra("\x83A.txt", "ア.txt")},
			"ア.txt",
		},
		{
			"Unicode Path field with substitutes between ASCII parts",
			zip.FileHeader{Name: "a_b?c.txt", Extra: unicodePathExtra("a_b?c.txt", "a日b本c.txt")},
			"a日b本c.txt",
		},
		{
			// Regression: the field renamed an entry that archive/zip
			// and the zip tools that ignore the field show differently
			"Unicode Path field that changes an ASCII name",
			zip.FileHeader{Name: "photo.jpg", Extra: unicodePathExtra("photo.jpg", "photo.html")},
			"photo.jpg",
		},
		{
			"Unicode Path field that changes the extension",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "日.exe")},
			"ä.txt",
		},
		{
			"Unicode Path field that adds a directory",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "日/x.txt")},
			"ä.txt",
		},
		{
			"Unicode Path field with a character for no stored byte",
			zip.FileHeader{Name: "ab.txt", Extra: unicodePathExtra("ab.txt", "a日b.txt")},
			"ab.txt",
		},
		{
			// The ASCII start of the field, like a directory, must start
			// the stored name
			"Unicode Path field that changes the ASCII start",
			zip.FileHeader{Name: "ab\x84.txt", NonUTF8: true, Extra: unicodePathExtra("ab\x84.txt", "x/日.txt")},
			"abä.txt",
		},
		{
			"Unicode Path field that changes an ASCII part between other characters",
			zip.FileHeader{Name: "a_b?c.txt", Extra: unicodePathExtra("a_b?c.txt", "a日z本c.txt")},
			"a_b?c.txt",
		},
		{
			"Unicode Path field of a name without an extension",
			zip.FileHeader{Name: "\x83A\x83C", NonUTF8: true, Extra: unicodePathExtra("\x83A\x83C", "アイ")},
			"アイ",
		},
		{
			"Unicode Path field that appends a character for no stored byte",
			zip.FileHeader{Name: "ab", Extra: unicodePathExtra("ab", "ab日")},
			"ab",
		},
		{
			"Unicode Path field of another name",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("other.txt", "日本.txt")},
			"ä.txt",
		},
		{
			"truncated extra field",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "日本.txt")[:8]},
			"ä.txt",
		},
		{
			// Regression: keepsASCII sliced the stored name past its end, so
			// a crafted entry panicked UnzipToMemFiles and the zipfs reader
			"Unicode Path field with characters after the end of the stored name",
			zip.FileHeader{Name: "ab", Extra: unicodePathExtra("ab", "ab日c本")},
			"ab",
		},
		{
			// Decode documents that the field can't give the entry another
			// directory than archive/zip shows
			"Unicode Path field that removes a directory",
			zip.FileHeader{Name: "a/\x84.txt", NonUTF8: true, Extra: unicodePathExtra("a/\x84.txt", "a日.txt")},
			"a/ä.txt",
		},
		{
			// zipfs and zip tools on Windows take the backslash for a separator
			"Unicode Path field that removes a backslash",
			zip.FileHeader{Name: `a\b.txt`, NonUTF8: true, Extra: unicodePathExtra(`a\b.txt`, "a日b.txt")},
			`a\b.txt`,
		},
		{
			// The second byte of "表" in Shift-JIS is a backslash,
			// which readers of other code pages take for a separator
			"Unicode Path field of a double-byte character with a backslash",
			zip.FileHeader{Name: "\x95\\.txt", NonUTF8: true, Extra: unicodePathExtra("\x95\\.txt", "表.txt")},
			"ò\\.txt",
		},
		{
			// Regression guard: the ASCII end of the field overlaps
			// its ASCII start, which used up the stored name
			"Unicode Path field whose ASCII end overlaps its start",
			zip.FileHeader{Name: "a.txt", Extra: unicodePathExtra("a.txt", "a.txt日.txt")},
			"a.txt",
		},
		{
			"Unicode Path field that removes an extension",
			zip.FileHeader{Name: "\x84.exe", NonUTF8: true, Extra: unicodePathExtra("\x84.exe", "日")},
			"ä.exe",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.want, Decode(&tc.header))
		})
	}
}

// FuzzKeepsASCII verifies that keepsASCII never panics and that a Unicode
// Path name it accepts has the slashes and dots of the stored name, so it
// can't name another directory or extension.
// Regression: it sliced the stored name past its end for a field whose
// ASCII parts used up the stored name.
func FuzzKeepsASCII(f *testing.F) {
	f.Add("ab", "ab日c本")
	f.Add("a/\x84.txt", "a日.txt")
	f.Add("\x83A.txt", "ア.txt")
	f.Add("a_b?c.txt", "a日b本c.txt")
	f.Add("ab", "ab日b")
	f.Add(`a\b.txt`, "a日b.txt")
	f.Fuzz(func(t *testing.T, stored, unicodeName string) {
		if !utf8.ValidString(unicodeName) || !keepsASCII(stored, unicodeName) {
			return
		}
		for _, c := range []string{"/", "."} {
			if strings.Count(stored, c) != strings.Count(unicodeName, c) {
				t.Errorf("keepsASCII(%q, %q) accepted another number of %q", stored, unicodeName, c)
			}
		}
	})
}
