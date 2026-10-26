package fs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// zipEntryNamesOf returns the entry names of a zip archive.
func zipEntryNamesOf(t *testing.T, data []byte) []string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	names := make([]string, len(r.File))
	for i, f := range r.File {
		names[i] = f.Name
	}
	return names
}

// TestZip_EntryNames is a regression test: Zip named the entry of a MemFile
// after its Name and ZipMemFiles after its FileName, so the same MemFile
// got different entry names. Both now use the FileName of a MemFile.
func TestZip_EntryNames(t *testing.T) {
	memFS, err := RegisteredTestMemFileSystem(nil, NewMemFile("/sub/c.txt", []byte("c")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = memFS.Close() })
	fsFile := memFS.RootDir().Join("sub", "c.txt")
	fsDir := memFS.RootDir().Join("sub")

	memFiles := []MemFile{
		NewMemFile("dir/a.txt", []byte("a")),
		NewMemFile("/abs/b.txt", []byte("b")), // leading slash removed
		NewMemFile("emptydir/", nil),
	}
	wantMemFileNames := []string{"dir/a.txt", "abs/b.txt", "emptydir/"}

	zipped, err := Zip(t.Context(), AsFileReaders(memFiles)...)
	require.NoError(t, err)
	require.Equal(t, wantMemFileNames, zipEntryNamesOf(t, zipped), "Zip")

	zipped, err = ZipMemFiles(memFiles...)
	require.NoError(t, err)
	require.Equal(t, wantMemFileNames, zipEntryNamesOf(t, zipped), "ZipMemFiles")

	zipped, err = Zip(t.Context(), &memFiles[0], fsFile, fsDir)
	require.NoError(t, err)
	require.Equal(t, []string{"dir/a.txt", "c.txt", "sub/"}, zipEntryNamesOf(t, zipped),
		"a *MemFile is named like a MemFile, a File after its Name")

	unzipped, err := UnzipToMemFiles(t.Context(), NewMemFile("x.zip", zipped))
	require.NoError(t, err)
	require.Equal(t, []MemFile{
		NewMemFile("dir/a.txt", []byte("a")),
		NewMemFile("c.txt", []byte("c")),
	}, unzipped, "round trip without the directory entry")

	for _, invalid := range []MemFile{
		NewMemFile("", []byte("x")),
		NewMemFile("../evil.txt", []byte("x")),
		NewMemFile("a/../../evil.txt", []byte("x")),
	} {
		_, err = ZipMemFiles(invalid)
		require.Errorf(t, err, "invalid name %q", invalid.FileName)
	}
}

// TestZip_InsecureNames is a regression test: Zip only rejected empty names
// and names starting with "..", so it wrote archives with names like
// `dir\a.txt` or "/abs.txt" that UnzipToMemFiles rejects with
// zip.ErrInsecurePath. Zip now rejects every name that can't be unzipped.
func TestZip_InsecureNames(t *testing.T) {
	data := NewMemFile("data.txt", []byte("x"))
	for _, file := range []FileReader{
		NewMemFile(`dir\a.txt`, []byte("x")),
		NewMemFile(`..\evil.txt`, []byte("x")),
		FileReaderWithName(data, "/abs.txt"),
		FileReaderWithName(data, "a/../../evil.txt"),
		FileReaderWithName(data, `dir\a.txt`),
		FileReaderWithName(data, "."),
	} {
		t.Run(file.Name(), func(t *testing.T) {
			zipped, err := Zip(t.Context(), file)
			if err == nil {
				// Show what made the archive unusable
				_, err = UnzipToMemFiles(t.Context(), NewMemFile("x.zip", zipped))
				t.Fatalf("Zip wrote the entry %q, UnzipToMemFiles returns %v", zipEntryNamesOf(t, zipped), err)
			}
			require.ErrorIs(t, err, os.ErrInvalid)
		})
	}

	// Valid names survive the round trip
	files := []MemFile{
		NewMemFile("a.txt", []byte("a")),
		NewMemFile("dir/sub/b.txt", []byte("b")),
		NewMemFile("dir/./c..txt", []byte("c")),
	}
	zipped, err := ZipMemFiles(files...)
	require.NoError(t, err)
	unzipped, err := UnzipToMemFiles(t.Context(), NewMemFile("x.zip", zipped))
	require.NoError(t, err)
	require.Equal(t, []MemFile{
		NewMemFile("a.txt", []byte("a")),
		NewMemFile("dir/sub/b.txt", []byte("b")),
		NewMemFile("dir/c..txt", []byte("c")),
	}, unzipped)
}

