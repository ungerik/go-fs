package fs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// modeInfo is an os.FileInfo that only has a file mode.
type modeInfo os.FileMode

func (m modeInfo) Name() string       { return "file" }
func (m modeInfo) Size() int64        { return 0 }
func (m modeInfo) Mode() os.FileMode  { return os.FileMode(m) }
func (m modeInfo) ModTime() time.Time { return time.Time{} }
func (m modeInfo) IsDir() bool        { return os.FileMode(m).IsDir() }
func (m modeInfo) Sys() any           { return nil }

// TestIsLocalSpecialFile is a regression test: a copy refused to replace
// every local file that os.Lstat doesn't describe as regular, but on Windows
// os.Lstat sets os.ModeIrregular for every reparse point that is no symbolic
// link, like the placeholders of OneDrive files, so CopyRecursive and Move
// failed over them. Only a device, a named pipe and a socket, which the copy
// would write into, must be refused, an irregular file is written like
// a regular one.
func TestIsLocalSpecialFile(t *testing.T) {
	tests := []struct {
		mode    os.FileMode
		special bool
	}{
		{0o644, false},
		{os.ModeIrregular | 0o644, false},
		{os.ModeNamedPipe | 0o600, true},
		{os.ModeSocket | 0o755, true},
		{os.ModeDevice | 0o660, true},
		{os.ModeDevice | os.ModeCharDevice | 0o666, true},
		{os.ModeCharDevice, true},
	}
	for _, tt := range tests {
		require.Equal(t, tt.special, isLocalSpecialFile(modeInfo(tt.mode)), "mode %s", tt.mode)
	}
}

// TestIsValidWindowsName is a regression test: on Windows the copy of
// a name with a character that Windows refuses in names, like the S3 key
// "report?.pdf", failed at that file, after the copy had written the files
// before it, because isValidLocalName only rejected the names that
// filepath.Localize rejects and the names that end with a dot or a space.
// isValidWindowsName doesn't depend on the platform, so its names are
// checked everywhere, TestCopyRecursive_WindowsInvalidNames checks the copies
// on Windows.
func TestIsValidWindowsName(t *testing.T) {
	valid := []string{
		"a.txt", "report.pdf", ".hidden", "a b.txt", "a..b", "ü.txt",
		"a#b", "a%b", "a&b", "a'b", "a+b,c;d=e", "[a]{b}", "a~b", "a\x7fb",
	}
	for _, name := range valid {
		require.True(t, isValidWindowsName(name), "%q", name)
	}
	invalid := []string{
		"report?.pdf", "a<b", "a>b", `a"b`, "a|b", "a*b", "*",
		"a\x01b", "a\x1fb", "\ttab", "line\n", "a\x00b",
		"dot.", "...", "space ", "dot. ",
	}
	for _, name := range invalid {
		require.False(t, isValidWindowsName(name), "%q", name)
	}
}

// TestIsSpecialFile checks that isSpecialFile takes the file mode of the
// local path for a local file and IsRegular of the FileInfo for other file
// systems, which have no file mode of their own.
func TestIsSpecialFile(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("data"), 0o600))
	// IsRegular false like for a reparse point on Windows: the mode tells
	require.False(t, isSpecialFile(File(localPath), &FileInfo{IsRegular: false}), "a regular local file")

	mem, err := RegisteredTestMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	file := mem.RootDir().Join("file.txt")
	require.True(t, isSpecialFile(file, &FileInfo{IsRegular: false}), "no regular file of another file system")
	require.False(t, isSpecialFile(file, &FileInfo{IsRegular: true}), "a regular file of another file system")
}

