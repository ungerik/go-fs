package pack

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/gitfs/internal/object"
)

// deltaSize encodes a size of a delta header.
func deltaSize(size int) []byte {
	var b []byte
	for {
		c := byte(size & 0x7f)
		size >>= 7
		if size == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

// deltaCopy encodes a copy instruction with all offset and size bytes.
func deltaCopy(offset, size int) []byte {
	return []byte{0xff,
		byte(offset), byte(offset >> 8), byte(offset >> 16), byte(offset >> 24),
		byte(size), byte(size >> 8), byte(size >> 16),
	}
}

// delta returns a delta from base to the concatenation of the parts,
// where a string part is inserted and an [2]int part copies a range.
func delta(baseSize, resultSize int, parts ...any) []byte {
	d := append(deltaSize(baseSize), deltaSize(resultSize)...)
	for _, part := range parts {
		switch p := part.(type) {
		case string:
			d = append(d, byte(len(p)))
			d = append(d, p...)
		case [2]int:
			d = append(d, deltaCopy(p[0], p[1])...)
		case []byte:
			d = append(d, p...)
		}
	}
	return d
}

func TestApplyDelta(t *testing.T) {
	base := []byte("Hello, World!")
	tests := []struct {
		name    string
		delta   []byte
		want    string
		wantErr string
	}{
		{name: "Copy", delta: delta(13, 5, [2]int{0, 5}), want: "Hello"},
		{name: "Insert", delta: delta(13, 3, "abc"), want: "abc"},
		{name: "Mixed", delta: delta(13, 11, [2]int{7, 5}, ", ", [2]int{0, 4}), want: "World, Hell"},
		{name: "ShortCopy", delta: delta(13, 2, []byte{0x91, 0x01, 0x02}), want: "el"}, // offset byte 0, size byte 0
		{name: "CopyOutOfBase", delta: delta(13, 5, [2]int{10, 5}), wantErr: "out of bounds"},
		{name: "CopyOverResult", delta: delta(13, 2, [2]int{0, 5}), wantErr: "out of bounds"},
		{name: "InsertOverResult", delta: delta(13, 2, "abc"), wantErr: "out of bounds"},
		{name: "InsertTruncated", delta: delta(13, 3, []byte{3, 'a'}), wantErr: "out of bounds"},
		{name: "CopyTruncated", delta: delta(13, 3, []byte{0x91}), wantErr: "truncated"},
		{name: "Reserved", delta: delta(13, 3, []byte{0}), wantErr: "reserved"},
		{name: "BaseSizeMismatch", delta: delta(12, 5, [2]int{0, 5}), wantErr: "base size"},
		{name: "ResultTooShort", delta: delta(13, 6, [2]int{0, 5}), wantErr: "instead of"},
		{name: "ResultAboveLimit", delta: delta(13, 1<<20, [2]int{0, 5}), wantErr: "above the limit"},
		{name: "SizeTruncated", delta: []byte{0x80}, wantErr: "truncated"},
		{name: "SizeOverflows", delta: bytes.Repeat([]byte{0x80}, 11), wantErr: "overflows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyDelta(base, tt.delta, 1<<10)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(result))
		})
	}

	t.Run("CopySizeZeroMeans64KiB", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 0x10000)
		result, err := ApplyDelta(big, delta(0x10000, 0x10000, []byte{0x80}), 1<<20)
		require.NoError(t, err)
		assert.Equal(t, big, result)
	})
}

