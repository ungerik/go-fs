package fs

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"io"
	iofs "io/fs"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemFile_Name(t *testing.T) {
	tests := []struct {
		FileName string
		want     string
	}{
		{FileName: "", want: ""},
		{FileName: "MyImage.jpeg", want: "MyImage.jpeg"},
		{FileName: "file", want: "file"},
		{FileName: "some/path/file.txt", want: "file.txt"},
		{FileName: "/some/path/file.txt", want: "file.txt"},              // leading slash, base unaffected
		{FileName: "some\\path\\file.txt", want: "some\\path\\file.txt"}, // backslash is a literal character
		// Directory names: a single trailing slash marks a directory and is ignored
		{FileName: "dir/", want: "dir"},
		{FileName: "my.dir/", want: "my.dir"},
		{FileName: "some/path/dir/", want: "dir"},
		{FileName: "/dir/", want: "dir"},
		{FileName: "/some/path/dir/", want: "dir"},
		{FileName: "/", want: ""}, // root has no name
		// Only a single trailing slash is ignored, a second one leaves an empty name
		{FileName: "some/path/dir//", want: ""},
		{FileName: "//", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.FileName, func(t *testing.T) {
			f := NewMemFile(tt.FileName, nil)
			if got := f.Name(); got != tt.want {
				t.Errorf("MemFile.Name() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemFile_Ext(t *testing.T) {
	tests := []struct {
		FileName string
		want     string
	}{
		{FileName: "", want: ""},
		{FileName: "My.Image.jpeg", want: ".jpeg"},
		{FileName: "some/path/file.txt", want: ".txt"},
		{FileName: "/some/path/file.txt", want: ".txt"}, // leading slash, ext unaffected
		{FileName: "some\\path\\file.txt", want: ".txt"},
		{FileName: "some/path/dir/", want: ""}, // trailing slash: no name, no ext
	}
	for _, tt := range tests {
		t.Run(tt.FileName, func(t *testing.T) {
			f := NewMemFile(tt.FileName, nil)
			if got := f.Ext(); got != tt.want {
				t.Errorf("MemFile.Name() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemFile_IsDir(t *testing.T) {
	// FileName ending with a slash is a directory, FileData is ignored
	require.True(t, MemFile{FileName: "dir/"}.IsDir())
	require.True(t, MemFile{FileName: "some/path/"}.IsDir())
	require.True(t, MemFile{FileName: "/"}.IsDir())
	require.True(t, MemFile{FileName: "dir/", FileData: []byte("ignored")}.IsDir())

	require.False(t, MemFile{FileName: ""}.IsDir())
	require.False(t, MemFile{FileName: "file.txt"}.IsDir())
	require.False(t, MemFile{FileName: "some/path/file.txt"}.IsDir())

	// CheckIsDir: nil for a directory, ErrEmptyPath for empty, ErrIsNotDirectory for a file
	require.NoError(t, MemFile{FileName: "dir/"}.CheckIsDir())
	require.ErrorIs(t, MemFile{FileName: ""}.CheckIsDir(), ErrEmptyPath)
	var errNotDir ErrIsNotDirectory
	require.ErrorAs(t, MemFile{FileName: "file.txt"}.CheckIsDir(), &errNotDir)

	// Stat reflects the directory mode
	info, err := MemFile{FileName: "dir/"}.Stat()
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.True(t, info.Mode().IsDir())
}

func TestMemFile_DirReadsFail(t *testing.T) {
	d := MemFile{FileName: "some/dir/", FileData: []byte("ignored")}
	ctx := t.Context()

	isDirErr := func(err error) {
		t.Helper()
		var errIsDir ErrIsDirectory
		require.ErrorAs(t, err, &errIsDir)
	}

	_, err := d.ReadAll(t.Context())
	isDirErr(err)
	_, err = d.ReadAll(ctx)
	isDirErr(err)
	_, _, err = d.ReadAllContentHash(ctx)
	isDirErr(err)
	_, err = d.ReadAllString(t.Context())
	isDirErr(err)
	_, err = d.ReadAllString(ctx)
	isDirErr(err)
	_, err = d.OpenReader()
	isDirErr(err)
	_, err = d.OpenReadSeeker()
	isDirErr(err)
	_, err = d.ReadAt(make([]byte, 4), 0)
	isDirErr(err)
	_, err = d.WriteTo(new(bytes.Buffer))
	isDirErr(err)
	isDirErr(d.ReadJSON(ctx, new(any)))
	isDirErr(d.ReadXML(ctx, new(any)))

	// ContentHash returns an empty string for a directory, matching File and MemDir
	hash, err := d.ContentHash(t.Context())
	require.NoError(t, err)
	require.Equal(t, "", hash)
	hash, err = d.ContentHash(ctx)
	require.NoError(t, err)
	require.Equal(t, "", hash)

	// A regular file still reads its FileData
	f := MemFile{FileName: "file.txt", FileData: []byte("hello")}
	data, err := f.ReadAll(t.Context())
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)
}

func TestMemFile_DirAndName(t *testing.T) {
	tests := []struct {
		FileName string
		wantDir  MemFile
		wantName string
	}{
		{FileName: "", wantDir: MemFile{}, wantName: ""},
		{FileName: "file.txt", wantDir: MemFile{}, wantName: "file.txt"},
		{FileName: "some/path/file.txt", wantDir: MemFile{FileName: "some/path/"}, wantName: "file.txt"},
		{FileName: "/some/path/file.txt", wantDir: MemFile{FileName: "/some/path/"}, wantName: "file.txt"},
		{FileName: "some\\path\\file.txt", wantDir: MemFile{}, wantName: "some\\path\\file.txt"}, // backslash is a literal character
		{FileName: "/file.txt", wantDir: MemFile{FileName: "/"}, wantName: "file.txt"},
		{FileName: "/", wantDir: MemFile{FileName: "/"}, wantName: ""}, // root is its own parent
		// Regression: a directory returned itself as its parent
		{FileName: "some/path/dir/", wantDir: MemFile{FileName: "some/path/"}, wantName: "dir"},
		{FileName: "/dir/", wantDir: MemFile{FileName: "/"}, wantName: "dir"},
		{FileName: "dir/", wantDir: MemFile{}, wantName: "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.FileName, func(t *testing.T) {
			f := NewMemFile(tt.FileName, nil)
			dir, name := f.DirAndName()
			require.Equal(t, tt.wantDir, dir)
			require.Equal(t, tt.wantName, name)
		})
	}
}

func TestMemFile_Dir(t *testing.T) {
	tests := []struct {
		FileName string
		want     MemFile
	}{
		{FileName: "", want: MemFile{}},
		{FileName: "file.txt", want: MemFile{}},                                 // no slash
		{FileName: "some/path/file.txt", want: MemFile{FileName: "some/path/"}}, // parent dir, keeps trailing slash
		{FileName: "some\\path\\file.txt", want: MemFile{}},                     // backslash is a literal character, no slash
		{FileName: "/file.txt", want: MemFile{FileName: "/"}},                   // root
		{FileName: "/", want: MemFile{FileName: "/"}},                           // root is its own parent
		{FileName: "/some/file.txt", want: MemFile{FileName: "/some/"}},         // absolute parent dir
		{FileName: "some/path/dir/", want: MemFile{FileName: "some/path/"}},     // regression: a dir returned itself
		{FileName: "/dir/", want: MemFile{FileName: "/"}},                       // dir in root
		{FileName: "dir/", want: MemFile{}},                                     // dir without parent
	}
	for _, tt := range tests {
		t.Run(tt.FileName, func(t *testing.T) {
			f := NewMemFile(tt.FileName, nil)
			require.Equal(t, tt.want, f.Dir())
			// The Dir result has nil FileData and is itself a directory
			require.Nil(t, f.Dir().FileData)
			if tt.want.FileName != "" {
				require.True(t, f.Dir().IsDir())
			}
		})
	}
}

func TestMemFile_CleanPath(t *testing.T) {
	tests := []struct {
		FileName string
		want     string
	}{
		{FileName: "", want: ""},
		{FileName: "file.txt", want: "file.txt"},
		{FileName: "a/b/../c", want: "a/c"},
		{FileName: "a/./b", want: "a/b"},
		{FileName: "./a", want: "a"},
		{FileName: "a/b/", want: "a/b"},                // trailing slash removed
		{FileName: "a/b///", want: "a/b"},              // trailing slashes removed
		{FileName: "a//b", want: "a/b"},                // redundant slash removed
		{FileName: "/a/../b", want: "/b"},              // leading slash kept
		{FileName: "a/b/../../c", want: "c"},           //
		{FileName: "foo/../..", want: ".."},            // may ascend above relative root
		{FileName: "/", want: ""},                      // trailing slash trimmed, even root
		{FileName: "/foo/../..", want: ""},             // ascends to root, then root slash trimmed
		{FileName: "a\\b\\..\\c", want: "a\\b\\..\\c"}, // backslash is literal: nothing to resolve
		{FileName: "a\\b\\", want: "a\\b\\"},           // backslash is literal: not a trailing separator
		{FileName: "a/b\\..\\c", want: "a/b\\..\\c"},   // only '/' splits, so '\' stays literal
	}
	for _, tt := range tests {
		t.Run(tt.FileName, func(t *testing.T) {
			f := NewMemFile(tt.FileName, nil)
			if got := f.CleanPath(); got != tt.want {
				t.Errorf("MemFile.CleanPath() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMemFile_MarshalJSON(t *testing.T) {
	tests := []struct {
		memFile MemFile
		want    []byte
	}{
		{
			memFile: MemFile{},
			want:    []byte(`{"filename":""}`),
		},
		{
			memFile: NewMemFile("no data", nil),
			want:    []byte(`{"filename":"no data"}`),
		},
		{
			memFile: NewMemFile("hello.txt", []byte(`Hello World!`)),
			want:    []byte(`{"filename":"hello.txt","data":"SGVsbG8gV29ybGQh"}`),
		},
	}
	for _, tt := range tests {
		got, err := json.Marshal(tt.memFile)
		require.NoError(t, err, "json.Marshal")
		require.Equal(t, string(tt.want), string(got))
	}
}

func TestMemFile_GobEncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		memFile MemFile
	}{
		{
			name:    "basic test",
			memFile: MemFile{FileName: "hello.txt", FileData: []byte(`Hello World!`)},
		},
		{
			name:    "nil data",
			memFile: MemFile{FileName: "hello.txt", FileData: nil},
		},
		{
			name:    "no name, no data",
			memFile: MemFile{FileName: "", FileData: nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test pass by value
			{
				buf := bytes.NewBuffer(nil)
				err := gob.NewEncoder(buf).Encode(tt.memFile)
				require.NoError(t, err, "gob.Encoder.Encode")
				require.NotEmpty(t, buf.Bytes())

				var out MemFile
				err = gob.NewDecoder(buf).Decode(&out)
				require.NoError(t, err, "gob.Decoder.Decode")
				require.Equal(t, tt.memFile, out)
			}
			// Test pass by pointer
			{
				buf := bytes.NewBuffer(nil)
				err := gob.NewEncoder(buf).Encode(&tt.memFile)
				require.NoError(t, err, "gob.Encoder.Encode")
				require.NotEmpty(t, buf.Bytes())

				var out *MemFile
				err = gob.NewDecoder(buf).Decode(&out)
				require.NoError(t, err, "gob.Decoder.Decode")
				require.Equal(t, &tt.memFile, out)
			}
		})
	}
}

// TestMemFile_ReadAt is a regression test: ReadAt panicked for a negative
// offset and returned a custom error instead of io.EOF when fewer than
// len(p) bytes were available, which broke io.SectionReader.
func TestMemFile_ReadAt(t *testing.T) {
	f := NewMemFile("x", []byte("hello"))

	buf := make([]byte, 2)
	require.NotPanics(t, func() {
		_, err := f.ReadAt(buf, -1)
		require.ErrorIs(t, err, iofs.ErrInvalid, "negative offset")
	})

	n, err := f.ReadAt(buf, 1)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, "el", string(buf))

	n, err = f.ReadAt(buf, 3)
	require.NoError(t, err, "all requested bytes up to the end")
	require.Equal(t, 2, n)
	require.Equal(t, "lo", string(buf))

	n, err = f.ReadAt(buf, 4)
	require.ErrorIs(t, err, io.EOF, "fewer bytes than requested")
	require.Equal(t, 1, n)
	require.Equal(t, "o", string(buf[:n]))

	n, err = f.ReadAt(buf, 5)
	require.ErrorIs(t, err, io.EOF, "offset at the end")
	require.Equal(t, 0, n)

	data, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<20))
	require.NoError(t, err, "io.SectionReader needs io.EOF at the end")
	require.Equal(t, "hello", string(data))
}

// TestMemFile_Stat is a regression test: the ModTime of Stat was time.Now()
// instead of the zero time, so a MemFile looked modified on every call.
func TestMemFile_Stat(t *testing.T) {
	info, err := NewMemFile("a.txt", []byte("abc")).Stat()
	require.NoError(t, err)
	require.True(t, info.ModTime().IsZero(), "ModTime must be the zero time")
	require.Equal(t, "a.txt", info.Name())
	require.Equal(t, int64(3), info.Size())
	require.True(t, info.Mode().IsRegular())

	require.False(t, NewMemFile("a.txt", nil).IsSymbolicLink())
	require.False(t, NewMemFile("dir/", nil).IsSymbolicLink())
}

func TestMemFile_WriteJSON(t *testing.T) {
	input := map[string]int{"a": 1}

	var f MemFile
	require.NoError(t, f.WriteJSON(t.Context(), input))
	require.Equal(t, `{"a":1}`, string(f.FileData))

	require.NoError(t, f.WriteJSONIndent(t.Context(), input, "  "))
	require.Equal(t, "{\n  \"a\": 1\n}", string(f.FileData))

	// An empty indent still starts a new line for every element
	require.NoError(t, f.WriteJSONIndent(t.Context(), input, ""))
	require.Equal(t, "{\n\"a\": 1\n}", string(f.FileData))

	err := f.WriteJSON(t.Context(), make(chan int))
	require.ErrorIs(t, err, ErrMarshalJSON)
	require.ErrorIs(t, f.WriteJSONIndent(t.Context(), make(chan int), "  "), ErrMarshalJSON)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, f.WriteJSON(ctx, input), context.Canceled)
	require.ErrorIs(t, f.WriteJSONIndent(ctx, input, "  "), context.Canceled)

	f, err = NewMemFileWriteJSON("a.json", input)
	require.NoError(t, err)
	require.Equal(t, MemFile{FileName: "a.json", FileData: []byte(`{"a":1}`)}, f)

	f, err = NewMemFileWriteJSONIndent("a.json", input, "\t")
	require.NoError(t, err)
	require.Equal(t, "{\n\t\"a\": 1\n}", string(f.FileData))

	f, err = NewMemFileWriteJSONIndent("a.json", input, "")
	require.NoError(t, err)
	require.Equal(t, "{\n\"a\": 1\n}", string(f.FileData))

	_, err = NewMemFileWriteJSON("a.json", make(chan int))
	require.ErrorIs(t, err, ErrMarshalJSON)
	_, err = NewMemFileWriteJSONIndent("a.json", make(chan int), "\t")
	require.ErrorIs(t, err, ErrMarshalJSON)
}

func TestMemFile_WriteXML(t *testing.T) {
	type doc struct {
		XMLName xml.Name `xml:"doc"`
		A       int      `xml:"a"`
	}
	input := doc{A: 1}

	var f MemFile
	require.NoError(t, f.WriteXML(t.Context(), input))
	require.Equal(t, xml.Header+"<doc><a>1</a></doc>", string(f.FileData))

	require.NoError(t, f.WriteXMLIndent(t.Context(), input, "  "))
	require.Equal(t, xml.Header+"<doc>\n  <a>1</a>\n</doc>", string(f.FileData))

	// An empty indent still starts a new line for every element
	require.NoError(t, f.WriteXMLIndent(t.Context(), input, ""))
	require.Equal(t, xml.Header+"<doc>\n<a>1</a>\n</doc>", string(f.FileData))

	err := f.WriteXML(t.Context(), make(chan int))
	require.ErrorIs(t, err, ErrMarshalXML)
	require.ErrorIs(t, f.WriteXMLIndent(t.Context(), make(chan int), "  "), ErrMarshalXML)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, f.WriteXML(ctx, input), context.Canceled)
	require.ErrorIs(t, f.WriteXMLIndent(ctx, input, "  "), context.Canceled)

	f, err = NewMemFileWriteXML("a.xml", input)
	require.NoError(t, err)
	require.Equal(t, MemFile{FileName: "a.xml", FileData: []byte(xml.Header + "<doc><a>1</a></doc>")}, f)

	f, err = NewMemFileWriteXMLIndent("a.xml", input, "\t")
	require.NoError(t, err)
	require.Equal(t, xml.Header+"<doc>\n\t<a>1</a>\n</doc>", string(f.FileData))

	f, err = NewMemFileWriteXMLIndent("a.xml", input, "")
	require.NoError(t, err)
	require.Equal(t, xml.Header+"<doc>\n<a>1</a>\n</doc>", string(f.FileData))

	_, err = NewMemFileWriteXML("a.xml", make(chan int))
	require.ErrorIs(t, err, ErrMarshalXML)
	_, err = NewMemFileWriteXMLIndent("a.xml", make(chan int), "\t")
	require.ErrorIs(t, err, ErrMarshalXML)
}

func TestReadMultipartFormMemFiles(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, file := range []struct{ field, name, data string }{
		{"docs", "a.txt", "aaa"},
		{"docs", "b.txt", "bbb"},
		{"image", "c.png", "ccc"},
	} {
		part, err := w.CreateFormFile(file.field, file.name)
		require.NoError(t, err)
		_, err = part.Write([]byte(file.data))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	// A maxMemory of 1 byte stores the files in temp files on disk,
	// which ReadMultipartFormMemFiles opens and closes one at a time
	form, err := multipart.NewReader(&body, w.Boundary()).ReadForm(1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = form.RemoveAll() })

	memFiles, err := ReadMultipartFormMemFiles(t.Context(), form)
	require.NoError(t, err)
	require.Equal(t, map[string][]MemFile{
		"docs": {
			NewMemFile("a.txt", []byte("aaa")),
			NewMemFile("b.txt", []byte("bbb")),
		},
		"image": {NewMemFile("c.png", []byte("ccc"))},
	}, memFiles)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = ReadMultipartFormMemFiles(ctx, form)
	require.ErrorIs(t, err, context.Canceled)
}
