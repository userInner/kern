// Package id generates sortable, locally unique identifiers.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New returns a UUIDv7-compatible identifier.
func New() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating random id: %w", err)
	}

	milliseconds := uint64(time.Now().UnixMilli())
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80

	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])

	return string(encoded), nil
}