func TestReadWrite(t *testing.T) {
	format := object.SHA1
	objects := []*Object{
		{Type: object.TypeBlob, Data: []byte("Hello, World!")},
		{Type: object.TypeBlob, Data: nil},
		{Type: object.TypeBlob, Data: bytes.Repeat([]byte("large "), 10000)},
		{Type: object.TypeCommit, Data: []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n\nmsg\n")},
	}
	var buf bytes.Buffer
	require.NoError(t, Write(&buf, format, objects))

	read, err := Read(&buf, format, Limits{MaxObjectSize: 1 << 20, MaxTotalSize: 1 << 20})
	require.NoError(t, err)
	require.Len(t, read, len(objects))
	for _, obj := range objects {
		got := read[format.Hash(obj.Type, obj.Data)]
		require.NotNil(t, got)
		assert.Equal(t, obj.Type, got.Type)
		assert.Equal(t, len(obj.Data), len(got.Data))
	}
	// The ID of the empty blob is well known
	assert.Contains(t, read, object.OID("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"))
}

func TestReadDeltas(t *testing.T) {
	format := object.SHA1
	base := []byte("Hello, World!")
	baseOID := format.Hash(object.TypeBlob, base)
	rawBaseOID, err := baseOID.Raw()
	require.NoError(t, err)

	var buf bytes.Buffer
	pw := NewWriter(&buf, format, 4)
	// A ref-delta before its base
	require.NoError(t, pw.Object(object.TypeRefDelta, rawBaseOID, delta(13, 5, [2]int{0, 5})))
	baseOffset := pw.Offset()
	require.NoError(t, pw.Object(object.TypeBlob, nil, base))
	// An ofs-delta of the base and an ofs-delta of that delta
	ofsOffset := pw.Offset()
	require.NoError(t, pw.Object(object.TypeOfsDelta, EncodeOffsetDistance(ofsOffset-baseOffset), delta(13, 6, [2]int{7, 6})))
	chainOffset := pw.Offset()
	require.NoError(t, pw.Object(object.TypeOfsDelta, EncodeOffsetDistance(chainOffset-ofsOffset), delta(6, 7, [2]int{0, 6}, "?")))
	require.NoError(t, pw.Close())

	read, err := Read(&buf, format, Limits{MaxObjectSize: 1 << 20, MaxTotalSize: 1 << 20})
	require.NoError(t, err)
	for _, want := range []string{"Hello, World!", "Hello", "World!", "World!?"} {
		obj := read[format.Hash(object.TypeBlob, []byte(want))]
		if assert.NotNil(t, obj, want) {
			assert.Equal(t, object.TypeBlob, obj.Type)
			assert.Equal(t, want, string(obj.Data))
		}
	}
}

func TestEncodeOffsetDistance(t *testing.T) {
	for _, distance := range []int64{1, 127, 128, 255, 16511, 16512, 1 << 30} {
		got, err := readOffsetDistance(bytes.NewReader(EncodeOffsetDistance(distance)))
		require.NoError(t, err)
		assert.Equal(t, distance, got)
	}
}

func TestReadErrors(t *testing.T) {
	format := object.SHA1
	valid := func(objects ...*Object) []byte {
		var buf bytes.Buffer
		require.NoError(t, Write(&buf, format, objects))
		return buf.Bytes()
	}
	blob := &Object{Type: object.TypeBlob, Data: []byte("content")}
	withObject := func(typ object.Type, base, data []byte) []byte {
		var buf bytes.Buffer
		pw := NewWriter(&buf, format, 1)
		require.NoError(t, pw.Object(typ, base, data))
		require.NoError(t, pw.Close())
		return buf.Bytes()
	}

	tests := []struct {
		name    string
		pack    []byte
		wantErr string
	}{
		{name: "Empty", pack: nil, wantErr: "header"},
		{name: "Signature", pack: append([]byte("KCAP"), valid(blob)[4:]...), wantErr: "signature"},
		{name: "Version", pack: append(append([]byte("PACK"), 0, 0, 0, 9), valid(blob)[8:]...), wantErr: "version 9"},
		{name: "Checksum", pack: func() []byte { p := valid(blob); p[len(p)-1] ^= 1; return p }(), wantErr: "checksum mismatch"},
		{name: "TruncatedChecksum", pack: func() []byte { p := valid(blob); return p[:len(p)-5] }(), wantErr: "checksum"},
		{name: "Truncated", pack: valid(blob)[:16], wantErr: "inflating"},
		{name: "MoreObjectsThanData", pack: func() []byte { p := valid(blob); p[11] = 2; return p }(), wantErr: ""},
		{name: "ObjectAboveLimit", pack: valid(&Object{Type: object.TypeBlob, Data: make([]byte, 2000)}), wantErr: "above the limit"},
		{name: "InvalidType", pack: withObject(5, nil, []byte("x")), wantErr: "invalid packfile object type 5"},
		{name: "OfsDeltaBeforeStart", pack: withObject(object.TypeOfsDelta, EncodeOffsetDistance(100), delta(1, 1, "x")), wantErr: "base distance"},
		{name: "RefDeltaWithoutBase", pack: withObject(object.TypeRefDelta, make([]byte, 20), delta(1, 1, "x")), wantErr: "without base"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(tt.pack), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 1000})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("InflatedSizeMismatch", func(t *testing.T) {
		// A header that declares 3 bytes for 7 bytes of data
		var buf bytes.Buffer
		pw := NewWriter(&buf, format, 1)
		pw.write([]byte{byte(object.TypeBlob)<<4 | 3})
		pw.write(withObject(object.TypeBlob, nil, []byte("content"))[12+1 : len(withObject(object.TypeBlob, nil, []byte("content")))-20])
		require.NoError(t, pw.Close())
		_, err := Read(&buf, format, Limits{MaxObjectSize: 1000, MaxTotalSize: 1000})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "instead of 3"), err)
	})

	t.Run("ObjectTooLargeError", func(t *testing.T) {
		pack := valid(&Object{Type: object.TypeBlob, Data: make([]byte, 2000)})
		_, err := Read(bytes.NewReader(pack), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 1000})
		var tooLarge *ObjectTooLargeError
		require.ErrorAs(t, err, &tooLarge)
		assert.Equal(t, object.TypeBlob, tooLarge.Type)
		assert.Equal(t, int64(2000), tooLarge.Size)
		assert.ErrorIs(t, err, ErrTooLarge)
	})

	t.Run("TotalSizeAboveLimit", func(t *testing.T) {
		// Every object is within MaxObjectSize, together they are not,
		// like a pack of many highly compressed objects
		var objects []*Object
		for i := range 5 {
			data := bytes.Repeat([]byte{byte(i)}, 300)
			objects = append(objects, &Object{Type: object.TypeBlob, Data: data})
		}
		_, err := Read(bytes.NewReader(valid(objects...)), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 1000})
		require.ErrorIs(t, err, ErrTooLarge)
		assert.Contains(t, err.Error(), "total size limit")

		_, err = Read(bytes.NewReader(valid(objects...)), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 1500})
		require.NoError(t, err)
	})

	t.Run("DeltaResultsCountTowardsTotalSize", func(t *testing.T) {
		// A 13 byte base at offset 12 and a 10 byte delta
		// that creates 13 bytes: 36 bytes in total
		base := []byte("Hello, World!")
		var buf bytes.Buffer
		pw := NewWriter(&buf, format, 2)
		require.NoError(t, pw.Object(object.TypeBlob, nil, base))
		require.NoError(t, pw.Object(object.TypeOfsDelta, EncodeOffsetDistance(pw.Offset()-12), delta(13, 13, [2]int{0, 13})))
		require.NoError(t, pw.Close())

		_, err := Read(bytes.NewReader(buf.Bytes()), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 25})
		require.ErrorIs(t, err, ErrTooLarge)
		_, err = Read(bytes.NewReader(buf.Bytes()), format, Limits{MaxObjectSize: 1000, MaxTotalSize: 40})
		require.NoError(t, err)
	})
}

