package redact

import "strings"

func Secret(value, secret string) string {
	return strings.Replace(value, secret, "[REDACTED]", 1)
}
