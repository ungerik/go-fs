package fsimpl

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
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
		data, err := io.ReadAll(r.Body)
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
			got, err := DropboxContentHash(t.Context(), tt.args.reader)
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

// chunkReader returns data in fixed-size chunks without io.EOF on the final
// read with data, exercising a legal io.Reader that does not fill the whole
// block in a single Read call.
type chunkReader struct {
	data      []byte
	pos       int
	chunkSize int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:min(r.pos+r.chunkSize, len(r.data))])
	r.pos += n
	return n, nil
}

// TestDropboxContentHash_ChunkingReader verifies that the hash does not depend
// on how the reader chunks its output. A reader returning fewer than one block
// per Read must yield the same hash as a single buffer of the same bytes. This
// guards against the io.ReadFull fix regressing back to a direct reader.Read
// that split blocks at the wrong boundaries.
func TestDropboxContentHash_ChunkingReader(t *testing.T) {
	// More than two 4MB blocks plus a partial last block.
	data := make([]byte, 2*hashBlockSize+12345)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}

	want, err := DropboxContentHash(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("reference hash error: %s", err)
	}

	for _, chunkSize := range []int{1, 7, 1000, hashBlockSize - 1, hashBlockSize + 1} {
		t.Run("chunk_"+strconv.Itoa(chunkSize), func(t *testing.T) {
			r := &chunkReader{data: data, chunkSize: chunkSize}
			got, err := DropboxContentHash(t.Context(), r)
			if err != nil {
				t.Fatalf("DropboxContentHash() error = %s", err)
			}
			if got != want {
				t.Errorf("DropboxContentHash() with chunkSize %d = %s, want %s", chunkSize, got, want)
			}
		})
	}
}
