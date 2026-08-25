package allow

import (
	"net/url"
	"strings"
)

func Host(endpoint, allowedHost string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(parsed.Hostname()), strings.ToLower(allowedHost))
}
