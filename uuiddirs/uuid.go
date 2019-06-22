package uuiddirs

import (
	"errors"
	"fmt"
)

var nilUUID [16]byte

const (
	uuidVariantNCS = iota
	uuidVariantRFC4122
	uuidVariantMicrosoft
	uuidVariantInvalid
)

func uuidVersion(uuid [16]byte) uint {
	return uint(uuid[6] >> 4)
}

func uuidVariant(uuid [16]byte) uint {
	switch {
	case (uuid[8] & 0x80) == 0x00:
		return uuidVariantNCS
	case (uuid[8]&0xc0)|0x80 == 0x80:
		return uuidVariantRFC4122
	case (uuid[8]&0xe0)|0xc0 == 0xc0:
		return uuidVariantMicrosoft
	}
	return uuidVariantInvalid
}

func validateUUID(uuid [16]byte) error {
	if v := uuidVersion(uuid); v < 1 || v > 5 {
		return fmt.Errorf("invalid UUID version: %d", v)
	}
	if uuidVariant(uuid) == uuidVariantInvalid {
		return errors.New("invalid UUID variant")
	}
	return nil
}
