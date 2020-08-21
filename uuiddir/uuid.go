package uuiddir

import (
	"encoding/hex"
	"errors"
	"fmt"
)

var nilUUID [16]byte

func mustParseUUID(str string) [16]byte {
	id, err := parseUUID(str)
	if err != nil {
		panic(err)
	}
	return id
}

func parseUUID(str string) (id [16]byte, err error) {
	if len(str) != 36 {
		return [16]byte{}, fmt.Errorf("invalid UUID string length: %q", str)
	}
	if str[8] != '-' || str[13] != '-' || str[18] != '-' || str[23] != '-' {
		return [16]byte{}, fmt.Errorf("invalid UUID string format: %q", str)
	}

	b := []byte(str)

	_, err = hex.Decode(id[0:4], b[0:8])
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}
	_, err = hex.Decode(id[4:6], b[9:13])
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}
	_, err = hex.Decode(id[6:8], b[14:18])
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}
	_, err = hex.Decode(id[8:10], b[19:23])
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}
	_, err = hex.Decode(id[10:16], b[24:36])
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}

	err = validateUUID(id)
	if err != nil {
		return [16]byte{}, fmt.Errorf("error %w parsing UUID string: %q", err, str)
	}

	return id, nil
}

func validateUUID(id [16]byte) error {
	if version := id[6] >> 4; version < 1 || version > 5 {
		return fmt.Errorf("invalid UUID version: %d", version)
	}
	switch {
	case (id[8] & 0x80) == 0x00:
		// Variant NCS
	case (id[8]&0xc0)|0x80 == 0x80:
		// Variant RFC4122
	case (id[8]&0xe0)|0xc0 == 0xc0:
		// Variant Microsoft
	default:
		return errors.New("invalid UUID variant")
	}
	return nil
}
