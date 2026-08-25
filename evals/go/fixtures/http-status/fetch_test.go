package fetch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchJSONRejectsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()
	_, err := FetchJSON(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("FetchJSON() error = %v, want status diagnostic", err)
	}
}

func TestFetchJSONReturnsSuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	data, err := FetchJSON(server.Client(), server.URL)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("FetchJSON() = %q, %v", data, err)
	}
}