// TestZip_InvalidNames is a regression test: archive/zip writes almost
// every name, so ZipMemFiles and Zip wrote entries with names that are not
// valid in a zip archive, like a NUL byte, invalid UTF-8 that zip readers
// decode as CP-437, or a drive letter that UnzipToMemFiles rejects on Windows.
func TestZip_InvalidNames(t *testing.T) {
	longName := strings.Repeat("a", math.MaxUint16)
	data := NewMemFile("data.txt", []byte("x"))
	for _, tc := range []struct {
		desc string
		file FileReader
	}{
		{"empty", NewMemFile("", []byte("x"))},
		{"root", NewMemFile("/", nil)},
		{"NUL", NewMemFile("a\x00b.txt", []byte("x"))},
		{"newline", NewMemFile("a\nb.txt", []byte("x"))},
		{"DEL", NewMemFile("a\x7fb.txt", []byte("x"))},
		{"invalid UTF-8", NewMemFile("dir/\xff.txt", []byte("x"))},
		{"drive letter", NewMemFile("C:x.txt", []byte("x"))},
		{"drive letter with slash", NewMemFile("C:/x.txt", []byte("x"))},
		{"colon", NewMemFile("dir/a:b.txt", []byte("x"))},
		{"too long", NewMemFile(longName+"a", []byte("x"))},
		{"too long with the slash of a directory", NewMemFile(longName+"/", nil)},
		{"FileReader with empty name", FileReaderWithName(data, "")},
		{"FileReader with trailing slash", FileReaderWithName(data, "dir/")},
		{"FileReader with empty element", FileReaderWithName(data, "a//b.txt")},
		{"FileReader with dot element", FileReaderWithName(data, "a/./b.txt")},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := Zip(t.Context(), tc.file)
			require.ErrorIs(t, err, os.ErrInvalid)
			if memFile, ok := tc.file.(MemFile); ok {
				_, err = ZipMemFiles(memFile)
				require.ErrorIs(t, err, os.ErrInvalid, "ZipMemFiles")
			}
		})
	}

	// Valid names up to the maximum length survive the round trip
	files := []MemFile{
		NewMemFile("dir/Größe ü.txt", []byte("a")),
		NewMemFile(longName, []byte("b")),
		NewMemFile(longName[1:]+"/", nil),
	}
	zipped, err := ZipMemFiles(files...)
	require.NoError(t, err)
	require.Equal(t, []string{"dir/Größe ü.txt", longName, longName[1:] + "/"}, zipEntryNamesOf(t, zipped))
	unzipped, err := UnzipToMemFiles(t.Context(), NewMemFile("x.zip", zipped))
	require.NoError(t, err)
	require.Equal(t, files[:2], unzipped)
}

// TestZip_Modified checks that Zip takes the modification time from Stat.
func TestZip_Modified(t *testing.T) {
	file := File(t.TempDir()).Join("a.txt")
	require.NoError(t, file.WriteAllString(t.Context(), "a"))
	modified := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	require.NoError(t, os.Chtimes(file.LocalPath(), modified, modified))

	zipped, err := Zip(t.Context(), file)
	require.NoError(t, err)
	r, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	require.NoError(t, err)
	require.Len(t, r.File, 1)
	require.True(t, modified.Equal(r.File[0].Modified), "Modified %s, want %s", r.File[0].Modified, modified)
}

// zipTestFileReader is a MemFile with a custom reader.
type zipTestFileReader struct {
	MemFile
	reader *zipTestReadCloser
}

func (f zipTestFileReader) OpenReader() (ReadCloser, error) {
	return f.reader, nil
}