func TestReadReverseRefDeltaChain(t *testing.T) {
	// A hostile server can send a chain of ref-deltas in reverse order, each
	// delta before the delta that creates its base. Resolving them must take
	// time linear in the number of deltas, a quadratic resolution takes
	// minutes for this chain.
	const n = 50000
	format := object.SHA1
	blob := func(i int) []byte { return []byte(fmt.Sprintf("%08d", i)) }

	var buf bytes.Buffer
	pw := NewWriter(&buf, format, n+1)
	for i := n; i > 0; i-- {
		raw, err := format.Hash(object.TypeBlob, blob(i-1)).Raw()
		require.NoError(t, err)
		require.NoError(t, pw.Object(object.TypeRefDelta, raw, delta(8, 8, string(blob(i)))))
	}
	require.NoError(t, pw.Object(object.TypeBlob, nil, blob(0)))
	require.NoError(t, pw.Close())

	start := time.Now()
	objects, err := Read(&buf, format, Limits{MaxObjectSize: 1 << 20, MaxTotalSize: 1 << 30})
	require.NoError(t, err)
	assert.Len(t, objects, n+1)
	assert.Contains(t, objects, format.Hash(object.TypeBlob, blob(n)))
	// gstack-timing-ok: the linear resolution takes milliseconds, the quadratic one minutes
	assert.Less(t, time.Since(start), 10*time.Second)
}
