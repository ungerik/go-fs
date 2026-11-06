// Package pack reads git packfiles (version 2 and 3) into memory,
// resolving the deltas of the objects and verifying their checksum,
// see gitformat-pack(5).
package pack

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/ungerik/go-fs/gitfs/internal/object"
)

// Object is an object read from a packfile.
type Object struct {
	Type object.Type // never one of the delta types
	Data []byte
}

// ErrTooLarge is wrapped by the errors of Read and ApplyDelta
// for objects or packfiles above their size limits.
var ErrTooLarge = errors.New("above the size limit")

// ObjectTooLargeError is the error of Read for an object whose size
// in its header is above the maximum object size. It unwraps to ErrTooLarge.
type ObjectTooLargeError struct {
	Offset int64
	Type   object.Type // can be one of the delta types
	Size   int64
	Limit  int64
}

func (e *ObjectTooLargeError) Error() string {
	return fmt.Sprintf("packfile object at offset %d has size %d above the limit %d", e.Offset, e.Size, e.Limit)
}

func (e *ObjectTooLargeError) Unwrap() error {
	return ErrTooLarge
}

// Limits of Read.
type Limits struct {
	// MaxObjectSize is the maximum size of an object or delta.
	MaxObjectSize int64
	// MaxTotalSize is the maximum sum of the inflated sizes of all
	// objects and deltas of a packfile and of the objects that the
	// deltas create, which bounds the memory that Read uses.
	MaxTotalSize int64
}

