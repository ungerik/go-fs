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
		{FileName: "some/path/file.txt", want: "file.txt"},
		{FileName: "some\\path\\file.txt", want: "file.txt"},
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
		{FileName: "some\\path\\file.txt", want: ".txt"},
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
