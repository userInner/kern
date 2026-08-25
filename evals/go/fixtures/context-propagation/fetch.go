package fetch

import (
	"context"
	"net/http"
)

type Client interface {
	Do(*http.Request) (*http.Response, error)
}

func Fetch(ctx context.Context, client Client, endpoint string) error {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
