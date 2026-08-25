package allow

import "testing"

func TestHostAllowlist(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{endpoint: "https://example.com/data", want: true},
		{endpoint: "https://api.example.com/data", want: true},
		{endpoint: "https://API.EXAMPLE.COM/data", want: true},
		{endpoint: "https://evil-example.com/data", want: false},
		{endpoint: "not a url", want: false},
	}
	for _, test := range tests {
		if got := Host(test.endpoint, "example.com"); got != test.want {
			t.Errorf("Host(%q) = %t, want %t", test.endpoint, got, test.want)
		}
	}
}
