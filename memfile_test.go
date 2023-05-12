package fs

import "testing"

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
