package fsimpl

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net/http"
	"testing"
)

func TestDropboxContentHash(t *testing.T) {
	download := func(url string) []byte {
		r, err := http.DefaultClient.Get(url)
		if err != nil {
			t.Fatalf("download(%s) error: %s", url, err)
		}
		if r.StatusCode < 200 || r.StatusCode > 299 {
			t.Fatalf("download(%s) response status %d: %s", url, r.StatusCode, r.Status)
		}
		defer r.Body.Close()
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("download(%s) error: %s", url, err)
		}
		return data
	}

	type args struct {
		reader io.Reader
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "milky-way-nasa.jpg",
			args:    args{reader: bytes.NewBuffer(download("https://www.dropbox.com/static/images/developers/milky-way-nasa.jpg"))},
			want:    "485291fa0ee50c016982abbfa943957bcd231aae0492ccbaa22c58e3997b35e0",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DropboxContentHash(context.Background(), tt.args.reader)
			if (err != nil) != tt.wantErr {
				t.Errorf("DropboxContentHash() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("DropboxContentHash() = %v, want %v", got, tt.want)
			}
		})
	}
}