// TestIsSpecialFile_OverlayUpperNotLocal is a regression test: for an
// OverlayFileSystem whose upper layer is not local, isSpecialFile took the
// FileInfo of the overlay, which describes the entry of the base layer if the
// upper layer has none, so a copy refused a special file of the base layer,
// although the write creates a new file in the upper layer. The entry of the
// upper layer must tell, a missing one is no special file.
func TestIsSpecialFile_OverlayUpperNotLocal(t *testing.T) {
	upper, err := NewMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upper.Close() })
	overlay, err := NewOverlayFileSystem(Local, upper, "")
	require.NoError(t, err)
	require.NoError(t, Register(overlay))
	t.Cleanup(func() { Unregister(overlay) })
	dest := overlay.RootDir().Join("file.txt")
	// The FileInfo of a special file of the base layer
	special := &FileInfo{IsRegular: false}

	require.False(t, isSpecialFile(dest, special), "a file missing in the upper layer")
	require.NoError(t, upper.WriteAll(t.Context(), "/file.txt", []byte("data"), 0o600))
	require.False(t, isSpecialFile(dest, special), "a regular file of the upper layer")
}

// TestNewDestDir is a regression test: a move between file systems checks
// that its destination is missing, then lists the whole source, see
// treeCopier.checkSource, and only then copies into the destination. Without
// the move flag, a destination directory that another process created in
// between was taken for an existing directory, whose entries the copy
// replaced before the move removed the source. Every directory of a move must
// be new, so that every existing entry is an error, see destDir.
func TestNewDestDir(t *testing.T) {
	statErrs := []error{nil, os.ErrNotExist, os.ErrPermission}
	for _, statErr := range statErrs {
		for _, localDest := range []bool{false, true} {
			move := &treeCopier{localDest: localDest, move: true}
			require.True(t, move.newDestDir(statErr).isNew, "a directory of a move, Stat error %v, local %t", statErr, localDest)
		}
	}

	copyLocal := &treeCopier{localDest: true}
	require.True(t, copyLocal.newDestDir(os.ErrNotExist).isNew, "a directory that the copy created")
	existing := copyLocal.newDestDir(nil)
	require.False(t, existing.isNew, "an existing local directory")
	require.NotNil(t, existing.written, "the writes into an existing local directory are recorded")

	copyRemote := &treeCopier{}
	existing = copyRemote.newDestDir(nil)
	require.False(t, existing.isNew, "an existing directory of another file system")
	require.Nil(t, existing.written, "other file systems have no identity of a file to record")
}

// TestEmptyPathIsNoTarget checks the same-target and into-itself checks
// behind the ErrEmptyPath of CopyRecursive, Move and moveByCopy for an empty
// path, like the one of a file URI without a path: the empty path names no
// file, so a copy to or from it is neither a copy onto itself nor into
// itself, although CleanPath of the local file system makes it ".", the
// working directory, filepath.Abs makes it the working directory too, and
// CleanPath of a rooted file system makes it the root.
func TestEmptyPathIsNoTarget(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	require.NoError(t, os.Mkdir(work, 0o700))
	t.Chdir(work)
	parent, root := filepath.Dir(work), Local.CleanPath(Separator)
	mem, err := NewMemFileSystem(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })

	require.False(t, isSameTarget(Local, "", Local, "."), "the empty path and the working directory")
	require.False(t, isSameTarget(Local, ".", Local, ""), "the working directory and the empty path")
	require.False(t, isSameTarget(Local, "", Local, ""), "two empty paths")
	require.False(t, isSameTarget(mem, "", mem, "/"), "the empty path and the root")
	require.False(t, isInsideDirView(mem, "", mem, "/x"), "a file in the root inside the empty path")
	require.False(t, isInsideDirView(Local, root, Local, ""), "the empty path inside the root")
	require.False(t, isLocalInsideDir(parent, ""), "the empty path inside the parent of the working directory")
	require.False(t, isCopyIntoItself(Local, parent, Local, parent, Local, ""), "a copy of the parent of the working directory to the empty path")

	// The paths that the empty path was taken for
	require.True(t, isSameTarget(Local, ".", Local, "d/.."), "the working directory")
	require.True(t, isInsideDirView(mem, "/", mem, "/x"), "a file in the root")
	require.True(t, isInsideDirView(Local, root, Local, "."), "the working directory inside the root")
	require.True(t, isLocalInsideDir(parent, "."), "the working directory inside its parent")
	require.True(t, isCopyIntoItself(Local, parent, Local, parent, Local, "."), "a copy of the parent of the working directory to it")
}