// Read reads a whole packfile and returns its objects by OID.
// It returns an error for objects, deltas or a packfile above the limits,
// for deltas without a base object in the packfile and if the checksum
// of the packfile doesn't match.
func Read(r io.Reader, format object.Format, limits Limits) (map[object.OID]*Object, error) {
	pr := &packReader{r: bufio.NewReaderSize(r, 64*1024), hash: format.New()}
	var header [12]byte
	_, err := io.ReadFull(pr, header[:])
	if err != nil {
		return nil, fmt.Errorf("reading packfile header: %w", err)
	}
	if string(header[:4]) != "PACK" {
		return nil, errors.New("invalid packfile signature")
	}
	if version := binary.BigEndian.Uint32(header[4:8]); version != 2 && version != 3 {
		return nil, fmt.Errorf("unsupported packfile version %d", version)
	}
	count := binary.BigEndian.Uint32(header[8:12])

	type pending struct {
		offset     int64
		baseOffset int64      // of an ofs-delta
		baseOID    object.OID // of a ref-delta
		delta      []byte
	}
	var (
		objects   = make(map[object.OID]*Object)
		byOffset  = make(map[int64]*Object) // the bases of ofs-deltas
		deltas    []pending
		totalSize int64
	)
	reserve := func(offset, size int64) error {
		if size > limits.MaxTotalSize-totalSize {
			return fmt.Errorf("packfile object at offset %d exceeds the total size limit %d of a packfile: %w", offset, limits.MaxTotalSize, ErrTooLarge)
		}
		totalSize += size
		return nil
	}
	for range count {
		offset := pr.offset
		typ, size, err := readObjectHeader(pr)
		if err != nil {
			return nil, fmt.Errorf("reading packfile object header at offset %d: %w", offset, err)
		}
		p := pending{offset: offset, baseOffset: -1}
		switch typ {
		case object.TypeCommit, object.TypeTree, object.TypeBlob, object.TypeTag:
		case object.TypeOfsDelta:
			distance, err := readOffsetDistance(pr)
			if err != nil {
				return nil, fmt.Errorf("reading ofs-delta at offset %d: %w", offset, err)
			}
			if distance <= 0 || distance > offset {
				return nil, fmt.Errorf("invalid ofs-delta base distance %d at offset %d", distance, offset)
			}
			p.baseOffset = offset - distance
		case object.TypeRefDelta:
			raw := make([]byte, format.Size)
			_, err := io.ReadFull(pr, raw)
			if err != nil {
				return nil, fmt.Errorf("reading ref-delta at offset %d: %w", offset, err)
			}
			p.baseOID = object.RawOID(raw)
		default:
			return nil, fmt.Errorf("invalid packfile object type %d at offset %d", typ, offset)
		}
		if size > limits.MaxObjectSize {
			return nil, &ObjectTooLargeError{Offset: offset, Type: typ, Size: size, Limit: limits.MaxObjectSize}
		}
		err = reserve(offset, size)
		if err != nil {
			return nil, err
		}
		data, err := inflate(pr, size)
		if err != nil {
			return nil, fmt.Errorf("inflating packfile object at offset %d: %w", offset, err)
		}
		if typ == object.TypeOfsDelta || typ == object.TypeRefDelta {
			p.delta = data
			deltas = append(deltas, p)
			continue
		}
		obj := &Object{Type: typ, Data: data}
		objects[format.Hash(typ, data)] = obj
		byOffset[offset] = obj
	}

	// The checksum covers everything before it
	pr.flushHash()
	sum := pr.hash.Sum(nil)
	trailer := make([]byte, format.Size)
	_, err = io.ReadFull(pr.r, trailer)
	if err != nil {
		return nil, fmt.Errorf("reading packfile checksum: %w", err)
	}
	if !bytes.Equal(sum, trailer) {
		return nil, errors.New("packfile checksum mismatch")
	}

	// A delta can have another delta as base, in a ref-delta
	// also one that follows it, so resolve until nothing changes
	for len(deltas) > 0 {
		unresolved := deltas[:0]
		for _, p := range deltas {
			var base *Object
			if p.baseOffset >= 0 {
				base = byOffset[p.baseOffset]
			} else {
				base = objects[p.baseOID]
			}
			if base == nil {
				unresolved = append(unresolved, p)
				continue
			}
			resultSize, err := DeltaResultSize(p.delta)
			if err != nil {
				return nil, fmt.Errorf("applying delta at offset %d: %w", p.offset, err)
			}
			if resultSize <= uint64(limits.MaxObjectSize) { //#nosec G115 -- the limit is not negative
				// Larger results are rejected by ApplyDelta
				err = reserve(p.offset, int64(resultSize)) //#nosec G115 -- checked against the limit
				if err != nil {
					return nil, err
				}
			}
			data, err := ApplyDelta(base.Data, p.delta, limits.MaxObjectSize)
			if err != nil {
				return nil, fmt.Errorf("applying delta at offset %d: %w", p.offset, err)
			}
			obj := &Object{Type: base.Type, Data: data}
			objects[format.Hash(obj.Type, data)] = obj
			byOffset[p.offset] = obj
		}
		if len(unresolved) == len(deltas) {
			return nil, fmt.Errorf("packfile has %d deltas without base object", len(unresolved))
		}
		deltas = unresolved
	}
	return objects, nil
}

// readObjectHeader reads the type and size of an object:
// the bits 6-4 of the first byte are the type, its bits 3-0 the
// lowest bits of the size and every following byte while the
// most significant bit is set adds 7 more bits of the size.
func readObjectHeader(r io.ByteReader) (object.Type, int64, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	typ := object.Type((c >> 4) & 0x07)
	size := int64(c & 0x0f)
	for shift := 4; c&0x80 != 0; shift += 7 {
		if shift > 56 {
			return 0, 0, errors.New("object size overflows")
		}
		c, err = r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		size |= int64(c&0x7f) << shift
	}
	return typ, size, nil
}

// readOffsetDistance reads the distance of the base object of an
// ofs-delta, a big-endian number of 7 bit groups where every group
// but the last adds one to its value, see gitformat-pack(5).
func readOffsetDistance(r io.ByteReader) (int64, error) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	distance := int64(c & 0x7f)
	for c&0x80 != 0 {
		if distance >= 1<<55 {
			return 0, errors.New("offset overflows")
		}
		c, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		distance = (distance+1)<<7 | int64(c&0x7f)
	}
	return distance, nil
}

