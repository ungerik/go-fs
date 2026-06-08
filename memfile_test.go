package fs

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
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

	_, err := d.ReadAll()
	isDirErr(err)
	_, err = d.ReadAllContext(ctx)
	isDirErr(err)
	_, _, err = d.ReadAllContentHash(ctx)
	isDirErr(err)
	_, err = d.ReadAllString()
	isDirErr(err)
	_, err = d.ReadAllStringContext(ctx)
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
	hash, err := d.ContentHash()
	require.NoError(t, err)
	require.Equal(t, "", hash)
	hash, err = d.ContentHashContext(ctx)
	require.NoError(t, err)
	require.Equal(t, "", hash)

	// A regular file still reads its FileData
	f := MemFile{FileName: "file.txt", FileData: []byte("hello")}
	data, err := f.ReadAll()
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
		{FileName: "some/path/dir/", wantDir: MemFile{FileName: "some/path/dir/"}, wantName: "dir"},
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
		{FileName: "some/path/dir/", want: MemFile{FileName: "some/path/dir/"}}, // already a dir, returns itself
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
