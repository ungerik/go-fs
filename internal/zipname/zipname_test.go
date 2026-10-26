package zipname

import (
	"archive/zip"
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCheck verifies the names that are not valid in a zip archive,
// up to the length limit that counts the trailing slash of a directory.
func TestCheck(t *testing.T) {
	maxName := strings.Repeat("a", math.MaxUint16)
	for _, name := range []string{
		"a.txt",
		"dir/Größe ü.txt",
		"dir/..a",
		"dir/‏name.txt", // right-to-left mark
		maxName,
	} {
		assert.NoError(t, Check(name, false), "%.20q", name)
	}
	assert.NoError(t, Check(maxName[1:], true), "a directory of the maximum length")

	for _, name := range []string{
		"\xff.txt",
		"a\x00b",
		"a\tb",
		"a\u0085b",
		"a b",          // line separator
		"evil‮txt.exe", // right-to-left override shows "evilexe.txt"
		"a⁦b",          // left-to-right isolate
		"C:x",
		"dir/a:b",
		maxName + "a",
	} {
		assert.ErrorIs(t, Check(name, false), os.ErrInvalid, "%.20q", name)
	}
	assert.ErrorIs(t, Check(maxName, true), os.ErrInvalid, "the slash of a directory is too much")

	// The error message doesn't grow with the name
	for _, name := range []string{strings.Repeat("\x00", 60000), strings.Repeat("\x00", 1<<20)} {
		err := Check(name, false)
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
		{"UTF-8 flag", zip.FileHeader{Name: "Größe.txt"}, "Größe.txt"},
		{"UTF-8 without the flag", zip.FileHeader{Name: "Größe.txt", NonUTF8: true}, "Größe.txt"},
		{"CP-437", zip.FileHeader{Name: "dir/M\x81ller \x84\xe1.pdf", NonUTF8: true}, "dir/Müller äß.pdf"},
		{"CP-437 box and space", zip.FileHeader{Name: "\xb3\xff", NonUTF8: true}, "│ "},
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
			"Unicode Path field of another name",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("other.txt", "日本.txt")},
			"ä.txt",
		},
		{
			"truncated extra field",
			zip.FileHeader{Name: "\x84.txt", NonUTF8: true, Extra: unicodePathExtra("\x84.txt", "日本.txt")[:8]},
			"ä.txt",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.want, Decode(&tc.header))
		})
	}
}
