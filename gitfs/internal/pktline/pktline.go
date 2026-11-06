// Package pktline implements the pkt-line framing of the git protocol
// and the demultiplexing of side-band-64k packfile streams,
// see gitprotocol-common(5) and gitprotocol-v2(5).
package pktline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// MaxLength is the maximum length of a pkt-line including its 4 byte header.
const MaxLength = 65520

// Kind is the kind of a packet.
type Kind int

// Packet kinds.
const (
	Data        Kind = iota // a line with data
	Flush                   // "0000", ends a message
	Delim                   // "0001", separates the sections of a message
	ResponseEnd             // "0002", ends a response in stateless connections
)

// Encoder writes pkt-lines to a buffer.
// The zero value is ready to use.
type Encoder struct {
	bytes.Buffer
}

// Line appends a data packet with the text and a trailing newline.
func (e *Encoder) Line(text string) {
	e.Packet([]byte(text + "\n"))
}

// Packet appends a data packet with the data. It panics if the data
// doesn't fit into a packet, which is a programming error.
func (e *Encoder) Packet(data []byte) {
	if len(data) == 0 || len(data)+4 > MaxLength {
		panic(fmt.Sprintf("pktline: invalid data length %d", len(data)))
	}
	_, _ = fmt.Fprintf(&e.Buffer, "%04x", len(data)+4)
	_, _ = e.Write(data)
}

// Flush appends a flush packet.
func (e *Encoder) Flush() {
	_, _ = e.WriteString("0000")
}

// Delim appends a delimiter packet.
func (e *Encoder) Delim() {
	_, _ = e.WriteString("0001")
}

// Decoder reads pkt-lines.
type Decoder struct {
	r   io.Reader
	buf [MaxLength]byte
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// Next returns the next packet. The data is only valid
// until the next call. It returns io.ErrUnexpectedEOF
// if the stream ends within a packet and io.EOF if it ends
// before one.
func (d *Decoder) Next() (Kind, []byte, error) {
	header := d.buf[:4]
	_, err := io.ReadFull(d.r, header)
	if err != nil {
		return 0, nil, err
	}
	length, err := strconv.ParseUint(string(header), 16, 16)
	if err != nil {
		return 0, nil, fmt.Errorf("pktline: invalid packet length %q", header)
	}
	switch length {
	case 0:
		return Flush, nil, nil
	case 1:
		return Delim, nil, nil
	case 2:
		return ResponseEnd, nil, nil
	case 3:
		return 0, nil, fmt.Errorf("pktline: invalid packet length %q", header)
	}
	if length > MaxLength {
		return 0, nil, fmt.Errorf("pktline: packet length %d exceeds the maximum %d", length, MaxLength)
	}
	data := d.buf[4:length]
	_, err = io.ReadFull(d.r, data)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return 0, nil, err
	}
	return Data, data, nil
}

// NextLine returns the next packet with the data
// as string without a trailing newline.
func (d *Decoder) NextLine() (Kind, string, error) {
	kind, data, err := d.Next()
	return kind, string(bytes.TrimSuffix(data, []byte{'\n'})), err
}

// RemoteError is an error message sent by the server,
// in an "ERR" packet or on side-band channel 3.
type RemoteError struct {
	Message string
}

func (e *RemoteError) Error() string {
	return "git server error: " + e.Message
}

// SidebandReader returns the data of side-band channel 1 of the packets
// of a side-band-64k stream until a flush packet. The progress messages
// of channel 2 are dropped, a message on channel 3 ends the stream with
// a *RemoteError.
type SidebandReader struct {
	d       *Decoder
	pending []byte
	err     error
}

// NewSidebandReader returns a SidebandReader for the packets of d.
func NewSidebandReader(d *Decoder) *SidebandReader {
	return &SidebandReader{d: d}
}

// Read implements io.Reader.
func (s *SidebandReader) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		kind, data, err := s.d.Next()
		switch {
		case errors.Is(err, io.EOF):
			s.err = io.ErrUnexpectedEOF // a stream ends with a flush packet
		case err != nil:
			s.err = err
		case kind == Flush:
			s.err = io.EOF
		case kind != Data:
			s.err = fmt.Errorf("pktline: unexpected packet kind %d in side-band stream", kind)
		case len(data) == 0:
			s.err = errors.New("pktline: side-band packet without channel")
		default:
			switch data[0] {
			case 1:
				s.pending = data[1:]
			case 2:
				// progress messages
			case 3:
				s.err = &RemoteError{Message: string(bytes.TrimSpace(data[1:]))}
			default:
				s.err = fmt.Errorf("pktline: invalid side-band channel %d", data[0])
			}
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
