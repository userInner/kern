package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	ErrMalformed = errors.New("malformed signature")
	ErrExpired   = errors.New("expired signature")
	ErrInvalid   = errors.New("invalid signature")
)

func Verify(secret string, body []byte, header string, now time.Time, tolerance time.Duration) error {
	parts := strings.Split(header, ",")
	if len(parts) != 2 { return ErrMalformed }
	timestamp := strings.TrimPrefix(parts[0], "t=")
	digest, err := hex.DecodeString(strings.TrimPrefix(parts[1], "v1="))
	if err != nil { return ErrMalformed }
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(body)))
	if !hmac.Equal(digest, mac.Sum(nil)) { return ErrInvalid }
	return nil
}
