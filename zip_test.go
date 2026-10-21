package fs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	iofs "io/fs"
	"os"
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
