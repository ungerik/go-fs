package fsimpl

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
)

// TestDropboxContentHash_KnownAnswers checks the hash of generated inputs
// against known answers that were computed with an independent implementation
// of the documented algorithm: the SHA-256 of the concatenated SHA-256 hashes
// of the 4 MiB blocks of the content. The sizes cover no block, a partial
// block, exactly one full block and a full block followed by a partial one.
func TestDropboxContentHash_KnownAnswers(t *testing.T) {
	// generate returns n bytes that differ between the blocks
	generate := func(n int) []byte {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i % 251)
		}
		return data
	}
	for _, tt := range []struct {
		size int
		want string
	}{
		{size: 0, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{size: 1, want: "1406e05881e299367766d313e26c05564ec91bf721d31726bd6e46e60689539a"},
		{size: hashBlockSize, want: "b9654428408015906b44a00935b70af33830aa344b780b0eabd535a133150d04"},
		{size: hashBlockSize + 1, want: "4a6cc0a344febaa07772e7c974834b2fb1d24594d4ba15f27c97a54699709f44"},
	} {
		t.Run(strconv.Itoa(tt.size), func(t *testing.T) {
			got, err := DropboxContentHash(t.Context(), bytes.NewReader(generate(tt.size)))
			if err != nil {
				t.Fatalf("DropboxContentHash() error = %s", err)
			}
			if got != tt.want {
				t.Errorf("DropboxContentHash() of %d bytes = %s, want %s", tt.size, got, tt.want)
			}
		})
	}
}

// TestDropboxContentHash checks the hash of the example file
// of the Dropbox documentation, which is downloaded from www.dropbox.com.
func TestDropboxContentHash(t *testing.T) {
	if os.Getenv("GOFS_ONLINE_TESTS") == "" {
		t.Skip("set GOFS_ONLINE_TESTS=1 to download the example file from www.dropbox.com")
	}
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
