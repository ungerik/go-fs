package fstest

import (
	"os"
	"slices"
	"strings"
	"testing"

	fs "github.com/ungerik/go-fs"
)

// validNames are names that every file system can write,
// so ValidateName must accept them.
var validNames = []string{
	"plain.txt",
	"space name.txt",
	"dash-under_score.txt",
	"Größe.txt",
}

// nameProbes are names that some file systems can't write, or write to
// another name, like Windows and its file shares, object stores or archive
// formats, see testValidateName.
var nameProbes = []string{
	`back\slash.txt`,
	"co:lon.txt",
	"what?.txt",
	"star*.txt",
	`quote".txt`,
	"pipe|.txt",
	"angle<>.txt",
	"trailing.",
	"trailing ",
	"ctrl\x01.txt",
	"tab\t.txt",
	"line\nbreak.txt",
	"nul\x00.txt",
	"\xff.txt",
	"evil\u202etxt.exe",
	"CON",
	"nul.txt",
}

// testValidateName checks the ValidateName method of a file system that
// implements fs.ValidateNameFileSystem: the names that every file system
// writes are valid, the error for every other name wraps os.ErrInvalid,
// a copy to such a name fails with it before it writes anything, and every
// valid name can be written and is listed with that name. So ValidateName
// must reject every name that the file system writes to another entry, like
// a name that a server truncates at a NUL character, or doesn't write.
// A write-only file system gets no writes, it couldn't remove the files.
func (c *conformance) testValidateName(t *testing.T) {
	v := c.fs.(fs.ValidateNameFileSystem)
	for _, name := range validNames {
		checkNoError(t, v.ValidateName(name), "ValidateName(%q) of a name that every file system writes", name)
	}
	var valid, invalid []string
	for _, name := range slices.Concat(validNames, nameProbes) {
		// ValidateName gets the name of one directory entry
		if strings.Contains(name, "/") || strings.Contains(name, c.fs.Separator()) {
			continue
		}
		if err := v.ValidateName(name); err != nil {
			checkErrorIs(t, err, os.ErrInvalid, "ValidateName(%q)", name)
			invalid = append(invalid, name)
		} else {
			valid = append(valid, name)
		}
	}

	c.run(t, "Rejected", func(t *testing.T) {
		for _, name := range invalid {
			err := fs.CopyFile(c.ctx, fs.NewMemFile(name, []byte(name)), c.wfile("validate-name-rejected/"+name))
			checkErrorIs(t, err, os.ErrInvalid, "CopyFile to the name %q that ValidateName rejects", name)
		}
		if c.readable {
			c.checkMissing(t, "validate-name-rejected", "CopyFile to names that ValidateName rejects must not write anything")
		}
	})

	if !c.readable {
		return
	}
	c.run(t, "Accepted", func(t *testing.T) {
		c.mkdir(t, "validate-name")
		for _, name := range valid {
			err := c.writeErr(c.wpath("validate-name/"+name), []byte(name))
			checkNoError(t, err, "writing the name %q that ValidateName accepts", name)
		}
		for _, name := range valid {
			c.checkContent(t, "validate-name/"+name, name, "the name %q that ValidateName accepts", name)
		}
		var listed []string
		err := c.fs.ListDir(c.ctx, c.wpath("validate-name"), nil, func(info *fs.FileInfo) error {
			listed = append(listed, info.Name)
			return nil
		})
		mustNoError(t, err, "ListDir of the names that ValidateName accepts")
		slices.Sort(listed)
		want := slices.Sorted(slices.Values(valid))
		checkStrings(t, want, listed, "ListDir must list the names that ValidateName accepts as they were written")
	})
}