// zipTestReadCloser calls read for every Read and records Close.
type zipTestReadCloser struct {
	read   func(p []byte) (int, error)
	closed bool
}

func (r *zipTestReadCloser) Read(p []byte) (int, error) { return r.read(p) }
func (r *zipTestReadCloser) Close() error               { r.closed = true; return nil }
func (r *zipTestReadCloser) Stat() (iofs.FileInfo, error) {
	return nil, errors.New("not implemented")
}

// TestZip_ReaderErrors is a regression test: Zip didn't close the reader
// of a file when copying it failed and ignored the context while copying.
func TestZip_ReaderErrors(t *testing.T) {
	t.Run("read error closes reader", func(t *testing.T) {
		errRead := errors.New("read failed")
		reader := &zipTestReadCloser{read: func([]byte) (int, error) { return 0, errRead }}
		_, err := Zip(t.Context(), zipTestFileReader{NewMemFile("a.txt", nil), reader})
		require.ErrorIs(t, err, errRead)
		require.True(t, reader.closed, "reader must be closed after a failed copy")
	})

	t.Run("canceled while copying", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reads := 0
		reader := &zipTestReadCloser{read: func(p []byte) (int, error) {
			reads++
			cancel()
			p[0] = 'x'
			return 1, nil // endless content
		}}
		_, err := Zip(ctx, zipTestFileReader{NewMemFile("a.txt", nil), reader})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, reads, "no read after the cancellation")
		require.True(t, reader.closed, "reader must be closed after a canceled copy")
	})
}

// TestUnzipToMemFiles_Entries is a regression test: UnzipToMemFiles returned
// empty MemFiles for directory entries.
func TestUnzipToMemFiles_Entries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	_, err := zw.Create("dir/")
	require.NoError(t, err)
	w, err := zw.Create("dir/a.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("a"))
	require.NoError(t, err)
	w, err = zw.Create("b.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("bb"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	files, err := UnzipToMemFiles(t.Context(), NewMemFile("x.zip", buf.Bytes()))
	require.NoError(t, err)
	require.Equal(t, []MemFile{
		NewMemFile("dir/a.txt", []byte("a")),
		NewMemFile("b.txt", []byte("bb")),
	}, files)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = UnzipToMemFiles(ctx, NewMemFile("x.zip", buf.Bytes()))
	require.ErrorIs(t, err, context.Canceled)
}

// TestUnzipToMemFiles_InsecureNames is a regression test: UnzipToMemFiles
// returned entries with names like "../evil.txt", which escape a directory
// when the MemFiles are written below it.
func TestUnzipToMemFiles_InsecureNames(t *testing.T) {
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/abs.txt", `dir\evil.txt`} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			w, err := zw.Create(name)
			require.NoError(t, err)
			_, err = w.Write([]byte("evil"))
			require.NoError(t, err)
			require.NoError(t, zw.Close())

			_, err = UnzipToMemFiles(t.Context(), NewMemFile("x.zip", buf.Bytes()))
			require.ErrorIs(t, err, zip.ErrInsecurePath)
		})
	}
}

// TestUnzipToMemFiles_MaxSize is a regression test: UnzipToMemFiles had
// no size limit, so a small zip bomb could exhaust the memory.
func TestUnzipToMemFiles_MaxSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"a.txt", "b.txt"} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte("123456"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zipFile := NewMemFile("x.zip", buf.Bytes())

	files, err := unzipToMemFiles(t.Context(), zipFile, 12)
	require.NoError(t, err, "exactly the maximum size")
	require.Len(t, files, 2)

	_, err = unzipToMemFiles(t.Context(), zipFile, 11)
	require.Error(t, err, "one byte more than the maximum size")

	// A header claiming more than UnzipToMemFilesMaxSize bytes
	// is rejected before reading anything
	buf.Reset()
	zw = zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "bomb.txt",
		Method:             zip.Store,
		CompressedSize64:   1,
		UncompressedSize64: UnzipToMemFilesMaxSize + 1,
	})
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	_, err = UnzipToMemFiles(t.Context(), NewMemFile("bomb.zip", buf.Bytes()))
	require.ErrorContains(t, err, "uncompressed bytes")
}
