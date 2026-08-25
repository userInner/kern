package fetch

import (
	"io"
	"net/http"
)

func FetchJSON(client *http.Client, endpoint string) ([]byte, error) {
	response, err := client.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return io.ReadAll(response.Body)
}