// inflate reads a zlib stream that must inflate to exactly size bytes.
// The packReader implements io.ByteReader, so zlib reads no further
// than the end of the stream.
func inflate(r *packReader, size int64) ([]byte, error) {
	z, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	var data bytes.Buffer
	if size < 1<<20 {
		data.Grow(int(size)) // the size is checked against the data, don't trust larger ones
	}
	// Reading one more byte than size finds a stream that is too long
	// and reaches the end of the stream, where zlib checks its checksum
	n, err := io.Copy(&data, io.LimitReader(z, size+1))
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, fmt.Errorf("object inflates to %d bytes instead of %d", n, size)
	}
	return data.Bytes(), nil
}

// ApplyDelta returns the object that a delta creates from base.
// The delta starts with the sizes of the base and the result,
// followed by instructions that copy a range of base or insert
// the data that follows the instruction, see gitformat-pack(5).
func ApplyDelta(base, delta []byte, maxObjectSize int64) ([]byte, error) {
	baseSize, delta, err := readDeltaSize(delta)
	if err != nil {
		return nil, err
	}
	if baseSize != uint64(len(base)) {
		return nil, fmt.Errorf("delta base size %d doesn't match the base object size %d", baseSize, len(base))
	}
	resultSize, delta, err := readDeltaSize(delta)
	if err != nil {
		return nil, err
	}
	if maxObjectSize < 0 || resultSize > uint64(maxObjectSize) { //#nosec G115 -- maxObjectSize is not negative
		return nil, fmt.Errorf("delta result size %d is above the limit %d: %w", resultSize, maxObjectSize, ErrTooLarge)
	}
	result := make([]byte, 0, resultSize)
	for len(delta) > 0 {
		cmd := delta[0]
		delta = delta[1:]
		switch {
		case cmd&0x80 != 0:
			// Copy from base: bits 0-3 select the bytes of the
			// offset, bits 4-6 the bytes of the size that follow
			var offset, size uint64
			for i := range 4 {
				if cmd&(1<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta copy instruction is truncated")
					}
					offset |= uint64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			for i := range 3 {
				if cmd&(0x10<<i) != 0 {
					if len(delta) == 0 {
						return nil, errors.New("delta copy instruction is truncated")
					}
					size |= uint64(delta[0]) << (8 * i)
					delta = delta[1:]
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if offset+size > uint64(len(base)) || uint64(len(result))+size > resultSize {
				return nil, errors.New("delta copy instruction is out of bounds")
			}
			result = append(result, base[offset:offset+size]...)
		case cmd != 0:
			// Insert the next cmd bytes
			size := int(cmd)
			if size > len(delta) || uint64(len(result))+uint64(size) > resultSize { //#nosec G115 -- lengths are not negative
				return nil, errors.New("delta insert instruction is out of bounds")
			}
			result = append(result, delta[:size]...)
			delta = delta[size:]
		default:
			return nil, errors.New("delta has reserved instruction 0")
		}
	}
	if uint64(len(result)) != resultSize {
		return nil, fmt.Errorf("delta result has %d bytes instead of %d", len(result), resultSize)
	}
	return result, nil
}

// DeltaResultSize returns the size of the object that a delta creates.
func DeltaResultSize(delta []byte) (uint64, error) {
	_, delta, err := readDeltaSize(delta)
	if err != nil {
		return 0, err
	}
	resultSize, _, err := readDeltaSize(delta)
	return resultSize, err
}

// readDeltaSize reads a little-endian number of 7 bit groups
// and returns it with the rest of the delta.
func readDeltaSize(delta []byte) (uint64, []byte, error) {
	var size uint64
	for shift := 0; ; shift += 7 {
		if len(delta) == 0 {
			return 0, nil, errors.New("delta size is truncated")
		}
		if shift > 63 {
			return 0, nil, errors.New("delta size overflows")
		}
		c := delta[0]
		delta = delta[1:]
		size |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return size, delta, nil
		}
	}
}

// packReader counts and hashes the consumed bytes of a packfile.
// It implements io.ByteReader, so that zlib doesn't read ahead.
type packReader struct {
	r       *bufio.Reader
	offset  int64
	hash    hash.Hash
	pending []byte // consumed bytes not yet written to hash
}

func (p *packReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.consumed(b[:n])
	return n, err
}

func (p *packReader) ReadByte() (byte, error) {
	c, err := p.r.ReadByte()
	if err != nil {
		return 0, err
	}
	p.pending = append(p.pending, c)
	p.offset++
	if len(p.pending) >= 32*1024 {
		p.flushHash()
	}
	return c, nil
}

func (p *packReader) consumed(b []byte) {
	p.offset += int64(len(b))
	p.flushHash()
	_, _ = p.hash.Write(b)
}

func (p *packReader) flushHash() {
	_, _ = p.hash.Write(p.pending)
	p.pending = p.pending[:0]
}

// Write writes a packfile version 2 with the objects, without deltas.
// gitfs only reads packfiles, Write exists for the tests and their
// fake git servers.
func Write(w io.Writer, format object.Format, objects []*Object) error {
	pw := NewWriter(w, format, len(objects))
	for _, obj := range objects {
		err := pw.Object(obj.Type, nil, obj.Data)
		if err != nil {
			return err
		}
	}
	return pw.Close()
}

// Writer writes a packfile version 2, also with deltas, for tests.
type Writer struct {
	w       io.Writer // writes to the output and the hash
	trailer io.Writer // the output
	hash    hash.Hash
	offset  int64
	err     error
}

// NewWriter writes the header of a packfile with count objects.
func NewWriter(w io.Writer, format object.Format, count int) *Writer {
	pw := &Writer{hash: format.New()}
	pw.w = io.MultiWriter(w, pw.hash)
	var header [12]byte
	copy(header[:4], "PACK")
	binary.BigEndian.PutUint32(header[4:8], 2)
	binary.BigEndian.PutUint32(header[8:12], uint32(count)) //#nosec G115 -- test helper with few objects
	pw.write(header[:])
	pw.trailer = w
	return pw
}

// Offset returns the offset of the next object.
func (pw *Writer) Offset() int64 {
	return pw.offset
}

// Object writes an object: its header with the type and the size of
// data, the uncompressed base of a delta (the encoded distance of an
// ofs-delta or the raw object ID of a ref-delta), and the compressed data.
func (pw *Writer) Object(typ object.Type, base, data []byte) error {
	size := len(data)
	c := byte(typ&0x07)<<4 | byte(size&0x0f) //#nosec G115 -- both are masked to their bits
	size >>= 4
	var header []byte
	for size > 0 {
		header = append(header, c|0x80)
		c = byte(size & 0x7f)
		size >>= 7
	}
	header = append(header, c)
	pw.write(header)
	pw.write(base)
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	_, _ = z.Write(data)
	_ = z.Close()
	pw.write(compressed.Bytes())
	return pw.err
}

// Close writes the checksum of the packfile.
func (pw *Writer) Close() error {
	if pw.err != nil {
		return pw.err
	}
	_, err := pw.trailer.Write(pw.hash.Sum(nil))
	return err
}

func (pw *Writer) write(b []byte) {
	if pw.err != nil {
		return
	}
	n, err := pw.w.Write(b)
	pw.offset += int64(n)
	pw.err = err
}

// EncodeOffsetDistance encodes the distance of the base
// of an ofs-delta, the inverse of readOffsetDistance.
func EncodeOffsetDistance(distance int64) []byte {
	encoded := []byte{byte(distance & 0x7f)}
	for distance >>= 7; distance > 0; distance >>= 7 {
		distance--
		encoded = append([]byte{byte(distance&0x7f) | 0x80}, encoded...)
	}
	return encoded
}
