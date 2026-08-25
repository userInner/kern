package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(body)))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyAcceptsValidSignatureInAnyV1Field(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	body := []byte(`{"event":"paid"}`)
	timestamp := fmt.Sprint(now.Unix())
	header := "v1=deadbeef,t=" + timestamp + ",v1=" + sign("secret", timestamp, body)
	if err := Verify("secret", body, header, now, 5*time.Minute); err != nil { t.Fatalf("Verify() error = %v", err) }
}

func TestVerifyRejectsExpiredFutureTamperedAndMalformed(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	body := []byte("payload")
	for _, test := range []struct { name, header string; want error }{
		{"expired", fmt.Sprintf("t=%d,v1=%s", now.Add(-6*time.Minute).Unix(), sign("s", fmt.Sprint(now.Add(-6*time.Minute).Unix()), body)), ErrExpired},
		{"future", fmt.Sprintf("t=%d,v1=%s", now.Add(6*time.Minute).Unix(), sign("s", fmt.Sprint(now.Add(6*time.Minute).Unix()), body)), ErrExpired},
		{"tampered", fmt.Sprintf("t=%d,v1=%s", now.Unix(), sign("s", fmt.Sprint(now.Unix()), []byte("other"))), ErrInvalid},
		{"missing timestamp", "v1=deadbeef", ErrMalformed},
		{"bad timestamp", "t=nope,v1=deadbeef", ErrMalformed},
		{"bad digest", fmt.Sprintf("t=%d,v1=xyz", now.Unix()), ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Verify("s", body, test.header, now, 5*time.Minute); !errors.Is(err, test.want) { t.Fatalf("Verify() error = %v, want %v", err, test.want) }
		})
	}
}
