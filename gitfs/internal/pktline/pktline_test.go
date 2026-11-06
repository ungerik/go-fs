package pktline

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncoderDecoder(t *testing.T) {
	var e Encoder
	e.Line("command=ls-refs")
	e.Delim()
	e.Packet([]byte("raw"))
	e.Flush()
	assert.Equal(t, "0014command=ls-refs\n00010007raw0000", e.String())

	d := NewDecoder(&e.Buffer)
	kind, line, err := d.NextLine()
	require.NoError(t, err)
	assert.Equal(t, Data, kind)
	assert.Equal(t, "command=ls-refs", line)
	kind, _, err = d.Next()
	require.NoError(t, err)
	assert.Equal(t, Delim, kind)
	kind, data, err := d.Next()
	require.NoError(t, err)
	assert.Equal(t, Data, kind)
	assert.Equal(t, "raw", string(data))
	kind, _, err = d.Next()
	require.NoError(t, err)
	assert.Equal(t, Flush, kind)
	_, _, err = d.Next()
	assert.ErrorIs(t, err, io.EOF)

	kind, _, err = NewDecoder(strings.NewReader("0002")).Next()
	require.NoError(t, err)
	assert.Equal(t, ResponseEnd, kind)

	assert.Panics(t, func() { e.Packet(nil) })
	assert.Panics(t, func() { e.Packet(make([]byte, MaxLength)) })
}

func TestDecoderErrors(t *testing.T) {
	for _, input := range []string{"0003", "zzzz", "fff0" + strings.Repeat("x", 10)} {
		_, _, err := NewDecoder(strings.NewReader(input)).Next()
		assert.Error(t, err, input)
	}
	_, _, err := NewDecoder(strings.NewReader("0010abc")).Next()
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	_, _, err = NewDecoder(strings.NewReader("ffff")).Next()
	assert.ErrorContains(t, err, "exceeds the maximum")
}

func sideband(packets ...string) *SidebandReader {
	var e Encoder
	for _, p := range packets {
		if p == "" {
			e.Flush()
			continue
		}
		e.Packet([]byte(p))
	}
	return NewSidebandReader(NewDecoder(bytes.NewReader(e.Bytes())))
}

func TestSidebandReader(t *testing.T) {
	data, err := io.ReadAll(sideband("\x01PA", "\x02progress", "\x01CK", ""))
	require.NoError(t, err)
	assert.Equal(t, "PACK", string(data))

	_, err = io.ReadAll(sideband("\x01PA", "\x03fatal: bad object\n", ""))
	var remote *RemoteError
	require.True(t, errors.As(err, &remote), err)
	assert.Equal(t, "fatal: bad object", remote.Message)
	assert.Contains(t, remote.Error(), "git server error")

	_, err = io.ReadAll(sideband("\x01PA"))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "missing flush")

	_, err = io.ReadAll(sideband("\x07x", ""))
	assert.ErrorContains(t, err, "invalid side-band channel")

	_, err = io.ReadAll(NewSidebandReader(NewDecoder(strings.NewReader("0001"))))
	assert.ErrorContains(t, err, "unexpected packet kind")

	_, err = io.ReadAll(NewSidebandReader(NewDecoder(strings.NewReader("0003"))))
	assert.ErrorContains(t, err, "invalid packet length")
}
